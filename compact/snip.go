package compact

import (
	"strings"

	"github.com/claude-code/go-claude-go/types"
)

// SnipResult is returned by ApplySnipIfNeeded.
type SnipResult struct {
	Messages    []types.Message
	TokensFreed int
}

// snippableTools are the tool names whose intermediate outputs are candidates
// for removal when a later message makes them redundant.
var snippableTools = map[string]bool{
	"Bash": true,
	"Grep": true,
	"Glob": true,
}

// ApplySnipIfNeeded removes intermediate, redundant tool_result blocks from
// repetitive read-only tools (Bash, Grep, Glob) when the same tool has been
// called again with a result that supersedes earlier outputs.
//
// The heuristic mirrors src/services/compact/snipCompact.ts:
//   - Scan for repeated Bash/Grep/Glob tool_use → tool_result pairs.
//   - If the same tool name appears more than once in a row with no
//     write-tool between them, drop all but the last occurrence.
//
// This is a conservative approximation; the TypeScript version uses
// feature-gated pattern matching.
func ApplySnipIfNeeded(messages []types.Message) SnipResult {
	// Build an index of (tool_use_id → assistant message index) for
	// snippable tools so we can find their tool_result counterparts.
	type snipCandidate struct {
		toolName   string
		toolUseID  string
		assistIdx  int // index in messages of the AssistantMessage
		resultIdx  int // index in messages of the UserMessage (tool_result)
	}

	var candidates []snipCandidate

	for i, msg := range messages {
		am, ok := msg.(*types.AssistantMessage)
		if !ok {
			continue
		}
		for _, blk := range am.Msg.Content {
			tb, ok := blk.(*types.ToolUseBlock)
			if !ok || !snippableTools[tb.Name] {
				continue
			}
			candidates = append(candidates, snipCandidate{
				toolName:  tb.Name,
				toolUseID: tb.ID,
				assistIdx: i,
				resultIdx: -1,
			})
		}
	}

	// Find the matching tool_result index for each candidate.
	for ci := range candidates {
		id := candidates[ci].toolUseID
		for i, msg := range messages {
			um, ok := msg.(*types.UserMessage)
			if !ok {
				continue
			}
			for _, blk := range um.Msg.Content.Blocks {
				tr, ok := blk.(*types.ToolResultBlock)
				if ok && tr.ToolUseID == id {
					candidates[ci].resultIdx = i
				}
			}
		}
	}

	// Group candidates by tool name and find which ones are intermediate
	// (i.e. there is a later occurrence of the same tool name).
	type toolGroup struct {
		indices []int // indices into candidates
	}
	groups := make(map[string]*toolGroup)
	for ci, c := range candidates {
		g, ok := groups[c.toolName]
		if !ok {
			g = &toolGroup{}
			groups[c.toolName] = g
		}
		g.indices = append(g.indices, ci)
	}

	// Mark message indices that should be removed.
	removeIdx := make(map[int]bool)
	for _, g := range groups {
		if len(g.indices) < 2 {
			continue
		}
		// Drop all but the last occurrence.
		for _, ci := range g.indices[:len(g.indices)-1] {
			c := candidates[ci]
			if c.resultIdx >= 0 {
				removeIdx[c.resultIdx] = true
			}
			removeIdx[c.assistIdx] = true
		}
	}

	if len(removeIdx) == 0 {
		return SnipResult{Messages: messages}
	}

	tokensFreed := 0
	out := make([]types.Message, 0, len(messages)-len(removeIdx))
	for i, msg := range messages {
		if removeIdx[i] {
			tokensFreed += estimateMessageTokens(msg)
			continue
		}
		out = append(out, msg)
	}

	return SnipResult{Messages: out, TokensFreed: tokensFreed}
}

// estimateMessageTokens returns an approximate token count for a single message.
func estimateMessageTokens(msg types.Message) int {
	switch m := msg.(type) {
	case *types.UserMessage:
		return estimateContentTokens(m.Msg.Content)
	case *types.AssistantMessage:
		total := 0
		for _, blk := range m.Msg.Content {
			total += estimateBlockTokens(blk)
		}
		return total
	case *types.SystemMessage:
		return len(strings.Fields(m.Content)) // rough word count
	}
	return 0
}
