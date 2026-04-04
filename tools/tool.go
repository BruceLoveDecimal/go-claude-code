// Package tools defines the Tool interface and supporting types used by the
// agent loop.  The design mirrors src/Tool.ts in the Claude Code TypeScript
// source.
package tools

import (
	"context"
	"sync"
	"time"

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

// PermissionMode controls how tool permission decisions are made for a session.
type PermissionMode string

const (
	// PermissionModeDefault requires explicit approval for mutating tools.
	PermissionModeDefault PermissionMode = "default"
	// PermissionModeAcceptEdits auto-approves file edits without prompting.
	PermissionModeAcceptEdits PermissionMode = "acceptEdits"
	// PermissionModeBypassPermissions skips all permission checks (dangerous).
	PermissionModeBypassPermissions PermissionMode = "bypassPermissions"
	// PermissionModeDontAsk auto-approves read-only tools; asks for mutating tools.
	PermissionModeDontAsk PermissionMode = "dontAsk"
)

// ToolPermissionRule matches a tool invocation by name and/or path glob.
// An empty field matches anything.
type ToolPermissionRule struct {
	// ToolName is the exact tool name to match (empty = any tool).
	ToolName string
	// PathGlob is a glob matched against the primary path argument of the
	// invocation (empty = any path / no path).
	PathGlob string
}

// ToolPermissionContext is the session-level permission configuration.
// It lives inside AppState and is updated by interactive user decisions.
type ToolPermissionContext struct {
	// Mode is the active permission mode.
	Mode PermissionMode
	// AlwaysAllowRules unconditionally allow matching tool invocations.
	AlwaysAllowRules []ToolPermissionRule
	// AlwaysDenyRules unconditionally deny matching tool invocations.
	AlwaysDenyRules []ToolPermissionRule
	// AlwaysAskRules force an interactive prompt even for otherwise-allowed tools.
	AlwaysAskRules []ToolPermissionRule
	// AdditionalWorkingDirectories grants path-based permissions beyond CWD.
	AdditionalWorkingDirectories []string
}

// PermissionDenial records a tool permission denial for audit/history purposes.
type PermissionDenial struct {
	ToolName  string
	Reason    string
	Timestamp time.Time
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
// Session-level state types
// ─────────────────────────────────────────────────────────────────────────────

// TodoStatus represents the lifecycle state of a todo item.
type TodoStatus string

const (
	TodoStatusPending    TodoStatus = "pending"
	TodoStatusInProgress TodoStatus = "in_progress"
	TodoStatusCompleted  TodoStatus = "completed"
)

// TodoPriority is the relative importance of a todo item.
type TodoPriority string

const (
	TodoPriorityHigh   TodoPriority = "high"
	TodoPriorityMedium TodoPriority = "medium"
	TodoPriorityLow    TodoPriority = "low"
)

// TodoItem represents a single task tracked by the Todo tools.
type TodoItem struct {
	ID       string       `json:"id"`
	Content  string       `json:"content"`
	Status   TodoStatus   `json:"status"`
	Priority TodoPriority `json:"priority"`
}

// AppState is the session-level mutable application state shared across all
// tool invocations within a conversation.  It mirrors AppState in
// src/state/AppState.ts.
type AppState struct {
	// PermissionContext is the active permission configuration for this session.
	PermissionContext ToolPermissionContext
	// FastMode enables faster but potentially less thorough responses.
	FastMode bool
	// Todos holds the session-scoped task list managed by TodoRead/TodoWrite.
	Todos []TodoItem
}

// DefaultAppState returns an AppState with safe defaults.
func DefaultAppState() AppState {
	return AppState{
		PermissionContext: ToolPermissionContext{Mode: PermissionModeDefault},
	}
}

// FileState caches metadata about a file that has been read this session.
// Mirrors src/utils/fileStateCache.ts.
type FileState struct {
	ContentHash string
	ReadAt      time.Time
}

// ReadFileState is a session-scoped concurrent-safe cache of file metadata.
// Tools use it to detect whether a file has changed since it was last read
// and to enforce permissions on edits.
type ReadFileState struct {
	mu    sync.RWMutex
	cache map[string]FileState
}

// NewReadFileState constructs an empty ReadFileState.
func NewReadFileState() *ReadFileState {
	return &ReadFileState{cache: make(map[string]FileState)}
}

// Set records that a file was read with the given content hash.
func (r *ReadFileState) Set(path, hash string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache[path] = FileState{ContentHash: hash, ReadAt: time.Now()}
}

// Get returns the cached state for a path, or (zero, false) if not found.
func (r *ReadFileState) Get(path string) (FileState, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.cache[path]
	return s, ok
}

// ContentReplacementRecord stores metadata about a single truncated tool result.
type ContentReplacementRecord struct {
	ToolUseID    string
	OriginalSize int
	Replacement  string
}

// ContentReplacementState tracks which tool results have been truncated to
// stay within the per-context tool-result budget.  Used in Phase 3.
type ContentReplacementState struct {
	mu      sync.Mutex
	records []ContentReplacementRecord
}

// NewContentReplacementState constructs an empty ContentReplacementState.
func NewContentReplacementState() *ContentReplacementState {
	return &ContentReplacementState{}
}

// Add records a content replacement.
func (c *ContentReplacementState) Add(rec ContentReplacementRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, rec)
}

// All returns a snapshot of all replacement records.
func (c *ContentReplacementState) All() []ContentReplacementRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ContentReplacementRecord, len(c.records))
	copy(out, c.records)
	return out
}

