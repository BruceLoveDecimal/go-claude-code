// Package api provides an Anthropic API client with SSE streaming support.
// It mirrors the TypeScript queryModelWithStreaming() function in
// src/services/api/claude.ts.
package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/claude-code/go-claude-go/tools"
	"github.com/claude-code/go-claude-go/types"
)

const (
	defaultBaseURL   = "https://api.anthropic.com"
	anthropicVersion = "2023-06-01"
	defaultMaxTokens = 8096
)

// ─────────────────────────────────────────────────────────────────────────────
// Client
// ─────────────────────────────────────────────────────────────────────────────

// Client holds the HTTP client and authentication key used for every API call.
type Client struct {
	APIKey  string
	BaseURL string
	HTTP    *http.Client
}

// NewClient creates an API client.  baseURL may be empty to use the default.
func NewClient(apiKey, baseURL string) *Client {
	base := defaultBaseURL
	if baseURL != "" {
		base = strings.TrimRight(baseURL, "/")
	}
	return &Client{
		APIKey:  apiKey,
		BaseURL: base,
		HTTP:    &http.Client{Timeout: 10 * time.Minute},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Request / response types
// ─────────────────────────────────────────────────────────────────────────────

// StreamParams holds everything needed for a single streaming API call.
type StreamParams struct {
	Messages     []types.Message
	SystemPrompt string
	Tools        []tools.Tool
	Model        string
	MaxTokens    int
}

// apiRequest is the JSON body sent to POST /v1/messages.
type apiRequest struct {
	Model     string        `json:"model"`
	Messages  []apiMessage  `json:"messages"`
	System    string        `json:"system,omitempty"`
	Tools     []apiTool     `json:"tools,omitempty"`
	MaxTokens int           `json:"max_tokens"`
	Stream    bool          `json:"stream"`
}

type apiMessage struct {
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
}

type apiTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

// ─────────────────────────────────────────────────────────────────────────────
// StreamMessage
// ─────────────────────────────────────────────────────────────────────────────

// StreamMessage opens a streaming API connection and sends events and the final
// AssistantMessage on the returned channels.  The caller should drain msgCh
// until it closes, then check errCh.
//
// Event types on msgCh:
//   - *types.StreamDeltaEvent  — incremental text / tool-input delta
//   - *types.AssistantMessage  — complete assembled response (sent last)
func StreamMessage(
	ctx context.Context,
	client *Client,
	params StreamParams,
) (<-chan interface{}, <-chan error) {
	msgCh := make(chan interface{}, 64)
	errCh := make(chan error, 1)

	go func() {
		defer close(msgCh)
		defer close(errCh)

		if err := doStream(ctx, client, params, msgCh); err != nil {
			errCh <- err
		}
	}()

	return msgCh, errCh
}

// doStream performs the actual HTTP request and SSE parsing.
func doStream(
	ctx context.Context,
	client *Client,
	params StreamParams,
	out chan<- interface{},
) error {
	reqBody, err := buildRequest(params)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		client.BaseURL+"/v1/messages",
		bytes.NewReader(reqBody),
	)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", client.APIKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("accept", "text/event-stream")

	resp, err := client.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		apiErr := &types.APIError{
			Status:  resp.StatusCode,
			Message: string(body),
		}
		// Try to extract error type from JSON response body.
		var errBody struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &errBody) == nil && errBody.Error.Type != "" {
			apiErr.ErrType = errBody.Error.Type
			if errBody.Error.Message != "" {
				apiErr.Message = errBody.Error.Message
			}
		}
		return apiErr
	}

	return parseSSEStream(resp.Body, out)
}

// ─────────────────────────────────────────────────────────────────────────────
// SSE parsing
// ─────────────────────────────────────────────────────────────────────────────

func parseSSEStream(body io.Reader, out chan<- interface{}) error {
	assembler := newStreamAssembler()
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	var dataLines []string

	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			// Blank line: dispatch accumulated data lines as one event
			if len(dataLines) > 0 {
				data := strings.Join(dataLines, "\n")
				dataLines = dataLines[:0]
				if err := dispatchSSEData(data, assembler, out); err != nil {
					return err
				}
			}
			continue
		}

		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		// Ignore "event:" and "id:" lines
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("sse scan: %w", err)
	}
	return nil
}

