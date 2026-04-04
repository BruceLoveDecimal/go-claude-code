# go-claude-code

A Go Agent SDK modeled on the core engine of Claude Code

**~35 Go source files · ~6,000 lines · single binary · zero Node.js dependency**

## What this is

`go-claude-go` is a **Go Agent SDK** that faithfully reimplements the agentic loop, tool orchestration, permission system, and context management from Claude Code's TypeScript engine. The SDK user writes the application layer (CLI, API server, CI pipeline, Slack bot) — the SDK provides the agent brain.

```go
qe := engine.NewQueryEngine(engine.QueryEngineConfig{
    APIKey: "sk-ant-...",
    Model:  "claude-sonnet-4-6",
    CWD:    "/path/to/project",
})
msgCh, errCh := qe.SubmitMessage(ctx, "Fix the failing test")
for msg := range msgCh { /* stream to user */ }
```
## SDK Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                      User Application                           │
│  (CLI app, API server, CI pipeline, Slack bot, etc.)            │
└──────────────────────────┬──────────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────────────┐
│                    go-claude-go SDK                             │
│                                                                 │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │                  engine.QueryEngine                     │    │
│  │                                                         │    │
│  │  • SubmitMessage(ctx, prompt) → (msgCh, errCh)          │    │
│  │  • GetAppState / SetAppState                            │    │
│  │  • Messages() / TotalUsage()                            │    │
│  │  • SessionID() / Resume from session                    │    │
│  └────────┬──────────────────────┬─────────────────────────┘    │
│           │                      │                              │
│     ┌─────▼──────┐        ┌─────▼──────────────┐                │
│     │ query.Loop │        │ tools.RunTools      │               │
│     │            │◄──────►│                     │               │
│     │ • stream   │        │ • permission check  │               │
│     │ • recover  │        │ • concurrent batch  │               │
│     │ • compact  │        │ • serial dispatch   │               │
│     └─────┬──────┘        └────────┬────────────┘               │
│           │                        │                            │
│  ┌────────▼────────────────────────▼───────────────────────┐    │
│  │              Infrastructure Layer                       │    │
│  │                                                         │    │
│  │  ┌──────────┐ ┌───────────┐ ┌──────────┐ ┌──────────┐   │    │
│  │  │ api/     │ │ compact/  │ │ session/ │ │ hooks/   │   │    │
│  │  │          │ │           │ │          │ │          │   │    │
│  │  │ • Client │ │ • Snip    │ │ • JSONL  │ │ • Stop   │   │    │
│  │  │ • Stream │ │ • Micro   │ │ • Load   │ │          │   │    │
│  │  │          │ │ • Auto    │ │ • Resume │ │          │   │    │
│  │  │          │ │ • Budget  │ │          │ │          │   │    │
│  │  └──────────┘ └───────────┘ └──────────┘ └──────────┘   │    │
│  └─────────────────────────────────────────────────────────┘    │
│                                                                 │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │                    Tool Layer                           │    │
│  │                                                         │    │
│  │  ┌─────────────────────────┐  ┌──────────────────────┐  │    │
│  │  │  Built-in Tools (14)    │  │  Extension Points    │  │    │
│  │  │                         │  │                      │  │    │
│  │  │  Bash Read Glob Grep    │  │  • Tool interface    │  │    │
│  │  │  Write Edit MultiEdit   │  │  • Registry.Register │  │    │
│  │  │  LS WebFetch Todo×2     │  │  • MCP auto-import   │  │    │
│  │  │  Agent SendMessage      │  │  • Custom CanUseTool │  │    │
│  │  └─────────────────────────┘  └──────────────────────┘  │    │
│  │                                                         │    │
│  │  ┌─────────────────────────┐  ┌──────────────────────┐  │    │
│  │  │  permissions/           │  │  mcp/                │  │    │
│  │  │                         │  │                      │  │    │
│  │  │  • 5-step chain         │  │  • StdioMCPClient    │  │    │
│  │  │  • Rule matching        │  │  • JSON-RPC 2.0      │  │    │
│  │  │  • Interactive prompt   │  │  • Tool wrapper      │  │    │
│  │  └─────────────────────────┘  └──────────────────────┘  │    │
│  └─────────────────────────────────────────────────────────┘    │
│                                                                 │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │                types/  (wire format)                    │    │
│  │                                                         │    │
│  │  Message, ContentBlock, SDKMessage, Usage, APIError     │    │
│  │  Marshal/Unmarshal (polymorphic JSON)                   │    │
│  └─────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────┘
```

## What this implements

Nine core modules, faithfully mirroring the TypeScript architecture:

| Module | Go package | TypeScript source |
|--------|-----------|-------------------|
| **QueryEngine** — stateful conversation manager, one instance per session | `engine/` | `src/QueryEngine.ts` |
| **query() / AgentLoop** — the `while(true)` state machine that drives tool calls | `query/` | `src/query.ts` |
| **Tool Orchestration** — concurrency-partitioned tool dispatch | `tools/` | `src/services/tools/toolOrchestration.ts` |
| **Permission System** — 5-step permission decision chain + interactive CLI | `tools/permissions/` | `src/hooks/useCanUseTool.tsx` |
| **Context Management** — four-layer compaction pipeline | `compact/` + `tools/budgetcompact.go` | `src/services/compact/` |
| **Stop Hooks** — post-response hook framework | `hooks/` | `src/hooks/` |
| **Session Persistence** — JSONL conversation history with resume | `session/` | `src/utils/sessionStorage.ts` |
| **MCP Client** — stdio JSON-RPC 2.0 with dynamic tool registration | `mcp/` | `src/services/mcp/` |
| **Agent / Subagent** — nested agentic loops with coordinator pattern | `engine/agent.go` + `tools/agent.go` | `src/tools/AgentTool/` |

### Permission System

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

### Query Loop Control Flow

| Feature | Behaviour |
|---------|-----------|
| **max_tokens recovery** | Detects `stop_reason == "max_tokens"` → injects continuation nudge (up to 3×) → escalates to 64k tokens |
| **Fallback model** | Detects HTTP 529 / SSE `overloaded_error` → switches to `FallbackModel`, strips thinking block signatures |
| **ToolResult.NewMessages** | Side-channel messages from tools forwarded to output (UI) but not added to API history |
| **Stop hooks** | `StopHookFn` runs after terminal responses; can trigger one more API round-trip |
| **Permission denial tracking** | Every `PermBlock` recorded in `QueryEngine.PermissionDenials()` audit log |

### Built-in tools (14)

| Tool | File | Description |
|------|------|-------------|
| **Bash** | `tools/bash.go` | Executes shell commands via `bash -c`. Read-only commands are concurrency-safe. |
| **Read** | `tools/read.go` | Reads files with line numbers; supports `offset` and `limit`. |
| **Glob** | `tools/glob.go` | Finds files matching a glob pattern (supports `**` recursive). |
| **Grep** | `tools/grep.go` | Searches file contents with a regular expression. |
| **LS** | `tools/ls.go` | Lists directory contents as a tree; supports ignore patterns. |
| **WebFetch** | `tools/webfetch.go` | Fetches URLs and extracts plain text from HTML. |
| **Write** | `tools/write.go` | Creates or overwrites files; updates ReadFileState cache. |
| **Edit** | `tools/edit.go` | Exact string replacement; supports `replace_all`. |
| **MultiEdit** | `tools/multiedit.go` | Sequential batch edits in a single file. |
| **TodoRead** | `tools/todo.go` | Reads session-scoped task list from AppState. |
| **TodoWrite** | `tools/todo.go` | Replaces session-scoped task list in AppState. |
| **Agent** | `tools/agent.go` | Spawns a subagent with independent query loop. |
| **SendMessage** | `tools/agent.go` | Sends a follow-up prompt to a running subagent. |
| **MCP tools** | `tools/mcp_tool.go` | Dynamically registered from MCP servers. |

---
### What is NOT in the SDK (application-layer concerns)

TUI/terminal rendering, slash commands, IDE bridge, voice input, telemetry, plugin system, settings UI. These belong to the application you build on top of the SDK.

### Package layout

```
go-claude-go/
├── main.go                  # Demo entry point
├── types/
│   ├── message.go           # Message union + APIError (with IsOverloaded())
│   ├── content.go           # ContentBlock union (Text, ToolUse, ToolResult, Thinking…)
│   ├── events.go            # Terminal, StreamDeltaEvent
│   └── marshal.go           # Polymorphic JSON marshal/unmarshal
├── engine/
│   ├── engine.go            # QueryEngine + session state + MCP registration
│   ├── submit.go            # SubmitMessage() + permission denial tracking
│   └── agent.go             # Subagent runner (creates child query loops)
├── query/
│   ├── query.go             # Query() entry point + QueryParams
│   ├── state.go             # State struct + TransitionReason constants
│   └── loop.go              # queryLoop() — max_tokens, fallback, stop hooks
├── api/
│   ├── client.go            # Anthropic HTTP client (SSE)
│   └── stream.go            # SSE assembler + error event handling
├── hooks/
│   └── stop.go              # StopHookFn type definition
├── mcp/
│   ├── types.go             # MCPTool, MCPResource, MCPContent, MCPServerConfig
│   └── client.go            # StdioMCPClient: JSON-RPC 2.0 over stdio
├── session/
│   ├── session.go           # SessionMeta, SessionFilePath, NewSessionID
│   └── persist.go           # AppendMessages, LoadSession, ListSessions
├── tools/
│   ├── tool.go              # Tool interface, AppState, PermissionContext…
│   ├── registry.go          # Tool registry (name → Tool)
│   ├── orchestration.go     # RunTools(): partition + dispatch + side messages
│   ├── budgetcompact.go     # ApplyToolResultBudget(): 250k char cap
│   ├── agent.go             # AgentTool + SendMessageTool
│   ├── agent_registry.go    # AgentRegistry for subagent coordination
│   ├── mcp_tool.go          # MCPToolWrapper: adapts MCP tools to Tool interface
│   ├── bash.go / read.go / glob.go / grep.go / ls.go / webfetch.go
│   ├── write.go / edit.go / multiedit.go / todo.go
│   ├── permissions/
│   │   ├── permissions.go   # 5-step decision chain
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
| Bash / Read / Glob / Grep / LS / WebFetch tools | ✅ Complete |
| Write / Edit / MultiEdit tools | ✅ Complete |
| TodoRead / TodoWrite tools | ✅ Complete |
| SSE streaming + error event handling | ✅ Complete |
| Thinking blocks | ✅ Parsed + stripped on model switch |
| Session persistence (JSONL) + resume | ✅ Complete |
| MCP client (stdio JSON-RPC) + tool wrapper | ✅ Complete |
| Agent / SendMessage + subagent coordination | ✅ Complete |

