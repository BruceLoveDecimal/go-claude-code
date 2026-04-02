# go-claude-go

基于 Claude Code泄露的 TypeScript 源码逆向重写的 Go 核心引擎。

**27 个 Go 源文件 · 约 4,700 行代码 · 单二进制 · 零 Node.js 依赖**

## 已实现模块

六大核心模块，忠实映射 TypeScript 架构：

| 模块 | Go 包 | TypeScript 来源 |
|------|------|----------------|
| **QueryEngine** — 有状态会话管理器，每个对话一个实例 | `engine/` | `src/QueryEngine.ts` |
| **query() / AgentLoop** — `while(true)` 状态机驱动工具调用 | `query/` | `src/query.ts` |
| **工具编排** — 基于并发安全性分批调度 | `tools/` | `src/services/tools/toolOrchestration.ts` |
| **权限系统** — 5 步权限决策链 + 交互式 CLI | `tools/permissions/` | `src/hooks/useCanUseTool.tsx` |
| **上下文管理** — 四层压缩管线 | `compact/` + `tools/budgetcompact.go` | `src/services/compact/` |
| **Stop Hooks** — 响应后钩子框架 | `hooks/` | `src/hooks/` |

### 权限系统 (Phase 2)

五步决策链，映射 TypeScript `hasPermissionsToUseTool()`：

1. **bypassPermissions** 模式 → 无条件允许
2. **AlwaysDenyRules** 匹配 → 拒绝
3. **AlwaysAllowRules** 匹配 → 允许
4. 工具 `IsReadOnly()` + **dontAsk** / **acceptEdits** 模式 → 允许
5. 其他 → **交互式 CLI 提示** `[y/n/a/N]`

四种权限模式：`default`、`acceptEdits`、`bypassPermissions`、`dontAsk`

交互式决策（"always" / "never"）通过 `SetAppState` 回写到会话规则，后续相同工具调用不再重复提示。非 TTY 环境自动拒绝。

### 上下文管理层

| 层级 | 文件 | 行为 |
|-----|------|-----|
| **工具结果预算** | `tools/budgetcompact.go` | 工具输出总量上限 250k 字符；优先截断最早的大型结果 |
| **AutoCompact** | `compact/autocompact.go` | 上下文超过阈值时全量摘要压缩，含断路器（连续 3 次失败后停止） |
| **MicroCompact** | `compact/microcompact.go` | 去重相同 `tool_use_id` 的重复 `tool_result` 块 |
| **Snip** | `compact/snip.go` | 模式匹配移除冗余的中间 Bash/Grep/Glob 输出 |

### Query Loop 控制流 (Phase 3)

| 特性 | 行为 |
|-----|------|
| **max_tokens 恢复** | 检测 `stop_reason == "max_tokens"` → 注入续写 nudge（最多 3 次）→ 升级到 64k tokens |
| **Fallback 模型** | 检测 HTTP 529 / SSE `overloaded_error` → 切换 `FallbackModel`，剥离 thinking block 签名 |
| **ToolResult.NewMessages** | 工具侧信道消息转发到输出（UI），不进 API 历史 |
| **Stop Hooks** | `StopHookFn` 在终端响应后执行，可触发额外一轮 API 调用 |
| **权限拒绝追踪** | 每次 `PermBlock` 记录到 `QueryEngine.PermissionDenials()` 审计日志 |

### 内置工具

| 工具 | 文件 | 描述 |
|-----|------|------|
| **Bash** | `tools/bash.go` | 通过 `bash -c` 执行 shell 命令，只读命令支持并发 |
| **Read** | `tools/read.go` | 读取文件（带行号），支持 `offset` 和 `limit` |
| **Glob** | `tools/glob.go` | 按 glob 模式匹配文件（支持 `**` 递归） |
| **Grep** | `tools/grep.go` | 正则表达式搜索文件内容 |

---

## 架构

```
go-claude-go/
├── main.go                  # 演示入口
├── types/
│   ├── message.go           # 消息联合类型 + APIError（含 IsOverloaded()）
│   ├── content.go           # ContentBlock 联合类型
│   └── events.go            # Terminal、StreamDeltaEvent
├── engine/
│   ├── engine.go            # QueryEngine + 会话状态 + defaultCanUseTool
│   └── submit.go            # SubmitMessage() + 权限拒绝追踪
├── query/
│   ├── query.go             # Query() 入口 + QueryParams（含 StopHooks）
│   ├── state.go             # State 结构体 + TransitionReason 常量
│   └── loop.go              # queryLoop() — max_tokens 恢复、fallback、stop hooks
├── api/
│   ├── client.go            # Anthropic HTTP 客户端（SSE）+ 错误类型解析
│   └── stream.go            # SSE 组装器 + SSE 错误事件处理
├── hooks/
│   └── stop.go              # StopHookFn 类型定义
├── tools/
│   ├── tool.go              # Tool 接口、AppState、PermissionContext、ReadFileState…
│   ├── registry.go          # 工具注册表（名称 → Tool）
│   ├── orchestration.go     # RunTools()：分批调度 + 侧信道消息转发
│   ├── budgetcompact.go     # ApplyToolResultBudget()：250k 字符上限
│   ├── globmatch.go         # Glob 转正则引擎（支持 **）
│   ├── bash.go / read.go / glob.go / grep.go
│   ├── permissions/
│   │   ├── permissions.go   # 5 步决策链（HasPermissionsToUseTool）
│   │   └── interactive.go   # CLI 提示 [y/n/a/N] + 规则回写
│   └── tools_test.go
└── compact/
    ├── autocompact.go       # AutoCompact + token 估算 + 断路器
    ├── microcompact.go      # MicroCompact：按 tool_use_id 去重
    └── snip.go              # Snip：移除冗余中间工具输出
```

