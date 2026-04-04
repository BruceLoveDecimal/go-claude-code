package query

import (
	"context"
	"fmt"

	"github.com/claude-code/go-claude-go/api"
	"github.com/claude-code/go-claude-go/compact"
	"github.com/claude-code/go-claude-go/tools"
	"github.com/claude-code/go-claude-go/types"
	"github.com/google/uuid"
)

const (
	// maxOutputTokensRecoveryLimit is the number of continuation nudges
	// injected before escalating to the larger token budget.
	maxOutputTokensRecoveryLimit = 3

	// maxOutputTokensEscalated is the escalated max_tokens value used after
	// exhausting the recovery attempts (default is 8096).
	maxOutputTokensEscalated = 64_000

	// continuationNudge is injected after a max_tokens stop to make the
	// model resume from where it left off.
	continuationNudge = "Continue from where you left off exactly, without any commentary."
)

// newQueryTracking creates the initial QueryChainTracking for a loop, or
// increments depth if tracking is already in progress (subagent path).
func newQueryTracking(existing *tools.QueryChainTracking) *tools.QueryChainTracking {
	if existing == nil {
		return &tools.QueryChainTracking{ChainID: uuid.New().String(), Depth: 0}
	}
	return &tools.QueryChainTracking{ChainID: existing.ChainID, Depth: existing.Depth + 1}
}

