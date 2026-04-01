// Package engine provides the QueryEngine, which is the stateful session
// manager for a single conversation.  It mirrors the TypeScript QueryEngine
// class in src/QueryEngine.ts.
package engine

import (
	"sync"

	"github.com/claude-code/go-claude-go/api"
	"github.com/claude-code/go-claude-go/compact"
	"github.com/claude-code/go-claude-go/tools"
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
		cfg.CanUseTool = tools.AlwaysAllow
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

	msgs := make([]types.Message, 0)
	if cfg.InitialMessages != nil {
		msgs = append(msgs, cfg.InitialMessages...)
	}

	return &QueryEngine{
		config:          cfg,
		mutableMessages: msgs,
		apiClient:       api.NewClient(cfg.APIKey, cfg.APIBaseURL),
	}
}

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
