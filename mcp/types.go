// Package mcp provides types and a client for the Model Context Protocol (MCP).
// MCP is a JSON-RPC 2.0 based protocol that allows Claude to communicate with
// external tool servers over stdio (or other transports).
package mcp

// MCPTool represents a tool advertised by an MCP server.
type MCPTool struct {
	// Name is the tool's canonical identifier within the server.
	Name string `json:"name"`
	// Description explains what the tool does (shown to the model).
	Description string `json:"description"`
	// InputSchema is a JSON Schema object describing the tool's input.
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// MCPResource represents a resource exposed by an MCP server.
type MCPResource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// MCPContent is a single content block within an MCPCallResult.
type MCPContent struct {
	// Type is the content kind: "text", "image", or "resource".
	Type string `json:"type"`
	// Text is populated when Type == "text".
	Text string `json:"text,omitempty"`
	// Data is base64-encoded bytes, populated when Type == "image".
	Data string `json:"data,omitempty"`
	// MimeType is the media type, populated for image and resource content.
	MimeType string `json:"mimeType,omitempty"`
}

// MCPCallResult is the response from a tools/call RPC invocation.
type MCPCallResult struct {
	Content []MCPContent `json:"content"`
	// IsError is set by the server to signal that the tool returned an error.
	IsError bool `json:"isError,omitempty"`
}

// MCPServerConfig holds the parameters needed to launch an MCP server process.
type MCPServerConfig struct {
	// Name is a human-readable label for this server (used in tool namespacing).
	Name string
	// Command is the executable to run (stdio transport only).
	Command string
	// Args are the command-line arguments passed to Command.
	Args []string
	// Env contains additional KEY=VALUE pairs injected into the server's
	// environment (merged with the current process environment).
	Env []string
}
