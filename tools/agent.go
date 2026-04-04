package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/claude-code/go-claude-go/types"
	"github.com/google/uuid"
)

// AgentRunner is a function that executes a full agentic query loop in a child
// context.  The engine package provides a concrete implementation and injects
// it via QueryEngineConfig.  The function must close outCh when done.
//
// Parameters:
//   - ctx: cancellation context.
//   - prompt: the user prompt that starts the subagent turn.
//   - systemPrompt: optional system prompt override.
//   - toolNames: if non-empty, restricts the subagent to only these tools.
//   - parentCtx: the ToolContext of the parent tool invocation.
//   - outCh: receives messages produced by the subagent loop.
//
// Returns the terminal reason string and any fatal error.
type AgentRunner func(
	ctx context.Context,
	agentID string,
	prompt string,
	systemPrompt string,
	toolNames []string,
	parentCtx ToolContext,
	outCh chan<- types.SDKMessage,
) (string, error)

// ─────────────────────────────────────────────────────────────────────────────
// AgentTool
// ─────────────────────────────────────────────────────────────────────────────

// AgentTool launches a subagent with an independent query loop.  It mirrors
// the TypeScript Agent tool from src/tools/agent.ts.
//
// The agent's outputs are collected and returned as a single text block.
// The parent engine does not see individual messages from the subagent.
type AgentTool struct {
	runner AgentRunner
}

// NewAgentTool creates an AgentTool backed by the provided runner.
func NewAgentTool(runner AgentRunner) *AgentTool {
	return &AgentTool{runner: runner}
}

func (t *AgentTool) Name() string { return "Agent" }
func (t *AgentTool) Description() string {
	return "Launch a subagent to work on a complex, multi-step task autonomously. " +
		"The subagent has access to the same tools as the parent and runs a full " +
		"agentic loop until the task is complete or a terminal condition is met."
}
func (t *AgentTool) IsEnabled() bool           { return t.runner != nil }
func (t *AgentTool) IsReadOnly(map[string]interface{}) bool  { return false }
func (t *AgentTool) IsConcurrencySafe(map[string]interface{}) bool { return false }
func (t *AgentTool) MaxResultSizeChars() int   { return 200_000 }

func (t *AgentTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "The task description / initial prompt for the subagent.",
			},
			"system_prompt": map[string]interface{}{
				"type":        "string",
				"description": "Optional system prompt override for the subagent.",
			},
			"tools": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Optional list of tool names to make available to the subagent. Defaults to all tools.",
			},
		},
		"required": []string{"prompt"},
	}
}

func (t *AgentTool) CheckPermissions(input map[string]interface{}, ctx ToolContext) (PermissionResult, error) {
	return PermissionResult{Behavior: PermAsk, UpdatedInput: input}, nil
}