// queryLoop is the core while(true) agent loop.  It mirrors the TypeScript
// queryLoop() function in src/query.ts.
//
// Each iteration:
//  1. Prepares messages (get slice after compact boundary)
//  2. Runs the context management pipeline
//     (budget compact → snip → microcompact → autocompact)
//  3. Calls the Anthropic API (streaming)
//  4. Handles error recovery (413 prompt-too-long, overloaded/fallback)
//  5. Handles max_output_tokens recovery (nudge → escalate)
//  6. Checks for tool_use blocks; runs stop hooks if none (turn complete)
//  7. Executes tools (concurrently / serially per partition)
//  8. Updates state and loops back
func queryLoop(
	ctx context.Context,
	params QueryParams,
	outCh chan<- types.SDKMessage,
) (types.Terminal, error) {
	state := initialState(params.Messages)

	// currentModel can be switched to FallbackModel on overload; persists
	// for the rest of the session once switched.
	currentModel := params.Model

	for {
		// ── 1. Snapshot current state ────────────────────────────────────────
		messages := state.Messages
		turnCount := state.TurnCount

		// ── 2. Prepare messages for API ──────────────────────────────────────
		msgsForQuery := types.GetMessagesAfterCompactBoundary(messages)

		// ── 2a. Tool-result budget compaction (Phase 3e) ─────────────────────
		msgsForQuery = tools.ApplyToolResultBudget(
			msgsForQuery,
			tools.DefaultToolResultBudget,
			params.ContentReplacementState,
		)

		// ── 2b. Snip — remove redundant intermediate tool outputs ────────────
		snipResult := compact.ApplySnipIfNeeded(msgsForQuery)
		msgsForQuery = snipResult.Messages

		// ── 2c. MicroCompact — dedup repeated tool_result blocks ─────────────
		msgsForQuery, _ = compact.ApplyMicroCompact(msgsForQuery)

		// ── 2d. AutoCompact — full summarisation if context is nearly full ───
		if params.AutoCompact.APIKey != "" {
			compResult, ok := compact.AutoCompactIfNeeded(
				ctx,
				msgsForQuery,
				params.AutoCompact,
				state.AutoCompactTracking,
			)
			if ok {
				for _, m := range compResult.SummaryMessages {
					outCh <- m
				}
				msgsForQuery = compResult.SummaryMessages

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

		// ── 3. Emit request-start marker ─────────────────────────────────────
		outCh <- &types.RequestStartEvent{Type: fmt.Sprintf("stream_request_start turn=%d", turnCount)}

		// ── 4. Stream API call ────────────────────────────────────────────────
		maxTokens := 0 // 0 = use api default (8096)
		if state.MaxOutputTokensOverride != nil {
			maxTokens = *state.MaxOutputTokensOverride
		}

		toolsList := params.Registry.Enabled()
		streamParams := api.StreamParams{
			Messages:     msgsForQuery,
			SystemPrompt: params.SystemPrompt,
			Tools:        toolsList,
			Model:        currentModel,
			MaxTokens:    maxTokens,
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
				outCh <- e
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
			} else if isAPIErr && apiErr.IsOverloaded() &&
				params.FallbackModel != "" &&
				currentModel != params.FallbackModel {
				// ── 4a. Fallback model on overload ───────────────────────
				currentModel = params.FallbackModel
				msgsForQuery = stripThinkingBlocks(msgsForQuery)
				outCh <- types.NewSystemMessage(
					types.SystemSubtypeInformational,
					fmt.Sprintf("Model overloaded — switching to fallback: %s", params.FallbackModel),
					types.SystemLevelWarning,
				)
				continue
			} else {
				return types.Terminal{Reason: "api_error"}, err
			}
		}

		// ── 5. Error recovery: prompt-too-long ───────────────────────────────
		if withheld413 {
			if state.HasAttemptedReactiveCompact {
				return types.Terminal{Reason: "prompt_too_long"}, nil
			}
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

		// ── 6. max_output_tokens recovery (Phase 3a) ─────────────────────────
		if finalAssistant != nil && finalAssistant.Msg.StopReason == "max_tokens" {
			if state.MaxOutputTokensRecoveryCount < maxOutputTokensRecoveryLimit {
				// Inject a continuation nudge and retry.
				nudge := types.NewUserMessage(continuationNudge)
				outCh <- nudge
				newMsgs := make([]types.Message, len(messages))
				copy(newMsgs, messages)
				newMsgs = append(newMsgs, finalAssistant, nudge)
				reason := TransitionMaxTokensRecovery
				state = State{
					Messages:                     newMsgs,
					AutoCompactTracking:          state.AutoCompactTracking,
					MaxOutputTokensRecoveryCount: state.MaxOutputTokensRecoveryCount + 1,
					MaxOutputTokensOverride:      state.MaxOutputTokensOverride,
					TurnCount:                    turnCount + 1,
					Transition:                   &reason,
				}
				continue
			}
			// Exhausted retries — escalate to larger token budget.
			override := maxOutputTokensEscalated
			reason := TransitionMaxTokensEscalate
			newMsgs := make([]types.Message, len(messages))
			copy(newMsgs, messages)
			newMsgs = append(newMsgs, finalAssistant)
			state = State{
				Messages:                     newMsgs,
				AutoCompactTracking:          state.AutoCompactTracking,
				MaxOutputTokensRecoveryCount: 0,
				MaxOutputTokensOverride:      &override,
				TurnCount:                    turnCount + 1,
				Transition:                   &reason,
			}
			continue
		}

		// ── 7. Terminal check — no tool calls → turn complete ────────────────
		if !needsFollowUp {
			// Run stop hooks (Phase 3d) — any may request one more round-trip.
			for _, hook := range params.StopHooks {
				shouldRetry, hookErr := hook(ctx, finalAssistant)
				if hookErr != nil {
					return types.Terminal{Reason: "stop_hook_error"}, hookErr
				}
				if shouldRetry {
					reason := TransitionNextTurn
					state = state.withTransition(reason)
					state.TurnCount = turnCount + 1
					needsFollowUp = true // break out and re-enter the loop
					break
				}
			}
			if !needsFollowUp {
				return types.Terminal{Reason: "completed"}, nil
			}
			continue // retry triggered by stop hook
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
		turnCtx, turnCancel := context.WithCancel(ctx)
		defer turnCancel()

		toolCtx := tools.ToolContext{
			Ctx:                     turnCtx,
			AbortFunc:               turnCancel,
			WorkingDir:              params.WorkingDir,
			Messages:                messages,
			Registry:                params.Registry,
			Verbose:                 params.Verbose,
			GetAppState:             params.GetAppState,
			SetAppState:             params.SetAppState,
			ReadFileState:           params.ReadFileState,
			ContentReplacementState: params.ContentReplacementState,
			QueryTracking:           newQueryTracking(nil),
			AgentID:                 "",
			InProgressToolUseIDs:    make(map[string]bool),
			AgentRegistry:           params.AgentRegistry,
		}

		toolResultMsgs, sideMessages, err := tools.RunTools(
			toolUseBlocks,
			params.CanUseTool,
			toolCtx,
		)
		if err != nil {
			return types.Terminal{Reason: "tool_error"}, err
		}

		// Emit side messages to output (UI only — not added to history).
		for _, msg := range sideMessages {
			outCh <- msg
		}
		// Emit tool result messages.
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
			Messages:                     newMessages,
			AutoCompactTracking:          state.AutoCompactTracking,
			MaxOutputTokensRecoveryCount: state.MaxOutputTokensRecoveryCount,
			HasAttemptedReactiveCompact:  false, // reset for next turn
			MaxOutputTokensOverride:      state.MaxOutputTokensOverride,
			TurnCount:                    turnCount + 1,
			Transition:                   &reason,
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

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

// stripThinkingBlocks removes ThinkingBlock and RedactedThinkingBlock content
// from all AssistantMessages.  Required when switching between models because
// thinking blocks carry a model-bound cryptographic signature.
func stripThinkingBlocks(messages []types.Message) []types.Message {
	result := make([]types.Message, len(messages))
	for i, msg := range messages {
		am, ok := msg.(*types.AssistantMessage)
		if !ok {
			result[i] = msg
			continue
		}
		var filtered []types.ContentBlock
		changed := false
		for _, blk := range am.Msg.Content {
			switch blk.(type) {
			case *types.ThinkingBlock, *types.RedactedThinkingBlock:
				changed = true // skip this block
			default:
				filtered = append(filtered, blk)
			}
		}
		if !changed {
			result[i] = msg
			continue
		}
		newAM := *am
		newAPIMsg := am.Msg
		newAPIMsg.Content = filtered
		newAM.Msg = newAPIMsg
		result[i] = &newAM
	}
	return result
}