// QueryChainTracking records the chain ID and nesting depth of an agent query.
// Mirrors QueryChainTracking in src/Tool.ts.
type QueryChainTracking struct {
	ChainID string
	Depth   int
}

// ─────────────────────────────────────────────────────────────────────────────
// Tool context
// ─────────────────────────────────────────────────────────────────────────────

// ToolContext carries per-invocation state that tools may need.  It mirrors
// ToolUseContext in src/Tool.ts.
type ToolContext struct {
	// Ctx is the request context (carries deadline, cancel, and values).
	Ctx context.Context
	// AbortFunc cancels the current agent turn from inside a tool.  May be nil
	// if no per-turn cancellation is configured.
	AbortFunc context.CancelFunc
	// WorkingDir is the current working directory for shell-based tools.
	WorkingDir string
	// Messages is a snapshot of the conversation so far (read-only).
	Messages []types.Message
	// Registry gives tools access to other registered tools if needed.
	Registry *Registry
	// Verbose enables additional diagnostic output.
	Verbose bool

	// ── Session-level state ───────────────────────────────────────────────

	// GetAppState returns a read-only snapshot of the current session AppState.
	// Never nil after Phase 1 initialisation.
	GetAppState func() AppState
	// SetAppState applies a pure transformation to the session AppState.
	// The provided function receives the current state and returns the new one.
	SetAppState func(func(AppState) AppState)

	// ReadFileState is the session-scoped file metadata cache.
	ReadFileState *ReadFileState

	// ContentReplacementState tracks tool-result truncations for budget
	// management (populated in Phase 3).
	ContentReplacementState *ContentReplacementState

	// ── Per-turn tracking ────────────────────────────────────────────────

	// QueryTracking records the chain ID and nesting depth for this query.
	// Nil on the outermost query; incremented by Agent tool calls (Phase 7).
	QueryTracking *QueryChainTracking

	// AgentID identifies the subagent executing this tool.  Empty string means
	// the main thread.  Populated by the Agent tool in Phase 7.
	AgentID string

	// InProgressToolUseIDs tracks which tool_use IDs are currently executing.
	// Used for progress indicators and abort coordination.
	InProgressToolUseIDs map[string]bool

	// ResponseLength accumulates the total character count of streamed text
	// deltas for the current turn.  Used for UI progress display.
	ResponseLength int

	// AgentRegistry is the session-scoped registry of running subagents.
	// Populated by engine.go; allows Agent and SendMessage tools to coordinate.
	// May be nil in minimal / test configurations.
	AgentRegistry *AgentRegistry
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
