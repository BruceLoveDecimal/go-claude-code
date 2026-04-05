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
// AutoCompactResult wraps the CompactionResult and updated tracking state.
type AutoCompactResult struct {
	*CompactionResult
	// UpdatedTracking is the tracking state after this compaction attempt.
	// Callers should persist this for the next call.
	UpdatedTracking *AutoCompactTrackingState
}

func AutoCompactIfNeeded(
	ctx context.Context,
	messages []types.Message,
	cfg AutoCompactConfig,
	tracking *AutoCompactTrackingState,
) (*CompactionResult, bool) {
	// Circuit breaker — stop trying after repeated failures.
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
		// Increment the circuit breaker counter on failure.
		if tracking != nil {
			tracking.ConsecutiveFailures++
		}
		return nil, false
	}

	// Reset consecutive failures on success.
	if tracking != nil {
		tracking.ConsecutiveFailures = 0
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

	// Preserve recent messages grouped by "API round" (assistant + following
	// user messages).  This is more intelligent than a fixed tail size and
	// mirrors the TS groupMessagesByApiRound() approach.
	toSummarise, tail := splitByAPIRound(messages)

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

// ─────────────────────────────────────────────────────────────────────────────
// API round grouping for dynamic tail preservation
// ─────────────────────────────────────────────────────────────────────────────

// apiRound is a group of messages forming one assistant turn: an
// AssistantMessage followed by zero or more UserMessages (tool results).
type apiRound struct {
	messages []types.Message
	tokens   int
}

// splitByAPIRound splits messages into (toSummarise, tail) where tail
// contains the most recent complete API rounds, up to a token budget.
// This replaces the fixed tailSize=5 approach with a dynamic grouping
// strategy that mirrors the TS groupMessagesByApiRound().
//
// The budget for the tail is min(30% of total tokens, 20_000 tokens).
// At minimum, the last round is always preserved.
func splitByAPIRound(messages []types.Message) (toSummarise, tail []types.Message) {
	if len(messages) == 0 {
		return nil, nil
	}

	// Group messages into rounds.  A new round starts at each AssistantMessage.
	var rounds []apiRound
	var current apiRound

	for _, msg := range messages {
		if _, ok := msg.(*types.AssistantMessage); ok && len(current.messages) > 0 {
			rounds = append(rounds, current)
			current = apiRound{}
		}
		current.messages = append(current.messages, msg)
		current.tokens += estimateMessageTokens(msg)
	}
	if len(current.messages) > 0 {
		rounds = append(rounds, current)
	}

	if len(rounds) <= 1 {
		// Only one round — summarise everything (summary prompt will still
		// produce something useful).
		return messages, nil
	}

	// Determine how many rounds to keep in the tail.
	totalTokens := 0
	for _, r := range rounds {
		totalTokens += r.tokens
	}

	// Budget: min(30% of total, 20k tokens).
	budget := totalTokens * 30 / 100
	if budget > 20_000 {
		budget = 20_000
	}

	// Walk backwards, greedily adding rounds while within budget.
	tailStart := len(rounds)
	tailTokens := 0
	for i := len(rounds) - 1; i >= 0; i-- {
		if tailTokens+rounds[i].tokens > budget && tailStart < len(rounds) {
			break // already have at least one round, would exceed budget
		}
		tailTokens += rounds[i].tokens
		tailStart = i
	}

	// Always keep at least the last round.
	if tailStart >= len(rounds) {
		tailStart = len(rounds) - 1
	}

	// Flatten rounds into message slices.
	for _, r := range rounds[:tailStart] {
		toSummarise = append(toSummarise, r.messages...)
	}
	for _, r := range rounds[tailStart:] {
		tail = append(tail, r.messages...)
	}

	// If nothing to summarise (all messages are in tail), move the first
	// round to the summarise side so we actually have something to compact.
	if len(toSummarise) == 0 && len(rounds) > 1 {
		toSummarise = rounds[0].messages
		tail = nil
		for _, r := range rounds[1:] {
			tail = append(tail, r.messages...)
		}
	}

	return toSummarise, tail
}

// compactSystemPrompt is the structured prompt sent to the summary model.
// It mirrors the TS getCompactPrompt() in src/services/compact/prompt.ts.
const compactSystemPrompt = `CRITICAL: Respond with TEXT ONLY. Do NOT call any tools.
- Tool calls will be REJECTED and waste your only turn — you will fail.

You are a conversation summarizer. Your task is to create a detailed summary of the conversation so far. This summary will REPLACE the original conversation context, so include ALL information needed to continue the work.

Your output must use the following format:

<analysis>
Analyze the conversation and extract information for each section below.
</analysis>

<summary>
1. Primary Request and Intent:
   [What the user originally asked for and their underlying goal]

2. Key Technical Concepts:
   [Important technical details, architecture decisions, patterns discussed]

3. Files and Code Sections:
   [ALL file paths mentioned, functions modified, code snippets that were written or discussed. Include FULL code for any significant implementations — these will be lost if not included here.]

4. Errors and Fixes:
   [Any errors encountered and how they were resolved]

5. Problem Solving:
   [Key decisions made, approaches tried, reasoning behind choices]

6. All User Messages:
   [Preserve the essential content of every user message, including corrections, preferences, and instructions]

7. Pending Tasks:
   [Any incomplete work, next steps mentioned, or tasks that were deferred]

8. Current Work:
   [What was being worked on at the end of the conversation, including the last action taken and its result]

9. Optional Next Step:
   [If the conversation suggests an obvious next action, note it here]
</summary>`

func callSummaryAPI(
	ctx context.Context,
	messages []types.Message,
	model, apiKey, baseURL string,
) (string, error) {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}

	// Build a structured transcript of the messages to summarise.
	// Strip images/documents to avoid prompt-too-long on the summary call.
	var sb strings.Builder
	for _, msg := range messages {
		switch m := msg.(type) {
		case *types.UserMessage:
			sb.WriteString("User: ")
			if len(m.Msg.Content.Blocks) > 0 {
				for _, blk := range m.Msg.Content.Blocks {
					switch b := blk.(type) {
					case *types.TextBlock:
						sb.WriteString(b.Text)
					case *types.ToolResultBlock:
						// Truncate very large tool results for the summary.
						content := b.Content
						if len(content) > 2000 {
							content = content[:2000] + "... [truncated]"
						}
						fmt.Fprintf(&sb, "\n[tool_result(%s): %s]", b.ToolUseID, content)
					case *types.ImageBlock:
						sb.WriteString("[image omitted]")
					case *types.DocumentBlock:
						sb.WriteString("[document omitted]")
					}
				}
			} else {
				sb.WriteString(m.Msg.Content.Text)
			}
			sb.WriteString("\n\n")
		case *types.AssistantMessage:
			sb.WriteString("Assistant: ")
			sb.WriteString(m.TextContent())
			for _, blk := range m.Msg.Content {
				switch b := blk.(type) {
				case *types.ToolUseBlock:
					// Truncate large tool inputs.
					input := string(b.Input)
					if len(input) > 1000 {
						input = input[:1000] + "..."
					}
					fmt.Fprintf(&sb, "\n[tool_use(%s): %s]", b.Name, input)
				}
			}
			sb.WriteString("\n\n")
		}
	}

	reqBody, err := json.Marshal(summaryRequest{
		Model:     model,
		MaxTokens: 8192,
		System:    compactSystemPrompt,
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
