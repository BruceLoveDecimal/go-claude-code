# go-claude-go

A Go implementation of the core agent engine from [Claude Code](https://github.com/anthropics/claude-code), reverse-engineered from the leaked TypeScript source.

## What this implements

Four core modules, faithfully mirroring the TypeScript architecture:

| Module | Go package | TypeScript source |
|--------|-----------|-------------------|
| **QueryEngine** — stateful conversation manager, one instance per session | `engine/` | `src/QueryEngine.ts` |
| **query() / AgentLoop** — the `while(true)` state machine that drives tool calls | `query/` | `src/query.ts` |
| **Tool Orchestration** — concurrency-partitioned tool dispatch (concurrent-safe batches run in parallel, write tools run serially) | `tools/` | `src/services/tools/toolOrchestration.ts` |
| **Context Management** — three-layer compaction pipeline | `compact/` | `src/services/compact/` |

### Context Management layers

| Layer | File | Behaviour |
|-------|------|-----------|
| **AutoCompact** | `compact/autocompact.go` | Full conversation summarisation when context exceeds threshold (`effectiveWindow - 13k`). Includes circuit breaker (stops after 3 consecutive failures). |
| **MicroCompact** | `compact/microcompact.go` | Deduplicates repeated `tool_result` blocks for the same `tool_use_id` (prompt-cache aware). |
| **Snip** | `compact/snip.go` | Pattern-based removal of redundant intermediate Bash/Grep/Glob outputs when later results supersede them. |

### Built-in tools

| Tool | File | Description |
|------|------|-------------|
| **Bash** | `tools/bash.go` | Executes shell commands via `bash -c`. Read-only commands (cat, grep, git log…) are concurrency-safe. |
| **Read** | `tools/read.go` | Reads files with line numbers; supports `offset` and `limit`. |
| **Glob** | `tools/glob.go` | Finds files matching a glob pattern. |
| **Grep** | `tools/grep.go` | Searches file contents with a regular expression. |

---

## Architecture

```
go-claude-go/
├── main.go                  # Demo entry point
├── types/
│   ├── message.go           # Message union (UserMessage, AssistantMessage, SystemMessage, …)
│   ├── content.go           # ContentBlock union (TextBlock, ToolUseBlock, ToolResultBlock, …)
│   └── events.go            # Terminal, StreamDeltaEvent
├── engine/
│   ├── engine.go            # QueryEngine struct + Config
│   └── submit.go            # SubmitMessage() — goroutine-based message generator
├── query/
│   ├── query.go             # Query() entry point + QueryParams
│   ├── state.go             # State struct + TransitionReason constants
│   └── loop.go              # queryLoop() — the core for{} state machine
├── api/
│   ├── client.go            # Anthropic API HTTP client (SSE streaming)
│   └── stream.go            # SSE assembler: deltas → AssistantMessage
├── tools/
│   ├── tool.go              # Tool interface + PermissionResult + ToolResult
│   ├── registry.go          # Tool registry (name → Tool)
│   ├── orchestration.go     # RunTools(): partition by concurrency + dispatch
│   ├── bash.go              # BashTool
│   ├── read.go              # ReadTool
│   ├── glob.go              # GlobTool
│   └── grep.go              # GrepTool
└── compact/
    ├── autocompact.go       # AutoCompact + token estimation + circuit breaker
    ├── microcompact.go      # MicroCompact: dedup tool_result by tool_use_id
    └── snip.go              # Snip: remove redundant intermediate tool outputs
```

### Key design mappings: TypeScript → Go

| TypeScript pattern | Go equivalent |
|--------------------|---------------|
| `async function*` (AsyncGenerator) | `chan types.Message` + goroutine |
| `while (true)` state machine with `{ ...state, field: val }` | `for {}` loop with explicit `State` struct assignment |
| `Promise.all()` for concurrent tools | `sync.WaitGroup` + goroutines |
| `AbortController` / `AbortSignal` | `context.Context` cancellation |
| Zod schema (`z.object(...)`) | `map[string]interface{}` JSON Schema |
| `AsyncGenerator<SDKMessage>` from `submitMessage()` | `<-chan types.Message` drained by caller |

---

## Usage

### Run the demo

```bash
git clone https://github.com/BruceLoveDecimal/go-claude-code
cd go-claude-code/go-claude-go
ANTHROPIC_API_KEY=sk-ant-... go run . "list Go source files in this directory"
```

### Embed in your own project

```go
package main

import (
    "context"
    "fmt"

    "github.com/claude-code/go-claude-go/engine"
    "github.com/claude-code/go-claude-go/types"
)

func main() {
    qe := engine.NewQueryEngine(engine.QueryEngineConfig{
        APIKey:       "sk-ant-...",
        Model:        "claude-sonnet-4-6",
        CWD:          "/your/project",
        MaxTurns:     10,
        SystemPrompt: "You are a helpful coding assistant.",
    })

    msgCh, errCh := qe.SubmitMessage(context.Background(),
        "What files are in this directory?")

    for msg := range msgCh {
        if am, ok := msg.(*types.AssistantMessage); ok {
            fmt.Println(am.TextContent())
        }
    }
    if err := <-errCh; err != nil {
        panic(err)
    }
}
```

### Extend with custom tools

```go
type MyTool struct{}

func (t *MyTool) Name() string        { return "MyTool" }
func (t *MyTool) Description() string { return "Does something custom." }
func (t *MyTool) IsEnabled() bool     { return true }
func (t *MyTool) IsConcurrencySafe(input map[string]interface{}) bool { return true }
func (t *MyTool) IsReadOnly(input map[string]interface{}) bool        { return true }
func (t *MyTool) MaxResultSizeChars() int { return 10_000 }
func (t *MyTool) InputSchema() map[string]interface{} {
    return map[string]interface{}{
        "type": "object",
        "properties": map[string]interface{}{
            "query": map[string]interface{}{"type": "string"},
        },
        "required": []string{"query"},
    }
}
func (t *MyTool) CheckPermissions(input map[string]interface{}, ctx tools.ToolContext) (tools.PermissionResult, error) {
    return tools.PermissionResult{Behavior: tools.PermAllow, UpdatedInput: input}, nil
}
func (t *MyTool) Call(input map[string]interface{}, ctx tools.ToolContext, canUse tools.CanUseToolFn, progress chan<- interface{}) (tools.ToolResult, error) {
    return tools.ToolResult{Data: "result of " + input["query"].(string)}, nil
}

// Register it:
registry := tools.DefaultRegistry()
registry.Register(&MyTool{})
```

---

## Agent loop flow

```
SubmitMessage(prompt)
  │
  ├─ Append UserMessage to history
  │
  └─ query.Query() ──► for {
        │
        ├─ GetMessagesAfterCompactBoundary()
        ├─ compact.ApplySnipIfNeeded()
        ├─ compact.ApplyMicroCompact()
        ├─ compact.AutoCompactIfNeeded()   ← if > threshold
        │
        ├─ api.StreamMessage()             ← POST /v1/messages (SSE)
        │    └─ yield AssistantMessage
        │
        ├─ no tool_use? ──► return Terminal{Reason: "completed"}
        │
        ├─ tools.RunTools()
        │    ├─ partitionByConcurrency()
        │    ├─ concurrent batch ──► goroutines (WaitGroup)
        │    └─ serial batch    ──► sequential
        │
        └─ state.Messages += [assistant, tool_results]
           state.TurnCount++
           continue
     }
```

---

## Dependencies

```
github.com/google/uuid v1.6.0
```

No Anthropic SDK dependency — the API client is implemented directly using `net/http` and SSE parsing, giving full control over streaming behaviour.

---

## Status

| Component | Status |
|-----------|--------|
| QueryEngine | ✅ Complete |
| query() / AgentLoop | ✅ Complete |
| Tool concurrency partitioning | ✅ Complete |
| AutoCompact (circuit breaker) | ✅ Complete |
| MicroCompact | ✅ Complete |
| Snip | ✅ Complete |
| Bash / Read / Glob / Grep tools | ✅ Complete |
| SSE streaming parser | ✅ Complete |
| Thinking blocks | ✅ Parsed |
| MCP tool support | 🔲 Not implemented |
| Session persistence (JSONL) | 🔲 Not implemented |
| Permission modal (interactive) | 🔲 Not implemented |