### TypeScript → Go 设计映射

| TypeScript 模式 | Go 等价实现 |
|----------------|------------|
| `async function*`（AsyncGenerator）| `chan types.Message` + goroutine |
| `while (true)` + `{ ...state, field: val }` | `for {}` 循环 + 显式 `State` 结构体赋值 |
| `Promise.all()` 并发工具 | `sync.WaitGroup` + goroutines |
| `AbortController` / `AbortSignal` | `context.WithCancel`（每轮独立） |
| React `setState(fn)` 更新 AppState | `SetAppState(func(AppState) AppState)` 回调 |
| `hasPermissionsToUseTool()` 钩子 | `permissions.HasPermissionsToUseTool()` 五步链 |
| Zod schema (`z.object(...)`) | `map[string]interface{}` JSON Schema |
| `FallbackTriggeredError` | `APIError.IsOverloaded()` → 模型切换 |

---

## 使用方法

### 运行演示

```bash
git clone https://github.com/anthropics/claude-code
cd claude-code/go-claude-go
ANTHROPIC_API_KEY=sk-ant-... go run . "列出当前目录的 Go 源文件"
```

### 嵌入到你的项目

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
        FallbackModel: "claude-haiku-4-5-20251001", // 可选
        CWD:           "/your/project",
        MaxTurns:      10,
        SystemPrompt:  "你是一个有用的编程助手。",
    })

    msgCh, errCh := qe.SubmitMessage(context.Background(),
        "这个目录下有什么文件？")

    for msg := range msgCh {
        if am, ok := msg.(*types.AssistantMessage); ok {
            fmt.Println(am.TextContent())
        }
    }
    if err := <-errCh; err != nil {
        panic(err)
    }

    // 查看权限拒绝记录
    for _, d := range qe.PermissionDenials() {
        fmt.Printf("被拒绝: %s — %s\n", d.ToolName, d.Reason)
    }
}
```

### 扩展自定义工具

```go
type MyTool struct{}

func (t *MyTool) Name() string        { return "MyTool" }
func (t *MyTool) Description() string { return "执行自定义操作。" }
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
    return tools.ToolResult{Data: "结果: " + input["query"].(string)}, nil
}

// 注册:
registry := tools.DefaultRegistry()
registry.Register(&MyTool{})
```

---

## Agent 循环流程

```
SubmitMessage(prompt)
  │
  ├─ 追加 UserMessage 到历史
  │
  └─ query.Query() ──► for {
        │
        ├─ GetMessagesAfterCompactBoundary()
        ├─ tools.ApplyToolResultBudget()       ← 250k 字符上限
        ├─ compact.ApplySnipIfNeeded()
        ├─ compact.ApplyMicroCompact()
        ├─ compact.AutoCompactIfNeeded()       ← 超过阈值时触发
        │
        ├─ api.StreamMessage()                 ← POST /v1/messages (SSE)
        │    └─ 产出 AssistantMessage
        │
        ├─ 错误? ─┬─ 413 → 反应式压缩 + 重试
        │          └─ 529 → 切换到 FallbackModel + 重试
        │
        ├─ stop_reason == "max_tokens"?
        │    ├─ 次数 < 3 → 注入续写 nudge + 重试
        │    └─ 次数 ≥ 3 → 升级到 64k tokens
        │
        ├─ 无 tool_use? ─┬─ 执行 StopHooks
        │                  ├─ ShouldRetry? → 继续循环
        │                  └─ 返回 Terminal{Reason: "completed"}
        │
        ├─ permissions.HasPermissionsToUseTool()
        │    └─ PermAsk? → PromptForPermission() [y/n/a/N]
        │
        ├─ tools.RunTools()
        │    ├─ partitionByConcurrency()
        │    ├─ 并发批次 → goroutines (WaitGroup)
        │    ├─ 串行批次 → 顺序执行
        │    └─ sideMessages → outCh（仅 UI 展示）
        │
        └─ state.Messages += [assistant, tool_results]
           state.TurnCount++
           continue
     }
```

---

## 依赖

```
github.com/google/uuid v1.6.0
```

无 Anthropic SDK 依赖 — API 客户端直接用 `net/http` + SSE 解析实现。

---

## 进度

| 组件 | 状态 |
|-----|------|
| QueryEngine + 会话状态 | ✅ 完成 |
| query() / AgentLoop（max_tokens、fallback、hooks）| ✅ 完成 |
| 权限系统（5 步决策链 + 交互式 CLI）| ✅ 完成 |
| 工具并发分批 + 侧信道消息 | ✅ 完成 |
| 工具结果预算压缩 | ✅ 完成 |
| AutoCompact / MicroCompact / Snip | ✅ 完成 |
| Bash / Read / Glob / Grep 工具 | ✅ 完成 |
| SSE 流式解析 + 错误事件处理 | ✅ 完成 |
| Thinking blocks | ✅ 已解析 + 模型切换时剥离 |
| Write / Edit / MultiEdit 工具 | 🔲 Phase 4 |
| 会话持久化（JSONL）| 🔲 Phase 5 |
| MCP 工具支持（stdio JSON-RPC）| 🔲 Phase 6 |
| Agent / Coordinator-Worker | 🔲 Phase 7 |

---

## 许可

本项目为教育研究目的的逆向重新实现。原始 Claude Code 源码为 Anthropic 所有。
