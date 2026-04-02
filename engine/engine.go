// Package engine provides the QueryEngine, which is the stateful session
// manager for a single conversation.  It mirrors the TypeScript QueryEngine
// class in src/QueryEngine.ts.
package engine

import (
	"sync"
	"time"

	"github.com/claude-code/go-claude-go/api"
	"github.com/claude-code/go-claude-go/compact"
	"github.com/claude-code/go-claude-go/tools"
	"github.com/claude-code/go-claude-go/tools/permissions"
	"github.com/claude-code/go-claude-go/types"
)

// ─────────────────────────────────────────────────────────────────────────────
// Config
// ─────────────────────────────────────────────────────────────────────────────

// QueryEngineConfig holds every static parameter for a conversation session.
type QueryEngineConfig struct {
	// CWD is the working directory for shell-based tools.
	CWD string

	// Registry holds all tools available to the model.  If nil,
	// tools.DefaultRegistry() is used.
	Registry *tools.Registry

	// CanUseTool is called before each tool invocation.  Defaults to
	// tools.AlwaysAllow if nil.
	CanUseTool tools.CanUseToolFn

	// APIKey is the Anthropic API key (required).
	APIKey string

	// APIBaseURL allows overriding the Anthropic API base URL (optional).
	APIBaseURL string

	// Model is the Anthropic model ID (e.g. "claude-sonnet-4-6").
	Model string

	// FallbackModel is used if the primary model is overloaded (Phase 3).
	FallbackModel string

	// MaxTurns limits the number of tool-execution rounds per submitMessage
	// call (0 = unlimited).
	MaxTurns int

	// SystemPrompt is prepended to every API request as the system message.
	SystemPrompt string

	// AppendSystemPrompt is appended to SystemPrompt when building the
	// effective system prompt.
	AppendSystemPrompt string

	// InitialMessages seeds the conversation history.
	InitialMessages []types.Message

	// AutoCompact configures the context compaction pipeline.  If APIKey is
	// empty in AutoCompact, compaction is disabled.
	AutoCompact compact.AutoCompactConfig

	// InitialAppState seeds the session-level AppState.  If zero-valued,
	// DefaultAppState() is used.
	InitialAppState tools.AppState

	// Verbose enables additional diagnostic output.
	Verbose bool
}

// ─────────────────────────────────────────────────────────────────────────────
// QueryEngine
// ─────────────────────────────────────────────────────────────────────────────

// QueryEngine manages a single conversation session.  One instance should be
// created per conversation; multiple goroutines must not call SubmitMessage
// concurrently.
type QueryEngine struct {
	config          QueryEngineConfig
	mutableMessages []types.Message
	totalUsage      types.Usage
	apiClient       *api.Client
	mu              sync.Mutex

	// ── Session-level state (Phase 1) ────────────────────────────────────

	// appState is the mutable session-level application state.  Protected by
	// appStateMu (separate from mu to avoid blocking message history reads
	// while app-state is being updated by tools).
	appState   tools.AppState
	appStateMu sync.RWMutex

	// readFileState caches file metadata for permission checking and change
	// detection.
	readFileState *tools.ReadFileState

	// contentReplState tracks tool-result truncations for the budget compactor
	// (populated in Phase 3).
	contentReplState *tools.ContentReplacementState

	// ── Permission tracking (Phase 2) ────────────────────────────────────

	// permissionDenials records every tool call that was denied this session.
	permissionDenials []tools.PermissionDenial
	permDenialsMu     sync.Mutex
}

