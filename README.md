# go-claude-go

A Go implementation of the core agent engine from [Claude Code](https://github.com/anthropics/claude-code), reverse-engineered from the leaked TypeScript source.

**27 Go source files · ~4,700 lines · single binary · zero Node.js dependency**

## What this implements

Six core modules, faithfully mirroring the TypeScript architecture:

| Module | Go package | TypeScript source |
|--------|-----------|-------------------|
| **QueryEngine** — stateful conversation manager, one instance per session | `engine/` | `src/QueryEngine.ts` |
| **query() / AgentLoop** — the `while(true)` state machine that drives tool calls | `query/` | `src/query.ts` |
| **Tool Orchestration** — concurrency-partitioned tool dispatch | `tools/` | `src/services/tools/toolOrchestration.ts` |
| **Permission System** — 5-step permission decision chain + interactive CLI | `tools/permissions/` | `src/hooks/useCanUseTool.tsx` |
| **Context Management** — four-layer compaction pipeline | `compact/` + `tools/budgetcompact.go` | `src/services/compact/` |
| **Stop Hooks** — post-response hook framework | `hooks/` | `src/hooks/` |

### Permission System (Phase 2)

Five-step decision chain mirroring the TypeScript `hasPermissionsToUseTool()`:

1. **bypassPermissions** mode → unconditional allow
2. **AlwaysDenyRules** match → deny
3. **AlwaysAllowRules** match → allow
4. Tool `IsReadOnly()` + **dontAsk** / **acceptEdits** mode → allow
5. Otherwise → **interactive CLI prompt** `[y/n/a/N]`

Four permission modes: `default`, `acceptEdits`, `bypassPermissions`, `dontAsk`

Interactive decisions ("always" / "never") are written back to session rules via `SetAppState`, so repeated tool calls don't re-prompt. Non-TTY stdin automatically denies.

### Context Management layers

| Layer | File | Behaviour |
|-------|------|-----------|
| **ToolResultBudget** | `tools/budgetcompact.go` | Caps total tool_result content at 250k chars; truncates oldest large results first. |
| **AutoCompact** | `compact/autocompact.go` | Full conversation summarisation when context exceeds threshold. Circuit breaker after 3 failures. |
| **MicroCompact** | `compact/microcompact.go` | Deduplicates repeated `tool_result` blocks for the same `tool_use_id`. |
| **Snip** | `compact/snip.go` | Pattern-based removal of redundant intermediate Bash/Grep/Glob outputs. |

### Query Loop Control Flow (Phase 3)

| Feature | Behaviour |
|---------|-----------|
| **max_tokens recovery** | Detects `stop_reason == "max_tokens"` → injects continuation nudge (up to 3×) → escalates to 64k tokens |
| **Fallback model** | Detects HTTP 529 / SSE `overloaded_error` → switches to `FallbackModel`, strips thinking block signatures |
| **ToolResult.NewMessages** | Side-channel messages from tools forwarded to output (UI) but not added to API history |
| **Stop hooks** | `StopHookFn` runs after terminal responses; can trigger one more API round-trip |
| **Permission denial tracking** | Every `PermBlock` recorded in `QueryEngine.PermissionDenials()` audit log |

### Built-in tools

| Tool | File | Description |
|------|------|-------------|
| **Bash** | `tools/bash.go` | Executes shell commands via `bash -c`. Read-only commands are concurrency-safe. |
| **Read** | `tools/read.go` | Reads files with line numbers; supports `offset` and `limit`. |
| **Glob** | `tools/glob.go` | Finds files matching a glob pattern (supports `**` recursive). |
| **Grep** | `tools/grep.go` | Searches file contents with a regular expression. |

---

## Architecture

```
go-claude-go/
├── main.go                  # Demo entry point
├── types/
│   ├── message.go           # Message union + APIError (with IsOverloaded())
│   ├── content.go           # ContentBlock union (Text, ToolUse, ToolResult, Thinking…)
│   └── events.go            # Terminal, StreamDeltaEvent
├── engine/
│   ├── engine.go            # QueryEngine + session state + defaultCanUseTool
│   └── submit.go            # SubmitMessage() + permission denial tracking
├── query/
│   ├── query.go             # Query() entry point + QueryParams (incl. StopHooks)
│   ├── state.go             # State struct + TransitionReason constants
│   └── loop.go              # queryLoop() — max_tokens recovery, fallback, stop hooks
├── api/
│   ├── client.go            # Anthropic HTTP client (SSE) + error type parsing
│   └── stream.go            # SSE assembler + SSE error event handling
├── hooks/
│   └── stop.go              # StopHookFn type definition
├── tools/
│   ├── tool.go              # Tool interface, AppState, PermissionContext, ReadFileState…
│   ├── registry.go          # Tool registry (name → Tool)
│   ├── orchestration.go     # RunTools(): partitioning + side-message forwarding
│   ├── budgetcompact.go     # ApplyToolResultBudget(): 250k char cap
│   ├── globmatch.go         # Glob-to-regex engine (supports **)
│   ├── bash.go / read.go / glob.go / grep.go
│   ├── permissions/
│   │   ├── permissions.go   # 5-step decision chain (HasPermissionsToUseTool)
│   │   └── interactive.go   # CLI prompt [y/n/a/N] + rule write-back
│   └── tools_test.go
└── compact/
    ├── autocompact.go       # AutoCompact + token estimation + circuit breaker
    ├── microcompact.go      # MicroCompact: dedup tool_result by tool_use_id
    └── snip.go              # Snip: remove redundant intermediate tool outputs
```

### Key design mappings: TypeScript → Go

| TypeScript pattern | Go equivalent |
|--------------------|---------------|
| `async function*` (AsyncGenerator) | `chan types.Message` + goroutine |
| `while (true)` + `{ ...state, field: val }` | `for {}` loop with explicit `State` struct |
| `Promise.all()` for concurrent tools | `sync.WaitGroup` + goroutines |
| `AbortController` / `AbortSignal` | `context.WithCancel` (per-turn) |
| React `setState(fn)` for AppState | `SetAppState(func(AppState) AppState)` callback |
| `hasPermissionsToUseTool()` hook | `permissions.HasPermissionsToUseTool()` 5-step chain |
| Zod schema (`z.object(...)`) | `map[string]interface{}` JSON Schema |
| `FallbackTriggeredError` | `APIError.IsOverloaded()` → model switch |

---

## Usage

### Run the demo

```bash
git clone https://github.com/anthropics/claude-code
cd claude-code/go-claude-go
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
        APIKey:        "sk-ant-...",
        Model:         "claude-sonnet-4-6",
        FallbackModel: "claude-haiku-4-5-20251001", // optional
        CWD:           "/your/project",
        MaxTurns:      10,
        SystemPrompt:  "You are a helpful coding assistant.",
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

    // Check permission denials
    for _, d := range qe.PermissionDenials() {
        fmt.Printf("denied: %s — %s\n", d.ToolName, d.Reason)
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
        ├─ tools.ApplyToolResultBudget()       ← 250k char cap
        ├─ compact.ApplySnipIfNeeded()
        ├─ compact.ApplyMicroCompact()
        ├─ compact.AutoCompactIfNeeded()       ← if > threshold
        │
        ├─ api.StreamMessage()                 ← POST /v1/messages (SSE)
        │    └─ yield AssistantMessage
        │
        ├─ error? ─┬─ 413 → reactive compact + retry
        │           └─ 529 → switch to FallbackModel + retry
        │
        ├─ stop_reason == "max_tokens"?
        │    ├─ count < 3 → inject nudge + retry
        │    └─ count ≥ 3 → escalate to 64k tokens
        │
        ├─ no tool_use? ─┬─ run StopHooks
        │                 ├─ ShouldRetry? → continue
        │                 └─ return Terminal{Reason: "completed"}
        │
        ├─ permissions.HasPermissionsToUseTool()
        │    └─ PermAsk? → PromptForPermission() [y/n/a/N]
        │
        ├─ tools.RunTools()
        │    ├─ partitionByConcurrency()
        │    ├─ concurrent batch → goroutines (WaitGroup)
        │    ├─ serial batch    → sequential
        │    └─ sideMessages → outCh (UI only)
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

No Anthropic SDK dependency — the API client is implemented directly using `net/http` and SSE parsing.

---

## Status

| Component | Status |
|-----------|--------|
| QueryEngine + session state | ✅ Complete |
| query() / AgentLoop (max_tokens, fallback, hooks) | ✅ Complete |
| Permission system (5-step chain + interactive CLI) | ✅ Complete |
| Tool concurrency partitioning + side messages | ✅ Complete |
| Tool-result budget compaction | ✅ Complete |
| AutoCompact / MicroCompact / Snip | ✅ Complete |
| Bash / Read / Glob / Grep tools | ✅ Complete |
| SSE streaming + error event handling | ✅ Complete |
| Thinking blocks | ✅ Parsed + stripped on model switch |
| Write / Edit / MultiEdit tools | 🔲 Phase 4 |
| Session persistence (JSONL) | 🔲 Phase 5 |
| MCP tool support (stdio JSON-RPC) | 🔲 Phase 6 |
| Agent / Coordinator-Worker | 🔲 Phase 7 |

---

## License

This project is an educational reimplementation for research purposes. The original Claude Code source is proprietary to Anthropic.
