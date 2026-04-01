package query

import (
	"context"
	"fmt"
	"time"

	"github.com/claude-code/go-claude-go/api"
	"github.com/claude-code/go-claude-go/compact"
	"github.com/claude-code/go-claude-go/tools"
	"github.com/claude-code/go-claude-go/types"
	"github.com/google/uuid"
)

// queryLoop is the core while(true) agent loop.  It mirrors the TypeScript
// queryLoop() function in src/query.ts.
//
// Each iteration:
//  1. Prepares messages (get slice after compact boundary)
//  2. Runs the three-layer context management pipeline
//     (snip → microcompact → autocompact)
//  3. Calls the Anthropic API (streaming)
//  4. Handles error recovery (413 prompt-too-long)
//  5. Checks for tool_use blocks; returns if none (turn complete)
//  6. Executes tools (concurrently / serially per partition)
//  7. Updates state and loops back
func queryLoop(
	ctx context.Context,
	params QueryParams,
	outCh chan<- types.Message,
) (types.Terminal, error) {
	state := initialState(params.Messages)

	for {
		// ── 1. Snapshot current state ────────────────────────────────────────
		messages := state.Messages
		turnCount := state.TurnCount

		// ── 2. Prepare messages for API ──────────────────────────────────────
		// Only send messages after the most recent compact boundary so the
		// context window doesn't grow unboundedly across compactions.
		msgsForQuery := types.GetMessagesAfterCompactBoundary(messages)

		// ── 3a. Snip — remove redundant intermediate tool outputs ────────────
		snipResult := compact.ApplySnipIfNeeded(msgsForQuery)
		msgsForQuery = snipResult.Messages

		// ── 3b. MicroCompact — dedup repeated tool_result blocks ─────────────
		msgsForQuery, _ = compact.ApplyMicroCompact(msgsForQuery)

		// ── 3c. AutoCompact — full summarisation if context is nearly full ───
		if params.AutoCompact.APIKey != "" {
			compResult, ok := compact.AutoCompactIfNeeded(
				ctx,
				msgsForQuery,
				params.AutoCompact,
				state.AutoCompactTracking,
			)
			if ok {
				// Emit boundary + summary messages downstream
				for _, m := range compResult.SummaryMessages {
					outCh <- m
				}
				msgsForQuery = compResult.SummaryMessages

				// Append compacted slice to the full history for subsequent turns
				newFullHistory := append(
					getMessagesBeforeCompactBoundary(messages),
					compResult.SummaryMessages...,
				)
				state = state.withMessages(newFullHistory).withAutoCompact(
					&compact.AutoCompactTrackingState{
						Compacted:   true,
						TurnCounter: 0,
						TurnID:      uuid.New().String(),
					},
				)
				messages = state.Messages
			}
		}

		// ── 4. Emit request-start marker ─────────────────────────────────────
		outCh <- &types.SystemMessage{
			Type:      types.MessageTypeSystem,
			Subtype:   types.SystemSubtypeInformational,
			UUID:      uuid.New().String(),
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
			Content:   fmt.Sprintf("stream_request_start turn=%d", turnCount),
			Level:     types.SystemLevelInfo,
		}

		// ── 5. Stream API call ────────────────────────────────────────────────
		toolsList := params.Registry.Enabled()
		streamParams := api.StreamParams{
			Messages:     msgsForQuery,
			SystemPrompt: params.SystemPrompt,
			Tools:        toolsList,
			Model:        params.Model,
		}

		var (
			finalAssistant *types.AssistantMessage
			toolUseBlocks  []*types.ToolUseBlock
			needsFollowUp  bool
			withheld413    bool
		)

		streamCh, streamErrCh := api.StreamMessage(ctx, params.APIClient, streamParams)

		for event := range streamCh {
			switch e := event.(type) {
			case *types.StreamDeltaEvent:
				// Delta events are informational; yield as system messages
				// so callers can stream text to the user.
				outCh <- &types.SystemMessage{
					Type:      types.MessageTypeSystem,
					Subtype:   types.SystemSubtypeInformational,
					UUID:      uuid.New().String(),
					Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
					Content:   e.Text,
					Level:     types.SystemLevelInfo,
				}
			case *types.AssistantMessage:
				finalAssistant = e
				for _, blk := range e.Msg.Content {
					if tb, ok := blk.(*types.ToolUseBlock); ok {
						toolUseBlocks = append(toolUseBlocks, tb)
						needsFollowUp = true
					}
				}
				outCh <- e
			}
		}

		if err := <-streamErrCh; err != nil {
			apiErr, isAPIErr := err.(*types.APIError)
			if isAPIErr && apiErr.Status == 413 {
				withheld413 = true
			} else {
				return types.Terminal{Reason: "api_error"}, err
			}
		}

		// ── 6. Error recovery: prompt-too-long ───────────────────────────────
		if withheld413 {
			if state.HasAttemptedReactiveCompact {
				// Already tried once — surface the error.
				return types.Terminal{Reason: "prompt_too_long"}, nil
			}
			// Attempt a reactive compact and retry.
			if params.AutoCompact.APIKey != "" {
				compResult, ok := compact.AutoCompactIfNeeded(
					ctx, msgsForQuery, params.AutoCompact, nil,
				)
				if ok {
					for _, m := range compResult.SummaryMessages {
						outCh <- m
					}
					newMessages := append(
						getMessagesBeforeCompactBoundary(messages),
						compResult.SummaryMessages...,
					)
					state = state.withMessages(newMessages)
					state.HasAttemptedReactiveCompact = true
					continue
				}
			}
			return types.Terminal{Reason: "prompt_too_long"}, nil
		}

		// ── 7. Terminal check — no tool calls → turn complete ────────────────
		if !needsFollowUp {
			return types.Terminal{Reason: "completed"}, nil
		}

		// ── 8. Max turns check ────────────────────────────────────────────────
		if params.MaxTurns > 0 && turnCount >= params.MaxTurns {
			return types.Terminal{Reason: "max_turns"}, nil
		}

		// ── 9. Context cancellation check ─────────────────────────────────────
		select {
		case <-ctx.Done():
			return types.Terminal{Reason: "aborted_streaming"}, nil
		default:
		}

		// ── 10. Execute tools ─────────────────────────────────────────────────
		toolCtx := tools.ToolContext{
			Ctx:        ctx,
			WorkingDir: params.WorkingDir,
			Messages:   messages,
			Registry:   params.Registry,
		}

		toolResultMsgs, err := tools.RunTools(
			toolUseBlocks,
			params.CanUseTool,
			toolCtx,
		)
		if err != nil {
			return types.Terminal{Reason: "tool_error"}, err
		}

		for _, msg := range toolResultMsgs {
			outCh <- msg
		}

		// ── 11. Update state and loop back ────────────────────────────────────
		newMessages := messages
		if finalAssistant != nil {
			newMessages = append(newMessages, finalAssistant)
		}
		newMessages = append(newMessages, toolResultMsgs...)

		reason := TransitionNextTurn
		state = State{
			Messages:                    newMessages,
			AutoCompactTracking:         state.AutoCompactTracking,
			MaxOutputTokensRecoveryCount: state.MaxOutputTokensRecoveryCount,
			HasAttemptedReactiveCompact: false, // reset for next turn
			MaxOutputTokensOverride:     state.MaxOutputTokensOverride,
			TurnCount:                   turnCount + 1,
			Transition:                  &reason,
		}
	}
}

// getMessagesBeforeCompactBoundary returns everything up to and including
// the last compact_boundary marker (the "archived" portion).
func getMessagesBeforeCompactBoundary(messages []types.Message) []types.Message {
	lastBoundary := -1
	for i, m := range messages {
		if types.IsCompactBoundary(m) {
			lastBoundary = i
		}
	}
	if lastBoundary == -1 {
		return nil
	}
	return messages[:lastBoundary+1]
}
