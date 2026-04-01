package query

import (
	"context"

	"github.com/claude-code/go-claude-go/api"
	"github.com/claude-code/go-claude-go/compact"
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

	// MaxTurns is the upper bound on tool-execution rounds.  0 = unlimited.
	MaxTurns int

	// AutoCompact configures context compaction.
	AutoCompact compact.AutoCompactConfig
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
	outCh chan<- types.Message,
) (types.Terminal, error) {
	return queryLoop(ctx, params, outCh)
}
