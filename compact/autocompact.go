// Package compact implements the three-layer context management system from
// Claude Code:
//
//  1. AutoCompact  — full conversation summarisation when the context window is
//     nearly full (mirrors src/services/compact/autoCompact.ts).
//  2. MicroCompact — deduplication of repeated tool_result blocks.
//  3. Snip         — pattern-based removal of redundant intermediate tool output.
package compact

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/claude-code/go-claude-go/types"
	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// Constants
// ─────────────────────────────────────────────────────────────────────────────

const (
	// AutoCompactBufferTokens is the safety margin subtracted from the
	// effective context window to produce the compaction threshold.
	AutoCompactBufferTokens = 13_000

	// MaxConsecutiveFailures is the circuit-breaker limit: if autocompact
	// fails this many times in a row, the loop stops trying.
	MaxConsecutiveFailures = 3

	// avgCharsPerToken is a rough approximation used for token estimation.
	avgCharsPerToken = 4
)

// modelContextWindows maps known model IDs to their context window sizes.
var modelContextWindows = map[string]int{
	"claude-opus-4-6":            200_000,
	"claude-sonnet-4-6":          200_000,
	"claude-haiku-4-5-20251001":  200_000,
	"claude-3-5-sonnet-20241022": 200_000,
	"claude-3-5-haiku-20241022":  200_000,
	"claude-3-opus-20240229":     200_000,
	"claude-3-haiku-20240307":    200_000,
}

const defaultContextWindow = 200_000

// GetEffectiveContextWindowSize returns the model's context window minus a
// 20k token reserved budget for the model's own output.
func GetEffectiveContextWindowSize(model string) int {
	window, ok := modelContextWindows[model]
	if !ok {
		window = defaultContextWindow
	}
	return window - 20_000
}

// GetAutoCompactThreshold returns the token count at which autocompact should
// trigger for the given model.
func GetAutoCompactThreshold(model string) int {
	return GetEffectiveContextWindowSize(model) - AutoCompactBufferTokens
}

// ─────────────────────────────────────────────────────────────────────────────
// Token estimation
// ─────────────────────────────────────────────────────────────────────────────

// EstimateTokenCount returns an approximate token count for a slice of
// messages based on character count.
func EstimateTokenCount(messages []types.Message) int {
	total := 0
	for _, msg := range messages {
		switch m := msg.(type) {
		case *types.UserMessage:
			total += estimateContentTokens(m.Msg.Content)
		case *types.AssistantMessage:
			for _, blk := range m.Msg.Content {
				total += estimateBlockTokens(blk)
			}
		case *types.SystemMessage:
			total += len(m.Content) / avgCharsPerToken
		}
	}
	return total
}

func estimateContentTokens(rc types.RawContent) int {
	if len(rc.Blocks) == 0 {
		return len(rc.Text) / avgCharsPerToken
	}
	total := 0
	for _, blk := range rc.Blocks {
		total += estimateBlockTokens(blk)
	}
	return total
}

func estimateBlockTokens(blk types.ContentBlock) int {
	switch b := blk.(type) {
	case *types.TextBlock:
		return len(b.Text) / avgCharsPerToken
	case *types.ToolUseBlock:
		return (len(b.Name) + len(b.Input)) / avgCharsPerToken
	case *types.ToolResultBlock:
		return len(b.Content) / avgCharsPerToken
	case *types.ThinkingBlock:
		return len(b.Thinking) / avgCharsPerToken
	}
	return 0
}

// ─────────────────────────────────────────────────────────────────────────────
// AutoCompact state
// ─────────────────────────────────────────────────────────────────────────────

// AutoCompactTrackingState is carried in the query loop's State and updated
// after each compaction event.
type AutoCompactTrackingState struct {
	Compacted           bool
	TurnCounter         int
	TurnID              string
	ConsecutiveFailures int
}

// CompactionResult is returned by CompactConversation.
type CompactionResult struct {
	SummaryMessages       []types.Message
	PreCompactTokenCount  int
	PostCompactTokenCount int
	MessagesSummarized    int
}

// ─────────────────────────────────────────────────────────────────────────────
// AutoCompactIfNeeded
// ─────────────────────────────────────────────────────────────────────────────

// AutoCompactConfig carries the dependencies needed by AutoCompactIfNeeded.
type AutoCompactConfig struct {
	// APIKey and Model are used to call the summarisation API.
	APIKey string
	Model  string
	// SummaryModel is the lighter model used for summarisation.
	// Falls back to Model if empty.
	SummaryModel string
	// BaseURL allows overriding the Anthropic API endpoint (useful for tests).
	BaseURL string
}

// AutoCompactIfNeeded checks whether the message history exceeds the compaction
// threshold and, if so, calls CompactConversation.  Returns (result, true) on
// success or (nil, false) when compaction is not needed or fails.
func AutoCompactIfNeeded(
	ctx context.Context,
	messages []types.Message,
	cfg AutoCompactConfig,
	tracking *AutoCompactTrackingState,
) (*CompactionResult, bool) {
	// Circuit breaker
	if tracking != nil && tracking.ConsecutiveFailures >= MaxConsecutiveFailures {
		return nil, false
	}

	tokenCount := EstimateTokenCount(messages)
	threshold := GetAutoCompactThreshold(cfg.Model)
	if tokenCount < threshold {
		return nil, false
	}

	result, err := CompactConversation(ctx, messages, cfg)
	if err != nil {
		return nil, false
	}
	return result, true
}

