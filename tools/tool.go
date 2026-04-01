// Package tools defines the Tool interface and supporting types used by the
// agent loop.  The design mirrors src/Tool.ts in the Claude Code TypeScript
// source.
package tools

import (
	"context"

	"github.com/claude-code/go-claude-go/types"
)

// ─────────────────────────────────────────────────────────────────────────────
// Permission types
// ─────────────────────────────────────────────────────────────────────────────

// PermissionBehavior is the outcome of a permission check.
type PermissionBehavior string

const (
	PermAllow PermissionBehavior = "allow"
	PermBlock PermissionBehavior = "block"
	PermAsk   PermissionBehavior = "ask"
)

// PermissionResult is returned by Tool.CheckPermissions and CanUseToolFn.
type PermissionResult struct {
	Behavior     PermissionBehavior
	Reason       string
	UpdatedInput map[string]interface{}
}

// ─────────────────────────────────────────────────────────────────────────────
// Tool result
// ─────────────────────────────────────────────────────────────────────────────

// ToolResult wraps the output of a tool invocation.
type ToolResult struct {
	// Data is the primary output (will be JSON-serialised into the
	// tool_result content block sent to the model).
	Data interface{}
	// NewMessages allows a tool to inject side-channel messages into the
	// conversation (e.g. an edit confirmation, a progress note).
	NewMessages []types.Message
	// IsError marks the result as an error so the model can react to it.
	IsError bool
}

// ─────────────────────────────────────────────────────────────────────────────
// Tool context
// ─────────────────────────────────────────────────────────────────────────────

// ToolContext carries per-invocation state that tools may need.
type ToolContext struct {
	// Ctx is the request context (carries deadline, cancel, and values).
	Ctx context.Context
	// WorkingDir is the current working directory for shell-based tools.
	WorkingDir string
	// Messages is a snapshot of the conversation so far (read-only).
	Messages []types.Message
	// Registry gives tools access to other registered tools if needed.
	Registry *Registry
	// Verbose enables additional diagnostic output.
	Verbose bool
}

// ─────────────────────────────────────────────────────────────────────────────
// Permission callback
// ─────────────────────────────────────────────────────────────────────────────

// CanUseToolFn is called before each tool invocation to check whether the
// current permission mode allows it.  Returning PermAllow proceeds; PermBlock
// skips execution; PermAsk is treated as block in non-interactive sessions.
type CanUseToolFn func(toolName string, input map[string]interface{}, ctx ToolContext) (PermissionResult, error)

// AlwaysAllow is a CanUseToolFn that permits every tool call unconditionally.
func AlwaysAllow(toolName string, input map[string]interface{}, ctx ToolContext) (PermissionResult, error) {
	return PermissionResult{Behavior: PermAllow, UpdatedInput: input}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Tool interface
// ─────────────────────────────────────────────────────────────────────────────

// Tool is the interface that every tool must implement.  The design mirrors the
// TypeScript Tool type from src/Tool.ts.
type Tool interface {
	// Name returns the canonical tool name used in API calls (e.g. "Bash").
	Name() string

	// Description returns a short human-readable description of what the
	// tool does.  Sent to the model in the tools array.
	Description() string

	// InputSchema returns a JSON Schema (as a map) describing the tool's
	// expected input.  Used to build the tools array for the Anthropic API.
	InputSchema() map[string]interface{}

	// IsConcurrencySafe returns true if this invocation can safely run in
	// parallel with other concurrent-safe invocations.  Typically true for
	// read-only operations (Read, Glob, Grep) and false for mutations (Bash
	// writes, Edit, Write).
	IsConcurrencySafe(input map[string]interface{}) bool

	// IsReadOnly returns true if the input does not mutate any state.
	IsReadOnly(input map[string]interface{}) bool

	// IsEnabled reports whether this tool should be advertised to the model.
	IsEnabled() bool

	// CheckPermissions validates the input against the current permission
	// policy.  May return PermAllow (proceed), PermBlock (reject with
	// reason), or PermAsk (requires interactive confirmation).
	CheckPermissions(input map[string]interface{}, ctx ToolContext) (PermissionResult, error)

	// Call executes the tool.  progress receives incremental updates
	// (may be nil for tools that don't stream progress).
	Call(
		input    map[string]interface{},
		ctx      ToolContext,
		canUse   CanUseToolFn,
		progress chan<- interface{},
	) (ToolResult, error)

	// MaxResultSizeChars is the upper bound on the result string length;
	// larger results should be persisted to disk and referenced by path.
	MaxResultSizeChars() int
}