func dispatchSSEData(data string, assembler *streamAssembler, out chan<- interface{}) error {
	if data == "[DONE]" {
		return nil
	}
	var ev sseEvent
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return nil // ignore malformed events
	}

	// Handle SSE-level error events (e.g. overloaded_error).
	if ev.Type == "error" && ev.Error != nil {
		return &types.APIError{
			Status:  529,
			Message: ev.Error.Message,
			ErrType: ev.Error.Type,
		}
	}

	delta, complete := assembler.applyEvent(ev)
	if delta != nil {
		out <- delta
	}
	if complete {
		msg := assembler.build()
		out <- msg
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Request building
// ─────────────────────────────────────────────────────────────────────────────

func buildRequest(params StreamParams) ([]byte, error) {
	maxTokens := params.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}

	apiMsgs, err := convertMessages(params.Messages)
	if err != nil {
		return nil, err
	}

	apiTools := convertTools(params.Tools)

	req := apiRequest{
		Model:     params.Model,
		Messages:  apiMsgs,
		System:    params.SystemPrompt,
		Tools:     apiTools,
		MaxTokens: maxTokens,
		Stream:    true,
	}

	return json.Marshal(req)
}

// convertMessages transforms the internal Message slice into the flat
// user/assistant alternation that the Anthropic API expects.  It filters out
// non-API messages (system, tombstone, progress, etc.) and ensures proper
// tool_result pairing.
func convertMessages(messages []types.Message) ([]apiMessage, error) {
	var out []apiMessage

	for _, msg := range messages {
		switch m := msg.(type) {
		case *types.UserMessage:
			if m.IsMeta || m.IsVirtual {
				continue
			}
			content, err := convertUserContent(m.Msg.Content)
			if err != nil {
				return nil, err
			}
			out = append(out, apiMessage{Role: "user", Content: content})

		case *types.AssistantMessage:
			if m.IsAPIErrorMessage {
				continue
			}
			content, err := convertAssistantContent(m.Msg.Content)
			if err != nil {
				return nil, err
			}
			out = append(out, apiMessage{Role: "assistant", Content: content})
		}
		// Skip SystemMessage, TombstoneMessage, ToolUseSummaryMessage
	}

	return out, nil
}

func convertUserContent(rc types.RawContent) ([]json.RawMessage, error) {
	if len(rc.Blocks) == 0 {
		// Plain text
		block := map[string]interface{}{"type": "text", "text": rc.Text}
		b, err := json.Marshal(block)
		return []json.RawMessage{b}, err
	}
	var out []json.RawMessage
	for _, blk := range rc.Blocks {
		b, err := marshalBlock(blk)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func convertAssistantContent(blocks []types.ContentBlock) ([]json.RawMessage, error) {
	var out []json.RawMessage
	for _, blk := range blocks {
		b, err := marshalBlock(blk)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func marshalBlock(blk types.ContentBlock) (json.RawMessage, error) {
	switch b := blk.(type) {
	case *types.TextBlock:
		return json.Marshal(map[string]interface{}{"type": "text", "text": b.Text})
	case *types.ToolUseBlock:
		return json.Marshal(map[string]interface{}{
			"type":  "tool_use",
			"id":    b.ID,
			"name":  b.Name,
			"input": json.RawMessage(b.Input),
		})
	case *types.ToolResultBlock:
		return json.Marshal(map[string]interface{}{
			"type":        "tool_result",
			"tool_use_id": b.ToolUseID,
			"content":     b.Content,
			"is_error":    b.IsError,
		})
	case *types.ThinkingBlock:
		return json.Marshal(map[string]interface{}{"type": "thinking", "thinking": b.Thinking})
	case *types.RedactedThinkingBlock:
		return json.Marshal(map[string]interface{}{"type": "redacted_thinking", "data": b.Data})
	default:
		return json.Marshal(blk)
	}
}

func convertTools(ts []tools.Tool) []apiTool {
	var out []apiTool
	for _, t := range ts {
		if !t.IsEnabled() {
			continue
		}
		out = append(out, apiTool{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	return out
}
