package engine

import (
	"context"
	"fmt"

	"github.com/claude-code/go-claude-go/api"
	"github.com/claude-code/go-claude-go/query"
	"github.com/claude-code/go-claude-go/tools"
	"github.com/claude-code/go-claude-go/types"
)

// agentRunner returns the AgentRunner closure used by AgentTool.
// It creates a child QueryEngine that shares the parent's API key and model
// but runs an independent conversation history with an optional tool subset.
func (qe *QueryEngine) agentRunner() tools.AgentRunner {
	return func(
		ctx context.Context,
		agentID string,
		prompt string,
		systemPrompt string,
		toolNames []string,
		parentCtx tools.ToolContext,
		outCh chan<- types.SDKMessage,
	) (string, error) {
		// Build a registry for the subagent.
		registry := buildSubRegistry(qe.config.Registry, toolNames)

		// Use the parent's system prompt unless the caller overrides it.
		sp := qe.buildSystemPrompt()
		if systemPrompt != "" {
			sp = systemPrompt
		}

		// Inherit session-level state from the parent engine.
		params := query.QueryParams{
			Messages:                []types.Message{types.NewUserMessage(prompt)},
			SystemPrompt:            sp,
			APIClient:               api.NewClient(qe.config.APIKey, qe.config.APIBaseURL),
			Registry:                registry,
			CanUseTool:              qe.config.CanUseTool,
			WorkingDir:              qe.config.CWD,
			Model:                   qe.config.Model,
			FallbackModel:           qe.config.FallbackModel,
			MaxTurns:                qe.config.MaxTurns,
			Verbose:                 qe.config.Verbose,
			AutoCompact:             qe.config.AutoCompact,
			RetryConfig:             qe.config.RetryConfig,
			Hooks:                   qe.config.Hooks,
			UserInputFn:             qe.config.UserInputFn,
			GetAppState:             qe.GetAppState,
			SetAppState:             qe.SetAppState,
			ReadFileState:           qe.readFileState,
			ContentReplacementState: qe.contentReplState,
			// API feature parameters inherited from parent.
			Thinking:                qe.config.Thinking,
			Betas:                   qe.config.Betas,
			Metadata:                qe.config.Metadata,
			EnableCaching:           qe.config.EnableCaching,
		}

		terminal, err := query.Query(ctx, params, outCh)
		if err != nil {
			return "agent_error", fmt.Errorf("subagent %s: %w", agentID, err)
		}
		return terminal.Reason, nil
	}
}

// buildSubRegistry returns a registry containing only the named tools.
// If toolNames is empty, the full parent registry is returned unchanged.
func buildSubRegistry(parent *tools.Registry, toolNames []string) *tools.Registry {
	if len(toolNames) == 0 {
		return parent
	}
	sub := tools.NewRegistry()
	for _, name := range toolNames {
		if t, ok := parent.Get(name); ok {
			sub.Register(t)
		}
	}
	return sub
}
