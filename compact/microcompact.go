package compact

import (
	"github.com/claude-code/go-claude-go/types"
)

// ApplyMicroCompact removes duplicate tool_result blocks for the same
// tool_use_id, keeping only the first occurrence.  This mirrors the
// microcompact behaviour in src/services/compact/microCompact.ts.
//
// Returns the cleaned message slice and the estimated number of tokens freed.
func ApplyMicroCompact(messages []types.Message) ([]types.Message, int) {
	seen := make(map[string]bool) // tool_use_id → already kept
	out := make([]types.Message, 0, len(messages))
	tokensFreed := 0

	for _, msg := range messages {
		um, ok := msg.(*types.UserMessage)
		if !ok {
			out = append(out, msg)
			continue
		}

		// Only inspect messages that are pure tool-result carriers (i.e.
		// every content block is a tool_result).
		if !isToolResultMessage(um) {
			out = append(out, msg)
			continue
		}

		// Collect the unique tool_use_ids in this message.
		ids := toolUseIDs(um)
		if len(ids) == 0 {
			out = append(out, msg)
			continue
		}

		// If all IDs have already been seen, drop the entire message.
		allDuplicate := true
		for _, id := range ids {
			if !seen[id] {
				allDuplicate = false
				break
			}
		}

		if allDuplicate {
			tokensFreed += estimateContentTokens(um.Msg.Content)
			continue // skip duplicate
		}

		// Mark IDs as seen and keep the message.
		for _, id := range ids {
			seen[id] = true
		}
		out = append(out, msg)
	}

	return out, tokensFreed
}

// isToolResultMessage reports whether all content blocks in a UserMessage are
// tool_result blocks.
func isToolResultMessage(m *types.UserMessage) bool {
	blocks := m.Msg.Content.Blocks
	if len(blocks) == 0 {
		return false
	}
	for _, blk := range blocks {
		if _, ok := blk.(*types.ToolResultBlock); !ok {
			return false
		}
	}
	return true
}

// toolUseIDs extracts all tool_use_id values from tool_result blocks.
func toolUseIDs(m *types.UserMessage) []string {
	var ids []string
	for _, blk := range m.Msg.Content.Blocks {
		if tr, ok := blk.(*types.ToolResultBlock); ok {
			ids = append(ids, tr.ToolUseID)
		}
	}
	return ids
}
