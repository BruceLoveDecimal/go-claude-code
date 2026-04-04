# go-claude-go

基于 Claude Code逆向代码为核心引擎的 Go Agent SDK。
**约 35 个 Go 源文件 · 约 6,000 行代码 · 单二进制 · 零 Node.js 依赖**

## 这是什么

`go-claude-go` 是一个 **Go Agent SDK**，忠实重现了 Claude Code TypeScript 引擎中的代理循环、工具编排、权限系统和上下文管理。SDK 用户编写应用层（CLI、API 服务、CI 流水线、Slack 机器人）——SDK 提供 Agent 大脑。

```go
qe := engine.NewQueryEngine(engine.QueryEngineConfig{
    APIKey: "sk-ant-...",
    Model:  "claude-sonnet-4-6",
    CWD:    "/path/to/project",
})
msgCh, errCh := qe.SubmitMessage(ctx, "修复失败的测试")
for msg := range msgCh { /* 流式输出给用户 */ }
```

## 已实现模块

九大核心模块，忠实映射 TypeScript 架构：

| 模块 | Go 包 | TypeScript 来源 |
|------|------|----------------|
| **QueryEngine** — 有状态会话管理器，每个对话一个实例 | `engine/` | `src/QueryEngine.ts` |
| **query() / AgentLoop** — `while(true)` 状态机驱动工具调用 | `query/` | `src/query.ts` |
| **工具编排** — 基于并发安全性分批调度 | `tools/` | `src/services/tools/toolOrchestration.ts` |
| **权限系统** — 5 步权限决策链 + 交互式 CLI | `tools/permissions/` | `src/hooks/useCanUseTool.tsx` |
| **上下文管理** — 四层压缩管线 | `compact/` + `tools/budgetcompact.go` | `src/services/compact/` |
| **Stop Hooks** — 响应后钩子框架 | `hooks/` | `src/hooks/` |
| **会话持久化** — JSONL 对话历史 + 恢复 | `session/` | `src/utils/sessionStorage.ts` |
| **MCP 客户端** — stdio JSON-RPC 2.0 + 动态工具注册 | `mcp/` | `src/services/mcp/` |
| **Agent / 子代理** — 嵌套代理循环 + 协调器模式 | `engine/agent.go` + `tools/agent.go` | `src/tools/AgentTool/` |

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

### 内置工具（14 个）

| 工具 | 文件 | 描述 |
|-----|------|------|
| **Bash** | `tools/bash.go` | 通过 `bash -c` 执行 shell 命令，只读命令支持并发 |
| **Read** | `tools/read.go` | 读取文件（带行号），支持 `offset` 和 `limit` |
| **Glob** | `tools/glob.go` | 按 glob 模式匹配文件（支持 `**` 递归） |
| **Grep** | `tools/grep.go` | 正则表达式搜索文件内容 |
| **LS** | `tools/ls.go` | 列目录树形结构，支持忽略模式 |
| **WebFetch** | `tools/webfetch.go` | 获取 URL 内容并提取纯文本 |
| **Write** | `tools/write.go` | 创建或覆盖文件，更新 ReadFileState 缓存 |
| **Edit** | `tools/edit.go` | 精确字符串替换，支持 `replace_all` |
| **MultiEdit** | `tools/multiedit.go` | 单文件内的顺序批量编辑 |
| **TodoRead** | `tools/todo.go` | 从 AppState 读取会话级任务列表 |
| **TodoWrite** | `tools/todo.go` | 替换 AppState 中的会话级任务列表 |
| **Agent** | `tools/agent.go` | 启动独立查询循环的子代理 |
| **SendMessage** | `tools/agent.go` | 向运行中的子代理发送追加提示 |
| **MCP 工具** | `tools/mcp_tool.go` | 从 MCP 服务器动态注册 |

---

## SDK 架构

