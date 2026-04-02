package tools

import (
	"github.com/claude-code/go-claude-go/types"
)

const (
	// DefaultToolResultBudget is the default maximum total size (in characters)
	// of all tool_result content in the live message window.
	DefaultToolResultBudget = 250_000

	// budgetTruncMinSize is the minimum content length a tool_result must have
	// to be eligible for truncation.  Small results are never touched.
	budgetTruncMinSize = 500

	budgetTruncPlaceholder = "[content truncated to conserve context space]"
)

// ApplyToolResultBudget ensures the total character size of all tool_result
// blocks in messages does not exceed budgetChars.  When over-budget the oldest
// results with content larger than budgetTruncMinSize are replaced with a short
// placeholder.
//
// The input slice is never mutated; a new slice is returned when changes are
// made.  Truncations are recorded in state (if non-nil).
func ApplyToolResultBudget(
	messages []types.Message,
	budgetChars int,
	state *ContentReplacementState,
) []types.Message {
	type blockRef struct {
		msgIdx int
		blkIdx int
		size   int
	}

	// ── Pass 1: measure total tool_result size ─────────────────────────────
	var refs []blockRef
	total := 0

	for i, msg := range messages {
		um, ok := msg.(*types.UserMessage)
		if !ok {
			continue
		}
		for j, blk := range um.Msg.Content.Blocks {
			if tr, ok := blk.(*types.ToolResultBlock); ok {
				refs = append(refs, blockRef{i, j, len(tr.Content)})
				total += len(tr.Content)
			}
		}
	}

	if total <= budgetChars || len(refs) == 0 {
		return messages
	}

	// ── Pass 2: build a copy and truncate oldest large results ─────────────
	msgs := make([]types.Message, len(messages))
	copy(msgs, messages)

	// Track which UserMessages have already been deep-copied.
	copied := make(map[int]bool)

	for _, ref := range refs {
		if total <= budgetChars {
			break
		}
		if ref.size < budgetTruncMinSize {
			continue
		}

		// Deep-copy the UserMessage (once) so we don't mutate the original.
		if !copied[ref.msgIdx] {
			orig := msgs[ref.msgIdx].(*types.UserMessage)
			dup := *orig
			newContent := orig.Msg.Content
			newBlocks := make([]types.ContentBlock, len(newContent.Blocks))
			copy(newBlocks, newContent.Blocks)
			newContent.Blocks = newBlocks
			dupMsg := dup.Msg
			dupMsg.Content = newContent
			dup.Msg = dupMsg
			msgs[ref.msgIdx] = &dup
			copied[ref.msgIdx] = true
		}

		um := msgs[ref.msgIdx].(*types.UserMessage)
		oldTR := um.Msg.Content.Blocks[ref.blkIdx].(*types.ToolResultBlock)
		newTR := *oldTR
		newTR.Content = budgetTruncPlaceholder
		um.Msg.Content.Blocks[ref.blkIdx] = &newTR

		if state != nil {
			state.Add(ContentReplacementRecord{
				ToolUseID:    oldTR.ToolUseID,
				OriginalSize: ref.size,
				Replacement:  budgetTruncPlaceholder,
			})
		}

		total -= ref.size - len(budgetTruncPlaceholder)
	}

	return msgs
}