// ─────────────────────────────────────────────────────────────────────────────
// CompactConversation — calls the API to summarise
// ─────────────────────────────────────────────────────────────────────────────

// CompactConversation sends the conversation to a summarisation model and
// returns a CompactionResult containing:
//   - A system "compact boundary" message
//   - A user message containing the summary
//   - The final N messages preserved verbatim (tail)
func CompactConversation(
	ctx context.Context,
	messages []types.Message,
	cfg AutoCompactConfig,
) (*CompactionResult, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("no messages to compact")
	}

	preCount := EstimateTokenCount(messages)

	// Preserve the last few messages verbatim so the model has recent context.
	const tailSize = 5
	var toSummarise, tail []types.Message
	if len(messages) > tailSize {
		toSummarise = messages[:len(messages)-tailSize]
		tail = messages[len(messages)-tailSize:]
	} else {
		toSummarise = messages
	}

	summaryModel := cfg.SummaryModel
	if summaryModel == "" {
		summaryModel = cfg.Model
	}

	summary, err := callSummaryAPI(ctx, toSummarise, summaryModel, cfg.APIKey, cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("summary API call: %w", err)
	}

	// Build the post-compact message list:
	//   [compact_boundary, user(summary), ...tail]
	boundary := &types.SystemMessage{
		Type:      types.MessageTypeSystem,
		Subtype:   types.SystemSubtypeCompactBoundary,
		UUID:      uuid.New().String(),
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Content:   fmt.Sprintf("Context compacted. Summarized %d messages.", len(toSummarise)),
		Level:     types.SystemLevelInfo,
		CompactMetadata: &types.CompactMetadata{
			PreCompactTokenCount:  preCount,
			MessagesSummarized:    len(toSummarise),
		},
	}

	summaryMsg := &types.UserMessage{
		Type:             types.MessageTypeUser,
		UUID:             uuid.New().String(),
		Timestamp:        time.Now().UTC().Format(time.RFC3339Nano),
		IsCompactSummary: true,
		Msg: types.UserContent{
			Role:    "user",
			Content: types.RawContent{Text: summary},
		},
	}

	var summaryMessages []types.Message
	summaryMessages = append(summaryMessages, boundary, summaryMsg)
	summaryMessages = append(summaryMessages, tail...)

	postCount := EstimateTokenCount(summaryMessages)
	boundary.CompactMetadata.PostCompactTokenCount = postCount

	return &CompactionResult{
		SummaryMessages:       summaryMessages,
		PreCompactTokenCount:  preCount,
		PostCompactTokenCount: postCount,
		MessagesSummarized:    len(toSummarise),
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Summary API helper
// ─────────────────────────────────────────────────────────────────────────────

type summaryRequest struct {
	Model     string               `json:"model"`
	MaxTokens int                  `json:"max_tokens"`
	System    string               `json:"system"`
	Messages  []summaryMessage     `json:"messages"`
}

type summaryMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type summaryResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func callSummaryAPI(
	ctx context.Context,
	messages []types.Message,
	model, apiKey, baseURL string,
) (string, error) {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}

	// Build a plain-text transcript of the messages to summarise.
	var sb strings.Builder
	for _, msg := range messages {
		switch m := msg.(type) {
		case *types.UserMessage:
			sb.WriteString("User: ")
			if len(m.Msg.Content.Blocks) > 0 {
				for _, blk := range m.Msg.Content.Blocks {
					if tb, ok := blk.(*types.TextBlock); ok {
						sb.WriteString(tb.Text)
					} else if tr, ok := blk.(*types.ToolResultBlock); ok {
						fmt.Fprintf(&sb, "[tool_result(%s): %s]", tr.ToolUseID, tr.Content)
					}
				}
			} else {
				sb.WriteString(m.Msg.Content.Text)
			}
			sb.WriteString("\n")
		case *types.AssistantMessage:
			sb.WriteString("Assistant: ")
			sb.WriteString(m.TextContent())
			for _, blk := range m.Msg.Content {
				if tb, ok := blk.(*types.ToolUseBlock); ok {
					fmt.Fprintf(&sb, "[tool_use(%s, %s)]", tb.Name, string(tb.Input))
				}
			}
			sb.WriteString("\n")
		}
	}

	reqBody, err := json.Marshal(summaryRequest{
		Model:     model,
		MaxTokens: 4096,
		System:    "You are a helpful assistant. Summarize the following conversation concisely, preserving all important context, decisions, tool outputs, and file paths. The summary will replace the original conversation, so include everything needed to continue the work.",
		Messages: []summaryMessage{
			{Role: "user", Content: "Please summarize this conversation:\n\n" + sb.String()},
		},
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+"/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("summary API status %d", resp.StatusCode)
	}

	var sr summaryResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return "", err
	}
	for _, c := range sr.Content {
		if c.Type == "text" {
			return c.Text, nil
		}
	}
	return "", fmt.Errorf("no text in summary response")
}