```
┌─────────────────────────────────────────────────────────────────┐
│                        用户应用层                                │
│  （CLI 应用、API 服务、CI 流水线、Slack 机器人等）                 │
└──────────────────────────┬──────────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────────────┐
│                    go-claude-go SDK                              │
│                                                                 │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │                  engine.QueryEngine                      │    │
│  │                                                         │    │
│  │  • SubmitMessage(ctx, prompt) → (msgCh, errCh)          │    │
│  │  • GetAppState / SetAppState                            │    │
│  │  • Messages() / TotalUsage()                            │    │
│  │  • SessionID() / 从会话恢复                               │    │
│  └────────┬──────────────────────┬─────────────────────────┘    │
│           │                      │                              │
│     ┌─────▼──────┐        ┌─────▼──────────────┐               │
│     │ query.Loop │        │ tools.RunTools      │               │
│     │            │◄──────►│                     │               │
│     │ • 流式输出  │        │ • 权限检查           │               │
│     │ • 错误恢复  │        │ • 并发批处理         │               │
│     │ • 上下文压缩│        │ • 串行调度           │               │
│     └─────┬──────┘        └────────┬────────────┘               │
│           │                        │                            │
│  ┌────────▼────────────────────────▼───────────────────────┐    │
│  │                  基础设施层                               │    │
│  │                                                         │    │
│  │  ┌──────────┐ ┌───────────┐ ┌──────────┐ ┌──────────┐  │    │
│  │  │ api/     │ │ compact/  │ │ session/ │ │ hooks/   │  │    │
│  │  │          │ │           │ │          │ │          │  │    │
│  │  │ • Client │ │ • Snip    │ │ • JSONL  │ │ • Stop   │  │    │
│  │  │ • Stream │ │ • Micro   │ │ • 加载    │ │          │  │    │
│  │  │          │ │ • Auto    │ │ • 恢复    │ │          │  │    │
│  │  │          │ │ • Budget  │ │          │ │          │  │    │
│  │  └──────────┘ └───────────┘ └──────────┘ └──────────┘  │    │
│  └─────────────────────────────────────────────────────────┘    │
│                                                                 │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │                     工具层                                │    │
│  │                                                         │    │
│  │  ┌─────────────────────────┐  ┌──────────────────────┐  │    │
│  │  │  内置工具（14 个）        │  │  扩展点              │  │    │
│  │  │                         │  │                      │  │    │
│  │  │  Bash Read Glob Grep   │  │  • Tool 接口          │  │    │
│  │  │  Write Edit MultiEdit  │  │  • Registry.Register  │  │    │
│  │  │  LS WebFetch Todo×2    │  │  • MCP 自动导入       │  │    │
│  │  │  Agent SendMessage     │  │  • 自定义 CanUseTool  │  │    │
│  │  └─────────────────────────┘  └──────────────────────┘  │    │
│  │                                                         │    │
│  │  ┌─────────────────────────┐  ┌──────────────────────┐  │    │
│  │  │  permissions/           │  │  mcp/                │  │    │
│  │  │                         │  │                      │  │    │
│  │  │  • 5 步决策链            │  │  • StdioMCPClient   │  │    │
│  │  │  • 规则匹配             │  │  • JSON-RPC 2.0     │  │    │
│  │  │  • 交互式确认            │  │  • 工具包装器        │  │    │
│  │  └─────────────────────────┘  └──────────────────────┘  │    │
│  └─────────────────────────────────────────────────────────┘    │
│                                                                 │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │                types/（协议格式）                          │    │
│  │                                                         │    │
│  │  Message, ContentBlock, SDKMessage, Usage, APIError      │    │
│  │  Marshal/Unmarshal（多态 JSON）                           │    │
│  └─────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────┘
```

### SDK 不包含的内容（应用层关注点）

TUI / 终端渲染、slash 命令、IDE 集成、语音输入、遥测、插件系统、设置界面。这些属于你基于 SDK 构建的应用。

### 包结构

