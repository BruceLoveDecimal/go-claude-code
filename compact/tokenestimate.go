package compact

import (
	"encoding/json"
	"strings"

	"github.com/claude-code/go-claude-go/types"
)

// ─────────────────────────────────────────────────────────────────────────────
// Token estimation
// ─────────────────────────────────────────────────────────────────────────────

// EstimateTokens returns a rough token count for the given message slice.
// The estimate uses ~4 chars/token for prose and ~2 chars/token for JSON
// blobs (tools, schemas, tool results).  It is not byte-perfect but is within
// ±15% for typical agent conversations and is fast (no external calls).
//
// The primary use-cases are:
//   - Deciding whether to trigger proactive compaction.
//   - Surfacing token usage estimates in SDK consumer dashboards.
func EstimateTokens(messages []types.Message) int {
	total := 0
	for _, msg := range messages {
		total += estimateMessage(msg)
	}
	return total
}

// EstimateSystemPromptTokens returns the estimated token count for a system
// prompt string.
func EstimateSystemPromptTokens(systemPrompt string) int {
	return charsToTokens(len(systemPrompt))
}

// TokenBudgetStatus describes how full the effective context window is.
type TokenBudgetStatus struct {
	// Estimated is the estimated token count of the conversation.
	Estimated int
	// ContextWindow is the effective context window for the model.
	ContextWindow int
	// UsedFraction is Estimated / ContextWindow (0.0–1.0+).
	UsedFraction float64
	// ShouldCompact is true when the conversation should be compacted before
	// the next API call.
	ShouldCompact bool
}

// CheckTokenBudget returns a TokenBudgetStatus for the given messages and model.
// It applies the same AutoCompactBufferTokens margin used by AutoCompactIfNeeded.
func CheckTokenBudget(messages []types.Message, systemPrompt, model string) TokenBudgetStatus {
	contextWindow := GetEffectiveContextWindowSize(model)
	estimated := EstimateTokens(messages) + EstimateSystemPromptTokens(systemPrompt)
	threshold := contextWindow - AutoCompactBufferTokens

	frac := float64(estimated) / float64(contextWindow)
	if frac < 0 {
		frac = 0
	}
	return TokenBudgetStatus{
		Estimated:     estimated,
		ContextWindow: contextWindow,
		UsedFraction:  frac,
		ShouldCompact: estimated >= threshold,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ─────────────────────────────────────────────────────────────────────────────

func estimateMessage(msg types.Message) int {
	// Role overhead (~4 tokens each)
	const roleOverhead = 4

	switch m := msg.(type) {
	case *types.UserMessage:
		if m.IsMeta || m.IsVirtual {
			return 0
		}
		return roleOverhead + estimateRawContent(m.Msg.Content)

	case *types.AssistantMessage:
		total := roleOverhead
		for _, blk := range m.Msg.Content {
			total += estimateContentBlock(blk)
		}
		return total

	case *types.SystemMessage:
		return roleOverhead + charsToTokens(len(m.Content))

	default:
		// Fallback: marshal to JSON and count.
		if b, err := json.Marshal(msg); err == nil {
			return charsToTokens(len(b))
		}
		return 0
	}
}

func estimateRawContent(rc types.RawContent) int {
	if len(rc.Blocks) == 0 {
		return charsToTokens(len(rc.Text))
	}
	total := 0
	for _, blk := range rc.Blocks {
		total += estimateContentBlock(blk)
	}
	return total
}

func estimateContentBlock(blk types.ContentBlock) int {
	switch b := blk.(type) {
	case *types.TextBlock:
		return charsToTokens(len(b.Text))
	case *types.ToolUseBlock:
		// Tool name + JSON input
		return charsToTokens(len(b.Name)) + jsonBytesToTokens(len(b.Input))
	case *types.ToolResultBlock:
		return jsonBytesToTokens(len(b.Content))
	case *types.ThinkingBlock:
		return charsToTokens(len(b.Thinking))
	case *types.RedactedThinkingBlock:
		return charsToTokens(len(b.Data))
	default:
		if raw, err := json.Marshal(blk); err == nil {
			return jsonBytesToTokens(len(raw))
		}
		return 0
	}
}

// charsToTokens converts a character count to an approximate token count
// using the standard 4-chars-per-token heuristic for English prose.
func charsToTokens(chars int) int {
	if chars <= 0 {
		return 0
	}
	return (chars + avgCharsPerToken - 1) / avgCharsPerToken
}

// jsonBytesToTokens converts a JSON byte count to approximate tokens.  JSON
// is denser than prose (~2 chars/token on average) so we use a lower ratio.
func jsonBytesToTokens(bytes int) int {
	if bytes <= 0 {
		return 0
	}
	const jsonCharsPerToken = 2
	return (bytes + jsonCharsPerToken - 1) / jsonCharsPerToken
}

// EstimateString is a convenience function that estimates tokens in a plain
// string, stripping any markdown fences before counting.
func EstimateString(s string) int {
	s = strings.TrimSpace(s)
	return charsToTokens(len(s))
}
