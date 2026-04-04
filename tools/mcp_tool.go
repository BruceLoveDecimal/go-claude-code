package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/claude-code/go-claude-go/mcp"
)

const mcpMaxResultSize = 100_000

// MCPToolWrapper adapts an mcp.MCPTool (advertised by a remote MCP server)
// into the Tool interface so it can be registered in a Registry and executed
// by the agent loop like any built-in tool.
type MCPToolWrapper struct {
	client     mcp.Client
	serverName string
	mcpTool    mcp.MCPTool
}

// NewMCPToolWrapper creates a Tool from a single MCP server tool definition.
func NewMCPToolWrapper(client mcp.Client, tool mcp.MCPTool) *MCPToolWrapper {
	return &MCPToolWrapper{
		client:     client,
		serverName: client.Name(),
		mcpTool:    tool,
	}
}

// Name returns a namespaced identifier in the form "mcp__<server>__<tool>"
// to avoid collisions between tools from different servers.
func (t *MCPToolWrapper) Name() string {
	return fmt.Sprintf("mcp__%s__%s",
		sanitizeName(t.serverName),
		sanitizeName(t.mcpTool.Name),
	)
}

// Description returns the MCP tool description prefixed with the server name.
func (t *MCPToolWrapper) Description() string {
	return fmt.Sprintf("[MCP:%s] %s", t.serverName, t.mcpTool.Description)
}

// InputSchema returns the JSON Schema provided by the MCP server.
func (t *MCPToolWrapper) InputSchema() map[string]interface{} {
	if t.mcpTool.InputSchema != nil {
		return t.mcpTool.InputSchema
	}
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
}

func (t *MCPToolWrapper) IsConcurrencySafe(map[string]interface{}) bool { return false }
func (t *MCPToolWrapper) IsReadOnly(map[string]interface{}) bool         { return false }
func (t *MCPToolWrapper) IsEnabled() bool                                { return true }
func (t *MCPToolWrapper) MaxResultSizeChars() int                        { return mcpMaxResultSize }

// CheckPermissions requires interactive confirmation (PermAsk) for all MCP
// tools because their side-effects are unknown to the client.
func (t *MCPToolWrapper) CheckPermissions(input map[string]interface{}, ctx ToolContext) (PermissionResult, error) {
	return PermissionResult{Behavior: PermAsk, UpdatedInput: input}, nil
}

// Call invokes the remote MCP tool and converts the result to a ToolResult.
func (t *MCPToolWrapper) Call(
	input map[string]interface{},
	ctx ToolContext,
	_ CanUseToolFn,
	_ chan<- interface{},
) (ToolResult, error) {
	result, err := t.client.CallTool(ctx.Ctx, t.mcpTool.Name, input)
	if err != nil {
		return ToolResult{IsError: true, Data: err.Error()}, nil
	}

	text := mcpContentToText(result.Content)
	return ToolResult{IsError: result.IsError, Data: text}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// mcpContentToText collapses an MCP content slice into a single string.
// Non-text blocks are JSON-encoded as a fallback.
func mcpContentToText(content []mcp.MCPContent) string {
	var sb strings.Builder
	for _, c := range content {
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		if c.Type == "text" {
			sb.WriteString(c.Text)
		} else {
			b, _ := json.Marshal(c)
			sb.Write(b)
		}
	}
	return sb.String()
}

// sanitizeName converts a name to a safe ASCII identifier by replacing any
// character that is not alphanumeric or underscore with an underscore.
func sanitizeName(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' {
			sb.WriteRune(r)
		} else {
			sb.WriteByte('_')
		}
	}
	return sb.String()
}
