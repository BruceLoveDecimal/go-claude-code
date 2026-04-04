package engine

import (
	"context"

	"github.com/claude-code/go-claude-go/query"
	"github.com/claude-code/go-claude-go/session"
	"github.com/claude-code/go-claude-go/tools"
	"github.com/claude-code/go-claude-go/types"
)

// SubmitMessage starts a new agent turn for the given prompt.  It yields
// messages on msgCh (closed when the turn finishes) and any fatal error on
// errCh.
//
// Message types on msgCh (in order):
//  1. *types.UserMessage          — the user's prompt
//  2. *types.SystemMessage        — stream_request_start marker per API call
//  3. *types.StreamDeltaEvent     — incremental text deltas (optional)
//  4. *types.AssistantMessage     — complete model response
//  5. *types.UserMessage          — tool_result messages (zero or more)
//  6. (repeat 2–5 for each tool-execution round)
//
// The method is safe to call sequentially (not concurrently).  The internal
// mutableMessages slice is updated atomically after each round.
func (qe *QueryEngine) SubmitMessage(
	ctx context.Context,
	prompt string,
) (<-chan types.SDKMessage, <-chan error) {
	msgCh := make(chan types.SDKMessage, 64)
	errCh := make(chan error, 1)

	go func() {
		defer close(msgCh)
		defer close(errCh)

		// Build the user message and push it to the history first so that
		// the query loop sees the full conversation.
		userMsg := types.NewUserMessage(prompt)

		qe.mu.Lock()
		qe.mutableMessages = append(qe.mutableMessages, userMsg)
		// Take a snapshot of the full history for this turn.
		snapshot := make([]types.Message, len(qe.mutableMessages))
		copy(snapshot, qe.mutableMessages)
		qe.mu.Unlock()

		// Yield the user message immediately so callers can echo it.
		msgCh <- userMsg

		// Set up the outgoing channel for the query loop.
		loopOutCh := make(chan types.SDKMessage, 64)
		loopErrCh := make(chan error, 1)

		go func() {
			defer close(loopOutCh)
			defer close(loopErrCh)

			// Wrap CanUseTool to record permission denials at the session level.
			wrappedCanUse := func(
				toolName string,
				input map[string]interface{},
				ctx tools.ToolContext,
			) (tools.PermissionResult, error) {
				result, err := qe.config.CanUseTool(toolName, input, ctx)
				if err == nil && result.Behavior == tools.PermBlock {
					reason := result.Reason
					if reason == "" {
						reason = "permission denied"
					}
					qe.recordDenial(toolName, reason)
				}
				return result, err
			}

			params := query.QueryParams{
				Messages:                snapshot,
				SystemPrompt:            qe.buildSystemPrompt(),
				APIClient:               qe.apiClient,
				Registry:                qe.config.Registry,
				CanUseTool:              wrappedCanUse,
				WorkingDir:              qe.config.CWD,
				Model:                   qe.config.Model,
				FallbackModel:           qe.config.FallbackModel,
				MaxTurns:                qe.config.MaxTurns,
				Verbose:                 qe.config.Verbose,
				AutoCompact:             qe.config.AutoCompact,
				// Phase 1: wire session-level state into the query loop.
				GetAppState:             qe.GetAppState,
				SetAppState:             qe.SetAppState,
				ReadFileState:           qe.readFileState,
				ContentReplacementState: qe.contentReplState,
				// Phase 7: subagent coordination.
				AgentRegistry:           qe.agentRegistry,
			}

			terminal, err := query.Query(ctx, params, loopOutCh)
			if err != nil {
				loopErrCh <- err
				return
			}
			// Emit a final informational system message with the terminal reason.
			loopOutCh <- types.NewSystemMessage(
				types.SystemSubtypeInformational,
				"turn_complete reason="+terminal.Reason,
				types.SystemLevelInfo,
			)
		}()

		// Relay every message from the loop to the caller and accumulate
		// assistant messages into history.
		var newMessages []types.Message
		for msg := range loopOutCh {
			// Don't re-add the user message (already in history).
			switch m := msg.(type) {
			case *types.AssistantMessage:
				newMessages = append(newMessages, m)
				qe.mu.Lock()
				qe.totalUsage = qe.totalUsage.Add(m.Msg.Usage)
				qe.mu.Unlock()
			case *types.UserMessage:
				if msg != userMsg { // only tool results, not the original prompt
					newMessages = append(newMessages, m)
				}
			case *types.SystemMessage:
				// System messages (compact boundaries, etc.) go into history.
				if m.Subtype == types.SystemSubtypeCompactBoundary {
					newMessages = append(newMessages, m)
				}
			}
			msgCh <- msg
		}

		if err := <-loopErrCh; err != nil {
			errCh <- err
			return
		}

		// Persist new messages to the mutable history.
		if len(newMessages) > 0 {
			qe.mu.Lock()
			qe.mutableMessages = append(qe.mutableMessages, newMessages...)
			qe.mu.Unlock()
		}

		// Append this turn to the JSONL session file when persistence is enabled.
		if qe.config.SessionPersist && qe.sessionID != "" {
			// Persist the user prompt + all new messages from this turn.
			toSave := make([]types.Message, 0, 1+len(newMessages))
			toSave = append(toSave, userMsg)
			toSave = append(toSave, newMessages...)
			_ = session.AppendMessages(qe.sessionID, qe.sessionMeta, toSave)
		}
	}()

	return msgCh, errCh
}
