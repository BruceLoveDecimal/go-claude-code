package hooks

import (
	"context"

	"github.com/claude-code/go-claude-go/types"
)

// ─────────────────────────────────────────────────────────────────────────────
// PreToolHookFn
// ─────────────────────────────────────────────────────────────────────────────

// PreToolHookFn is called immediately before a tool is executed (after the
// permission check passes).
//
//   - toolName is the tool's canonical name.
//   - input is the parsed input map (read-only; modifying it has no effect).
//
// Returning a non-nil error aborts the tool call and propagates the error as a
// tool_result error to the model.
type PreToolHookFn func(ctx context.Context, toolName string, input map[string]interface{}) error

// ─────────────────────────────────────────────────────────────────────────────
// PostToolHookFn
// ─────────────────────────────────────────────────────────────────────────────

// PostToolResult carries the tool output passed to a PostToolHookFn.
type PostToolResult struct {
	// Data is the tool's return value (identical to ToolResult.Data).
	Data interface{}
	// IsError is true when the tool reported an error.
	IsError bool
}

// PostToolHookFn is called immediately after a tool returns (even on error).
//
//   - toolName is the tool's canonical name.
//   - input is the original input map.
//   - result is the tool's output.
//
// Returning a non-nil error replaces the tool result with an error message.
type PostToolHookFn func(
	ctx context.Context,
	toolName string,
	input map[string]interface{},
	result PostToolResult,
) error

// ─────────────────────────────────────────────────────────────────────────────
// MessageHookFn
// ─────────────────────────────────────────────────────────────────────────────

// MessageHookFn is called for every SDKMessage emitted by the query loop
// (assistant messages, stream deltas, tool results, system markers, …).
//
// It is invoked synchronously on the output channel relay goroutine, so it
// must not block for long.  Returning a non-nil error terminates the turn with
// a hook_error terminal reason.
type MessageHookFn func(ctx context.Context, msg types.SDKMessage) error

// ─────────────────────────────────────────────────────────────────────────────
// HookSet — convenience aggregate
// ─────────────────────────────────────────────────────────────────────────────

// HookSet groups all hook slices so callers can pass a single value into
// QueryParams instead of three separate fields.
type HookSet struct {
	// StopHooks are called after each API response that has no tool_use blocks.
	StopHooks []StopHookFn
	// PreToolHooks are called before each tool execution (after permission check).
	PreToolHooks []PreToolHookFn
	// PostToolHooks are called after each tool execution.
	PostToolHooks []PostToolHookFn
	// MessageHooks are called for every message emitted on the output channel.
	MessageHooks []MessageHookFn
}

// IsEmpty returns true when no hooks are registered.
func (h HookSet) IsEmpty() bool {
	return len(h.StopHooks) == 0 &&
		len(h.PreToolHooks) == 0 &&
		len(h.PostToolHooks) == 0 &&
		len(h.MessageHooks) == 0
}
