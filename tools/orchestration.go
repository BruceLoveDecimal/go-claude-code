package tools

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/claude-code/go-claude-go/types"
	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// Batch partitioning
// ─────────────────────────────────────────────────────────────────────────────

// toolBatch groups one or more ToolUseBlock invocations that should be
// dispatched together (concurrently if concurrent==true, otherwise serially).
type toolBatch struct {
	blocks     []*types.ToolUseBlock
	concurrent bool
}

// partitionByConcurrency groups tool blocks into batches.  Consecutive
// concurrent-safe blocks go into a single concurrent batch; any unsafe block
// forms its own serial batch.  This preserves the ordering invariant: a
// serial tool never starts before all preceding tools have finished.
func partitionByConcurrency(
	blocks []*types.ToolUseBlock,
	registry *Registry,
) []toolBatch {
	var batches []toolBatch
	var currentBatch []*types.ToolUseBlock
	currentConcurrent := false

	flush := func() {
		if len(currentBatch) == 0 {
			return
		}
		batches = append(batches, toolBatch{blocks: currentBatch, concurrent: currentConcurrent})
		currentBatch = nil
	}

	for _, block := range blocks {
		tool, ok := registry.Get(block.Name)
		input, _ := block.InputMap()
		safe := ok && tool.IsConcurrencySafe(input)

		if len(currentBatch) == 0 {
			currentConcurrent = safe
			currentBatch = append(currentBatch, block)
		} else if safe == currentConcurrent && currentConcurrent {
			// Both concurrent-safe: merge into same batch
			currentBatch = append(currentBatch, block)
		} else {
			// Boundary: flush current, start new
			flush()
			currentConcurrent = safe
			currentBatch = append(currentBatch, block)
		}
	}
	flush()
	return batches
}

// ─────────────────────────────────────────────────────────────────────────────
// RunTools — public entry point
// ─────────────────────────────────────────────────────────────────────────────

// RunTools executes all tool_use blocks from an assistant turn and returns the
// corresponding tool_result UserMessages plus any side-channel messages
// injected by tools (via ToolResult.NewMessages).
//
// toolResults are the tool_result UserMessages that MUST go into the
// conversation history.  sideMessages are informational (confirmations,
// progress notes) and should be emitted to the output channel but NOT added
// to the API conversation history.
//
// Concurrency policy:
//   - Blocks that are read-only / concurrent-safe are dispatched as a batch
//     using goroutines.
//   - All other blocks are executed serially within their batch.
func RunTools(
	blocks     []*types.ToolUseBlock,
	canUse     CanUseToolFn,
	toolCtx    ToolContext,
) (toolResults []types.Message, sideMessages []types.Message, err error) {
	if len(blocks) == 0 {
		return nil, nil, nil
	}

	registry := toolCtx.Registry
	batches := partitionByConcurrency(blocks, registry)

	for _, batch := range batches {
		if batch.concurrent {
			results, sides, batchErr := runConcurrently(batch.blocks, canUse, toolCtx)
			if batchErr != nil {
				return nil, nil, batchErr
			}
			toolResults = append(toolResults, results...)
			sideMessages = append(sideMessages, sides...)
		} else {
			for _, block := range batch.blocks {
				msgs, singleErr := runSingleTool(block, canUse, toolCtx)
				if singleErr != nil {
					return nil, nil, singleErr
				}
				if len(msgs) > 0 {
					toolResults = append(toolResults, msgs[0])
					sideMessages = append(sideMessages, msgs[1:]...)
				}
			}
		}
	}
	return toolResults, sideMessages, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Concurrent dispatch
// ─────────────────────────────────────────────────────────────────────────────

type indexedResult struct {
	index int
	msgs  []types.Message // [0] = tool_result, [1:] = side messages
	err   error
}

func runConcurrently(
	blocks  []*types.ToolUseBlock,
	canUse  CanUseToolFn,
	toolCtx ToolContext,
) (toolResults []types.Message, sideMessages []types.Message, err error) {
	out := make([][]types.Message, len(blocks))
	ch := make(chan indexedResult, len(blocks))
	var wg sync.WaitGroup

	for i, block := range blocks {
		wg.Add(1)
		go func(idx int, b *types.ToolUseBlock) {
			defer wg.Done()
			msgs, runErr := runSingleTool(b, canUse, toolCtx)
			ch <- indexedResult{index: idx, msgs: msgs, err: runErr}
		}(i, block)
	}

	wg.Wait()
	close(ch)

	for res := range ch {
		if res.err != nil {
			return nil, nil, res.err
		}
		out[res.index] = res.msgs
	}

	for _, msgs := range out {
		if len(msgs) > 0 {
			toolResults = append(toolResults, msgs[0])
			sideMessages = append(sideMessages, msgs[1:]...)
		}
	}
	return toolResults, sideMessages, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Single tool execution
// ─────────────────────────────────────────────────────────────────────────────

// runSingleTool returns a slice where [0] is the tool_result message and
// [1:] are any side-channel messages from ToolResult.NewMessages.
func runSingleTool(
	block   *types.ToolUseBlock,
	canUse  CanUseToolFn,
	toolCtx ToolContext,
) ([]types.Message, error) {
	registry := toolCtx.Registry

	tool, ok := registry.Get(block.Name)
	if !ok {
		return []types.Message{makeErrorResult(block.ID, fmt.Sprintf("unknown tool: %s", block.Name))}, nil
	}

	input, err := block.InputMap()
	if err != nil {
		return []types.Message{makeErrorResult(block.ID, fmt.Sprintf("invalid tool input: %v", err))}, nil
	}

	// Permission check
	perm, err := canUse(tool.Name(), input, toolCtx)
	if err != nil {
		return []types.Message{makeErrorResult(block.ID, fmt.Sprintf("permission error: %v", err))}, nil
	}
	if perm.Behavior == PermBlock {
		reason := perm.Reason
		if reason == "" {
			reason = "permission denied"
		}
		return []types.Message{makeErrorResult(block.ID, reason)}, nil
	}
	// Use potentially updated input from permission handler
	if perm.UpdatedInput != nil {
		input = perm.UpdatedInput
	}

	// Execute
	result, err := tool.Call(input, toolCtx, canUse, nil)
	if err != nil {
		return []types.Message{makeErrorResult(block.ID, fmt.Sprintf("tool error: %v", err))}, nil
	}

	msgs := []types.Message{makeToolResultMessage(block.ID, result)}
	msgs = append(msgs, result.NewMessages...)
	return msgs, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Message factories
// ─────────────────────────────────────────────────────────────────────────────

func makeToolResultMessage(toolUseID string, result ToolResult) types.Message {
	content := fmt.Sprintf("%v", result.Data)
	if b, err := json.Marshal(result.Data); err == nil {
		// Use JSON string for non-string types to preserve structure
		if result.Data != nil {
			switch result.Data.(type) {
			case string:
				content = result.Data.(string)
			default:
				content = string(b)
			}
		}
	}

	block := &types.ToolResultBlock{
		Type:      types.ContentTypeToolResult,
		ToolUseID: toolUseID,
		Content:   content,
		IsError:   result.IsError,
	}

	return &types.UserMessage{
		Type:      types.MessageTypeUser,
		UUID:      uuid.New().String(),
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Msg: types.UserContent{
			Role:    "user",
			Content: types.RawContent{Blocks: []types.ContentBlock{block}},
		},
	}
}

func makeErrorResult(toolUseID, errMsg string) types.Message {
	return makeToolResultMessage(toolUseID, ToolResult{
		Data:    errMsg,
		IsError: true,
	})
}
