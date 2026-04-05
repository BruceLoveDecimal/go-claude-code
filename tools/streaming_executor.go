package tools

import (
	"sync"

	"github.com/claude-code/go-claude-go/hooks"
	"github.com/claude-code/go-claude-go/types"
)

// StreamingToolExecutor starts executing tools as their input_json is fully
// received during model streaming, rather than waiting for the full response.
// This reduces end-to-end latency by overlapping model output with tool I/O.
//
// Usage:
//  1. Create with NewStreamingToolExecutor before starting the stream.
//  2. Call OnBlockComplete(block) each time a content_block_stop event fires
//     for a tool_use block (i.e. its input JSON is fully accumulated).
//  3. After the stream finishes, call Finish() to wait for all in-flight
//     tools and collect their results.
type StreamingToolExecutor struct {
	canUse    CanUseToolFn
	toolCtx   ToolContext
	preHooks  []hooks.PreToolHookFn
	postHooks []hooks.PostToolHookFn

	mu       sync.Mutex
	wg       sync.WaitGroup
	results  map[string]executorResult // keyed by tool_use ID
	order    []string                  // insertion order for deterministic output
}

type executorResult struct {
	toolResult []types.Message // [0]=tool_result, [1:]=side messages
	err        error
}

// NewStreamingToolExecutor creates an executor bound to the given context.
func NewStreamingToolExecutor(
	canUse CanUseToolFn,
	toolCtx ToolContext,
	preHooks []hooks.PreToolHookFn,
	postHooks []hooks.PostToolHookFn,
) *StreamingToolExecutor {
	return &StreamingToolExecutor{
		canUse:    canUse,
		toolCtx:   toolCtx,
		preHooks:  preHooks,
		postHooks: postHooks,
		results:   make(map[string]executorResult),
	}
}

// OnBlockComplete is called when a tool_use block's input JSON is fully
// accumulated (content_block_stop event).  If the tool is concurrency-safe,
// execution starts immediately in a goroutine.  Otherwise the block is
// queued for sequential execution in Finish().
func (e *StreamingToolExecutor) OnBlockComplete(block *types.ToolUseBlock) {
	tool, ok := e.toolCtx.Registry.Get(block.Name)
	input, _ := block.InputMap()
	safe := ok && tool.IsConcurrencySafe(input)

	e.mu.Lock()
	e.order = append(e.order, block.ID)
	e.mu.Unlock()

	if safe {
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			msgs, err := runSingleToolInternal(block, e.canUse, e.toolCtx, e.preHooks, e.postHooks)
			e.mu.Lock()
			e.results[block.ID] = executorResult{toolResult: msgs, err: err}
			e.mu.Unlock()
		}()
	}
	// Non-safe blocks will be executed in Finish() below.
}

// Finish waits for all in-flight concurrent tools, then executes any
// remaining non-concurrent tools sequentially.  Returns the combined
// tool results and side messages in submission order.
func (e *StreamingToolExecutor) Finish(blocks []*types.ToolUseBlock) (toolResults []types.Message, sideMessages []types.Message, err error) {
	// Wait for concurrent tools to complete.
	e.wg.Wait()

	// Execute any blocks that were NOT started concurrently.
	for _, block := range blocks {
		e.mu.Lock()
		_, done := e.results[block.ID]
		e.mu.Unlock()

		if !done {
			msgs, runErr := runSingleToolInternal(block, e.canUse, e.toolCtx, e.preHooks, e.postHooks)
			e.mu.Lock()
			e.results[block.ID] = executorResult{toolResult: msgs, err: runErr}
			e.mu.Unlock()
		}
	}

	// Collect results in original order.
	for _, block := range blocks {
		e.mu.Lock()
		res := e.results[block.ID]
		e.mu.Unlock()

		if res.err != nil {
			return nil, nil, res.err
		}
		if len(res.toolResult) > 0 {
			toolResults = append(toolResults, res.toolResult[0])
			sideMessages = append(sideMessages, res.toolResult[1:]...)
		}
	}

	return toolResults, sideMessages, nil
}

// runSingleToolInternal is a package-internal wrapper around runSingleTool
// (from orchestration.go) that StreamingToolExecutor can call.
func runSingleToolInternal(
	block *types.ToolUseBlock,
	canUse CanUseToolFn,
	toolCtx ToolContext,
	preHooks []hooks.PreToolHookFn,
	postHooks []hooks.PostToolHookFn,
) ([]types.Message, error) {
	return runSingleTool(block, canUse, toolCtx, preHooks, postHooks)
}
