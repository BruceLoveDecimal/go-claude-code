package query

import (
	"context"

	"github.com/claude-code/go-claude-go/api"
	"github.com/claude-code/go-claude-go/compact"
	"github.com/claude-code/go-claude-go/hooks"
	"github.com/claude-code/go-claude-go/tools"
	"github.com/claude-code/go-claude-go/types"
)

// ─────────────────────────────────────────────────────────────────────────────
// QueryParams
// ─────────────────────────────────────────────────────────────────────────────

// QueryParams holds every configuration value that is constant across all
// iterations of the agent loop for a single user turn.
type QueryParams struct {
	// Messages is the conversation history going into this turn (includes
	// the freshly appended user message).
	Messages []types.Message

	// SystemPrompt is injected as the system field in every API request.
	SystemPrompt string

	// APIClient is the Anthropic HTTP client used to stream completions.
	APIClient *api.Client

	// Registry holds all enabled tools.  Must not be nil.
	Registry *tools.Registry

	// CanUseTool is called before each tool invocation to check permissions.
	CanUseTool tools.CanUseToolFn

	// WorkingDir is the filesystem root for shell-based tools.
	WorkingDir string

	// Model is the Anthropic model ID (e.g. "claude-sonnet-4-6").
	Model string

	// FallbackModel is used if the primary model returns an overloaded error.
	// Empty string disables fallback (Phase 3).
	FallbackModel string

	// MaxTurns is the upper bound on tool-execution rounds.  0 = unlimited.
	MaxTurns int

	// Verbose enables additional diagnostic output forwarded to ToolContext.
	Verbose bool

	// AutoCompact configures context compaction.
	AutoCompact compact.AutoCompactConfig

	// ── Session-level state (Phase 1) ────────────────────────────────────

	// GetAppState provides read access to the session-level AppState.
	// If nil, a no-op returning DefaultAppState() is used.
	GetAppState func() tools.AppState

	// SetAppState applies a transformation to the session-level AppState.
	// If nil, state updates are silently dropped.
	SetAppState func(func(tools.AppState) tools.AppState)

	// ReadFileState is the session-scoped file metadata cache.
	// If nil, tools that need it will skip caching.
	ReadFileState *tools.ReadFileState

	// ContentReplacementState tracks tool-result truncations.
	// If nil, budget compaction is disabled (Phase 3).
	ContentReplacementState *tools.ContentReplacementState

	// ── Stop hooks (Phase 3) ─────────────────────────────────────────────

	// StopHooks are invoked after each API response that contains no tool_use
	// blocks.  If any hook returns ShouldRetry=true, the loop performs one
	// additional API round-trip.
	StopHooks []hooks.StopHookFn
}

// ─────────────────────────────────────────────────────────────────────────────
// Query — public entry point
// ─────────────────────────────────────────────────────────────────────────────

// Query drives the full agentic loop for a single user turn.  It sends
// messages and stream events to outCh as they are produced and returns the
// Terminal reason when the loop finishes.
//
// The caller is responsible for draining outCh; it is closed before Query
// returns.
func Query(
	ctx context.Context,
	params QueryParams,
	outCh chan<- types.SDKMessage,
) (types.Terminal, error) {
	// Fill in nil callbacks with safe no-op defaults so the loop and tools
	// never need to nil-check these.
	if params.GetAppState == nil {
		params.GetAppState = func() tools.AppState { return tools.DefaultAppState() }
	}
	if params.SetAppState == nil {
		params.SetAppState = func(func(tools.AppState) tools.AppState) {}
	}
	if params.ReadFileState == nil {
		params.ReadFileState = tools.NewReadFileState()
	}
	if params.ContentReplacementState == nil {
		params.ContentReplacementState = tools.NewContentReplacementState()
	}
	return queryLoop(ctx, params, outCh)
}