```
go-claude-go/
├── main.go                  # 演示入口
├── types/
│   ├── message.go           # 消息联合类型 + APIError（含 IsOverloaded()）
│   ├── content.go           # ContentBlock 联合类型
│   ├── events.go            # Terminal、StreamDeltaEvent
│   └── marshal.go           # 多态 JSON 序列化/反序列化
├── engine/
│   ├── engine.go            # QueryEngine + 会话状态 + MCP 注册
│   ├── submit.go            # SubmitMessage() + 权限拒绝追踪
│   └── agent.go             # 子代理运行器（创建子查询循环）
├── query/
│   ├── query.go             # Query() 入口 + QueryParams
│   ├── state.go             # State 结构体 + TransitionReason 常量
│   └── loop.go              # queryLoop() — max_tokens 恢复、fallback、stop hooks
├── api/
│   ├── client.go            # Anthropic HTTP 客户端（SSE）
│   └── stream.go            # SSE 组装器 + 错误事件处理
├── hooks/
│   └── stop.go              # StopHookFn 类型定义
├── mcp/
│   ├── types.go             # MCPTool、MCPResource、MCPContent、MCPServerConfig
│   └── client.go            # StdioMCPClient：基于 stdio 的 JSON-RPC 2.0
├── session/
│   ├── session.go           # SessionMeta、SessionFilePath、NewSessionID
│   └── persist.go           # AppendMessages、LoadSession、ListSessions
├── tools/
│   ├── tool.go              # Tool 接口、AppState、PermissionContext…
│   ├── registry.go          # 工具注册表（名称 → Tool）
│   ├── orchestration.go     # RunTools()：分批调度 + 侧信道消息转发
│   ├── budgetcompact.go     # ApplyToolResultBudget()：250k 字符上限
│   ├── agent.go             # AgentTool + SendMessageTool
│   ├── agent_registry.go    # 子代理协调注册表
│   ├── mcp_tool.go          # MCPToolWrapper：将 MCP 工具适配为 Tool 接口
│   ├── bash.go / read.go / glob.go / grep.go / ls.go / webfetch.go
│   ├── write.go / edit.go / multiedit.go / todo.go
│   ├── permissions/
│   │   ├── permissions.go   # 5 步决策链
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
| Bash / Read / Glob / Grep / LS / WebFetch 工具 | ✅ 完成 |
| Write / Edit / MultiEdit 工具 | ✅ 完成 |
| TodoRead / TodoWrite 工具 | ✅ 完成 |
| SSE 流式解析 + 错误事件处理 | ✅ 完成 |
| Thinking blocks | ✅ 已解析 + 模型切换时剥离 |
| 会话持久化（JSONL）+ 恢复 | ✅ 完成 |
| MCP 客户端（stdio JSON-RPC）+ 工具包装 | ✅ 完成 |
| Agent / SendMessage + 子代理协调 | ✅ 完成 |

---

## 路线图

SDK 目前已可使用。以下是达到生产级质量的三个优先级层。

### P0 — Agent 行为正确性

| 项目 | 描述 |
|------|------|
| **System Prompt 构建器** | 分段构建器：环境检测（OS、shell、CWD、git 分支、日期）、动态工具描述注入、CLAUDE.md 项目指令加载。这是影响最大的缺失模块——没有它模型不知道自己在什么环境下运行。 |
| **API 重试** | 429/529/5xx 指数退避，可配置 `maxRetries`，含 jitter。没有重试在真实 API 负载下不可用。 |
| **Bash 安全分类器** | 危险命令模式库（rm -rf、DROP TABLE、git push --force 等），即使在 `bypassPermissions` 模式下也作为安全兜底。 |

### P1 — Agent 交互质量

| 项目 | 描述 |
|------|------|
| **Hooks 体系扩展** | `PreToolHook`、`PostToolHook`、`MessageHook`，统一 `HookFn` 接口——SDK 用户在不修改引擎代码的前提下注入审计、过滤或改写逻辑的核心扩展机制。 |
| **AskUserQuestion 工具** | 通过 `QueryEngineConfig.UserInputFn` 回调。SDK 用户注入自己的 IO 实现（CLI stdin、Web 表单、Slack 机器人等）。没有此工具模型无法主动询问用户。 |
| **Token 估算 + 预算** | 准确的 token 计数，发请求前预算检查，主动触发 compact 而非等待 413 错误。 |
| **CLAUDE.md + 项目上下文** | 从 CWD 到 git root 递归搜索 `.claude/CLAUDE.md` 和 `CLAUDE.md`，合并注入 system prompt。 |

### P2 — 生态

| 项目 | 描述 |
|------|------|
| **MCP SSE/HTTP transport** | 支持远程 MCP 服务器（GitHub MCP、Slack MCP 等），超越 stdio。 |
| **流式回调 API** | `QueryEngineConfig.OnMessage func(SDKMessage)` 回调 + `engine.RunSync()` 同步便捷方法。 |
| **结构化日志** | 集成 `slog.Logger`，全链路可观测性：API 调用、工具执行、权限决策、compact 触发。 |
| **测试覆盖** | 核心路径单元测试：query loop（mock API）、compact 管线、权限链、会话持久化、MCP 客户端。 |
| **Go module 发布** | 正式 module 路径、语义化版本、godoc 注释、示例目录。 |

---

## 许可

本项目为教育研究目的的逆向重新实现。原始 Claude Code 源码为 Anthropic 所有。
