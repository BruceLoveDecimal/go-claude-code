package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
)

// ─────────────────────────────────────────────────────────────────────────────
// Client interface
// ─────────────────────────────────────────────────────────────────────────────

// Client is the interface for interacting with an MCP server.
type Client interface {
	// Name returns the human-readable label for this server.
	Name() string
	// Initialize performs the MCP handshake (must be called once before use).
	Initialize(ctx context.Context) error
	// ListTools returns all tools advertised by the server.
	ListTools(ctx context.Context) ([]MCPTool, error)
	// CallTool invokes a named tool with the given arguments.
	CallTool(ctx context.Context, name string, args map[string]interface{}) (MCPCallResult, error)
	// Close shuts down the connection.
	Close() error
}

// ─────────────────────────────────────────────────────────────────────────────
// JSON-RPC 2.0 wire types
// ─────────────────────────────────────────────────────────────────────────────

type rpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type rpcNotification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ─────────────────────────────────────────────────────────────────────────────
// StdioMCPClient
// ─────────────────────────────────────────────────────────────────────────────

// StdioMCPClient communicates with an MCP server over stdin/stdout.
// Call Initialize() before ListTools() or CallTool().
type StdioMCPClient struct {
	config  MCPServerConfig
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	scanner *bufio.Scanner
	mu      sync.Mutex // guards stdin and scanner access
	nextID  atomic.Int64
}

// NewStdioMCPClient creates a new stdio-based MCP client.  The server process
// is not launched until Initialize() is called.
func NewStdioMCPClient(cfg MCPServerConfig) *StdioMCPClient {
	return &StdioMCPClient{config: cfg}
}

// Name returns the human-readable server name from config.
func (c *StdioMCPClient) Name() string { return c.config.Name }

// Initialize launches the server process and performs the MCP handshake.
func (c *StdioMCPClient) Initialize(ctx context.Context) error {
	if err := c.start(); err != nil {
		return err
	}

	initParams := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]interface{}{
			"name":    "go-claude-go",
			"version": "0.1.0",
		},
	}
	if _, err := c.call(ctx, "initialize", initParams); err != nil {
		return fmt.Errorf("mcp initialize: %w", err)
	}

	// Send the initialized notification (server expects it but sends no reply).
	return c.notify("notifications/initialized", nil)
}

// ListTools returns all tools the server currently advertises.
func (c *StdioMCPClient) ListTools(ctx context.Context) ([]MCPTool, error) {
	raw, err := c.call(ctx, "tools/list", map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("mcp tools/list: %w", err)
	}

	var resp struct {
		Tools []MCPTool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("mcp tools/list decode: %w", err)
	}
	return resp.Tools, nil
}

// CallTool invokes the named tool with the provided arguments.
func (c *StdioMCPClient) CallTool(ctx context.Context, name string, args map[string]interface{}) (MCPCallResult, error) {
	params := map[string]interface{}{
		"name":      name,
		"arguments": args,
	}
	raw, err := c.call(ctx, "tools/call", params)
	if err != nil {
		return MCPCallResult{}, fmt.Errorf("mcp tools/call %q: %w", name, err)
	}

	var result MCPCallResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return MCPCallResult{}, fmt.Errorf("mcp tools/call decode: %w", err)
	}
	return result, nil
}

// Close terminates the server process.
func (c *StdioMCPClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		return c.cmd.Process.Kill()
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ─────────────────────────────────────────────────────────────────────────────

// start launches the server subprocess and connects the stdio pipes.
func (c *StdioMCPClient) start() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	cmd := exec.Command(c.config.Command, c.config.Args...)
	if len(c.config.Env) > 0 {
		cmd.Env = append(cmd.Environ(), c.config.Env...)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("mcp stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("mcp stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("mcp server start: %w", err)
	}

	c.cmd = cmd
	c.stdin = stdin
	scanner := bufio.NewScanner(stdout)
	// Allocate a large buffer — MCP tool results can be substantial.
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	c.scanner = scanner
	return nil
}

// call sends a JSON-RPC request and waits for the matching response.
// Notifications from the server (no "id") are skipped.
func (c *StdioMCPClient) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	id := c.nextID.Add(1)
	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	b, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal rpc request: %w", err)
	}
	if _, err := fmt.Fprintf(c.stdin, "%s\n", b); err != nil {
		return nil, fmt.Errorf("write rpc request: %w", err)
	}

	// Read lines until we find the response with our request ID.
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		if !c.scanner.Scan() {
			if scanErr := c.scanner.Err(); scanErr != nil {
				return nil, fmt.Errorf("read rpc response: %w", scanErr)
			}
			return nil, fmt.Errorf("mcp server closed connection")
		}

		var resp rpcResponse
		if err := json.Unmarshal(c.scanner.Bytes(), &resp); err != nil {
			// Skip non-JSON lines (e.g. server debug/log output).
			continue
		}
		if resp.ID != id {
			// Notification or response for a different call; skip.
			continue
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

// notify sends a JSON-RPC notification (fire-and-forget; no response expected).
func (c *StdioMCPClient) notify(method string, params interface{}) error {
	n := rpcNotification{JSONRPC: "2.0", Method: method, Params: params}
	b, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}
	_, err = fmt.Fprintf(c.stdin, "%s\n", b)
	return err
}