---

## Roadmap

The SDK is functional today. Below are the three priority tiers for reaching production-grade quality.

### P0 — Agent behavior correctness

| Item | Description |
|------|-------------|
| **System prompt builder** | Sectioned builder with environment detection (OS, shell, CWD, git branch, date), dynamic tool description injection, and CLAUDE.md project directive loading. This is the single highest-impact missing piece — without it the model has no environmental awareness. |
| **API retry** | Exponential backoff for 429/529/5xx with configurable `maxRetries` and jitter. Without retry the SDK is unusable under real API load. |
| **Bash safety classifier** | Dangerous command pattern library (rm -rf, DROP TABLE, git push --force, etc.) that acts as a safety net even under `bypassPermissions` mode. |

### P1 — Agent interaction quality

| Item | Description |
|------|-------------|
| **Hooks expansion** | `PreToolHook`, `PostToolHook`, `MessageHook` with unified `HookFn` interface — the core extension mechanism for SDK users to inject audit, filtering, or rewriting logic without modifying engine code. |
| **AskUserQuestion tool** | Via `QueryEngineConfig.UserInputFn` callback. SDK users inject their own IO implementation (CLI stdin, web form, Slack bot, etc.). Without this, the model cannot ask for clarification. |
| **Token estimation + budget** | Accurate token counting, pre-request budget check, proactive compact trigger instead of waiting for 413 errors. |
| **CLAUDE.md + project context** | Recursive search for `.claude/CLAUDE.md` and `CLAUDE.md` from CWD up to git root, merged into system prompt. |

### P2 — Ecosystem

| Item | Description |
|------|-------------|
| **MCP SSE/HTTP transport** | Support for remote MCP servers (GitHub MCP, Slack MCP, etc.) beyond stdio. |
| **Streaming callback API** | `QueryEngineConfig.OnMessage func(SDKMessage)` callback + `engine.RunSync()` convenience method for simple use cases. |
| **Structured logging** | `slog.Logger` integration for full observability: API calls, tool execution, permission decisions, compact triggers. |
| **Test coverage** | Unit tests for core paths: query loop (mock API), compact pipeline, permission chain, session persistence, MCP client. |
| **Go module publication** | Proper module path, semantic versioning, godoc comments, example directory. |

---

## License

This project is an educational reimplementation for research purposes. The original Claude Code source is proprietary to Anthropic.