// NewQueryEngine creates a QueryEngine from the given config.  Panics if
// APIKey is empty.
func NewQueryEngine(cfg QueryEngineConfig) *QueryEngine {
	if cfg.APIKey == "" {
		panic("QueryEngineConfig.APIKey must not be empty")
	}
	if cfg.Registry == nil {
		cfg.Registry = tools.DefaultRegistry()
	}
	if cfg.CanUseTool == nil {
		cfg.CanUseTool = defaultCanUseTool
	}
	if cfg.Model == "" {
		cfg.Model = "claude-sonnet-4-6"
	}
	// Propagate API key to AutoCompact config if not set independently.
	if cfg.AutoCompact.APIKey == "" {
		cfg.AutoCompact.APIKey = cfg.APIKey
	}
	if cfg.AutoCompact.Model == "" {
		cfg.AutoCompact.Model = cfg.Model
	}

	// Seed conversation history.
	msgs := make([]types.Message, 0)
	if cfg.InitialMessages != nil {
		msgs = append(msgs, cfg.InitialMessages...)
	}

	// Seed AppState — use provided value or safe defaults.
	appState := cfg.InitialAppState
	if appState.PermissionContext.Mode == "" {
		appState = tools.DefaultAppState()
	}

	return &QueryEngine{
		config:           cfg,
		mutableMessages:  msgs,
		apiClient:        api.NewClient(cfg.APIKey, cfg.APIBaseURL),
		appState:         appState,
		readFileState:    tools.NewReadFileState(),
		contentReplState: tools.NewContentReplacementState(),
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Public accessors
// ─────────────────────────────────────────────────────────────────────────────

// Messages returns a snapshot of the current conversation history.
func (qe *QueryEngine) Messages() []types.Message {
	qe.mu.Lock()
	defer qe.mu.Unlock()
	out := make([]types.Message, len(qe.mutableMessages))
	copy(out, qe.mutableMessages)
	return out
}

// TotalUsage returns the accumulated token usage across all turns.
func (qe *QueryEngine) TotalUsage() types.Usage {
	qe.mu.Lock()
	defer qe.mu.Unlock()
	return qe.totalUsage
}

// GetAppState returns a snapshot of the current session-level AppState.
// Safe to call from multiple goroutines.
func (qe *QueryEngine) GetAppState() tools.AppState {
	qe.appStateMu.RLock()
	defer qe.appStateMu.RUnlock()
	return qe.appState
}

// SetAppState applies f to the current AppState atomically.
// f receives the current state and must return the new state without
// retaining any reference to the argument.
func (qe *QueryEngine) SetAppState(f func(tools.AppState) tools.AppState) {
	qe.appStateMu.Lock()
	defer qe.appStateMu.Unlock()
	qe.appState = f(qe.appState)
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ─────────────────────────────────────────────────────────────────────────────

// buildSystemPrompt assembles the effective system prompt from config.
func (qe *QueryEngine) buildSystemPrompt() string {
	sp := qe.config.SystemPrompt
	if qe.config.AppendSystemPrompt != "" {
		if sp != "" {
			sp += "\n\n"
		}
		sp += qe.config.AppendSystemPrompt
	}
	return sp
}

// PermissionDenials returns a snapshot of all tool permission denials recorded
// during this session.
func (qe *QueryEngine) PermissionDenials() []tools.PermissionDenial {
	qe.permDenialsMu.Lock()
	defer qe.permDenialsMu.Unlock()
	out := make([]tools.PermissionDenial, len(qe.permissionDenials))
	copy(out, qe.permissionDenials)
	return out
}

// recordDenial appends a PermissionDenial to the session-level audit log.
func (qe *QueryEngine) recordDenial(toolName, reason string) {
	qe.permDenialsMu.Lock()
	defer qe.permDenialsMu.Unlock()
	qe.permissionDenials = append(qe.permissionDenials, tools.PermissionDenial{
		ToolName:  toolName,
		Reason:    reason,
		Timestamp: time.Now().UTC(),
	})
}

// defaultCanUseTool is the built-in CanUseToolFn used when the caller does not
// supply one.  It runs the five-step permission decision chain and, when the
// result is PermAsk, presents an interactive CLI prompt.
func defaultCanUseTool(
	toolName string,
	input map[string]interface{},
	ctx tools.ToolContext,
) (tools.PermissionResult, error) {
	permCtx := ctx.GetAppState().PermissionContext
	result, err := permissions.HasPermissionsToUseTool(toolName, input, ctx, permCtx)
	if err != nil {
		return result, err
	}
	if result.Behavior == tools.PermAsk {
		return permissions.PromptForPermission(toolName, input, ctx)
	}
	return result, nil
}

// appendToHistory safely appends messages to the mutable history and
// accumulates usage statistics.
func (qe *QueryEngine) appendToHistory(msgs ...types.Message) {
	qe.mu.Lock()
	defer qe.mu.Unlock()
	for _, msg := range msgs {
		// Skip re-appending messages that are already in the history (e.g.
		// the initial user message we pushed before starting the loop).
		if am, ok := msg.(*types.AssistantMessage); ok {
			qe.totalUsage = qe.totalUsage.Add(am.Msg.Usage)
		}
		qe.mutableMessages = append(qe.mutableMessages, msg)
	}
}