func (t *AgentTool) Call(
	input map[string]interface{},
	ctx ToolContext,
	_ CanUseToolFn,
	_ chan<- interface{},
) (ToolResult, error) {
	prompt, _ := input["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		return ToolResult{IsError: true, Data: "Agent: prompt must not be empty"}, nil
	}

	systemPrompt, _ := input["system_prompt"].(string)

	var toolNames []string
	if rawTools, ok := input["tools"]; ok {
		switch v := rawTools.(type) {
		case []interface{}:
			for _, item := range v {
				if s, ok := item.(string); ok {
					toolNames = append(toolNames, s)
				}
			}
		case []string:
			toolNames = v
		}
	}

	agentID := uuid.New().String()

	// Register a handle in the session registry so SendMessage can find it.
	var handle *AgentHandle
	if ctx.AgentRegistry != nil {
		promptCh := make(chan string, 8)
		handle = &AgentHandle{ID: agentID, promptCh: promptCh}
		if err := ctx.AgentRegistry.Register(handle); err != nil {
			return ToolResult{IsError: true, Data: fmt.Sprintf("Agent: registry error: %v", err)}, nil
		}
		defer ctx.AgentRegistry.Unregister(agentID)
	}

	// Collect subagent messages into a result buffer.
	outCh := make(chan types.SDKMessage, 128)
	var runErr error
	var terminal string

	done := make(chan struct{})
	go func() {
		defer close(done)
		terminal, runErr = t.runner(ctx.Ctx, agentID, prompt, systemPrompt, toolNames, ctx, outCh)
		close(outCh)
	}()

	var sb strings.Builder
	for msg := range outCh {
		summariseMessage(msg, &sb)
	}
	<-done

	if runErr != nil {
		return ToolResult{IsError: true, Data: fmt.Sprintf("Agent error: %v", runErr)}, nil
	}

	result := strings.TrimSpace(sb.String())
	if result == "" {
		result = fmt.Sprintf("Subagent completed (terminal=%s).", terminal)
	}
	return ToolResult{Data: result}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// summariseMessage appends a human-readable summary of a subagent message to sb.
func summariseMessage(msg types.SDKMessage, sb *strings.Builder) {
	switch m := msg.(type) {
	case *types.AssistantMessage:
		if m.IsAPIErrorMessage {
			return
		}
		for _, blk := range m.Msg.Content {
			switch b := blk.(type) {
			case *types.TextBlock:
				if b.Text != "" {
					sb.WriteString(b.Text)
					sb.WriteByte('\n')
				}
			case *types.ToolUseBlock:
				sb.WriteString(fmt.Sprintf("[tool_use:%s %s]\n", b.Name, truncate(string(b.Input), 120)))
			}
		}
	case *types.UserMessage:
		// Include tool results so the parent can see tool output.
		for _, blk := range m.Msg.Content.Blocks {
			if tr, ok := blk.(*types.ToolResultBlock); ok {
				preview := truncate(tr.Content, 300)
				if tr.IsError {
					sb.WriteString(fmt.Sprintf("[tool_error] %s\n", preview))
				} else {
					sb.WriteString(fmt.Sprintf("[tool_result] %s\n", preview))
				}
			}
		}
	case *types.SystemMessage:
		// Bubble informational messages up.
		if m.Level == types.SystemLevelWarning || m.Level == types.SystemLevelError {
			sb.WriteString(fmt.Sprintf("[system:%s] %s\n", m.Level, m.Content))
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ─────────────────────────────────────────────────────────────────────────────
// SendMessageTool
// ─────────────────────────────────────────────────────────────────────────────

// SendMessageTool sends a follow-up prompt to a running subagent identified by
// its ID.  It mirrors SendMessage in the TS coordinator-worker pattern.
type SendMessageTool struct{}

func NewSendMessageTool() *SendMessageTool { return &SendMessageTool{} }

func (t *SendMessageTool) Name() string { return "SendMessage" }
func (t *SendMessageTool) Description() string {
	return "Send a follow-up prompt to a running subagent. " +
		"The subagent must have been started with the Agent tool in the same session."
}
func (t *SendMessageTool) IsEnabled() bool                               { return true }
func (t *SendMessageTool) IsReadOnly(map[string]interface{}) bool        { return false }
func (t *SendMessageTool) IsConcurrencySafe(map[string]interface{}) bool { return false }
func (t *SendMessageTool) MaxResultSizeChars() int                       { return 10_000 }

func (t *SendMessageTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"agent_id": map[string]interface{}{
				"type":        "string",
				"description": "The ID of the subagent to message (returned by the Agent tool).",
			},
			"message": map[string]interface{}{
				"type":        "string",
				"description": "The follow-up prompt to send.",
			},
		},
		"required": []string{"agent_id", "message"},
	}
}

func (t *SendMessageTool) CheckPermissions(input map[string]interface{}, ctx ToolContext) (PermissionResult, error) {
	return PermissionResult{Behavior: PermAllow, UpdatedInput: input}, nil
}

func (t *SendMessageTool) Call(
	input map[string]interface{},
	ctx ToolContext,
	_ CanUseToolFn,
	_ chan<- interface{},
) (ToolResult, error) {
	agentID, _ := input["agent_id"].(string)
	message, _ := input["message"].(string)

	if agentID == "" {
		return ToolResult{IsError: true, Data: "SendMessage: agent_id is required"}, nil
	}
	if message == "" {
		return ToolResult{IsError: true, Data: "SendMessage: message is required"}, nil
	}
	if ctx.AgentRegistry == nil {
		return ToolResult{IsError: true, Data: "SendMessage: no agent registry available in this context"}, nil
	}

	handle, ok := ctx.AgentRegistry.Get(agentID)
	if !ok {
		return ToolResult{IsError: true, Data: fmt.Sprintf("SendMessage: agent %q not found or has already exited", agentID)}, nil
	}

	if err := handle.SendPrompt(message); err != nil {
		return ToolResult{IsError: true, Data: fmt.Sprintf("SendMessage: %v", err)}, nil
	}

	b, _ := json.Marshal(map[string]string{"status": "sent", "agent_id": agentID})
	return ToolResult{Data: string(b)}, nil
}
