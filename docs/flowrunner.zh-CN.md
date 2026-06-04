# `internal/flowrunner` —— 在服务中嵌入 `llm-agent-flow`

`flowrunner` 包是
[`github.com/costa92/llm-agent-flow`](https://github.com/costa92/llm-agent-flow)
的下游消费者示例：
它通过
[`github.com/costa92/llm-agent-otel/otelflow`](https://github.com/costa92/llm-agent-otel)
将一个流程 `Engine` 与客服服务共享的 OTel `TracerProvider` 组合在一起，
使每个流程和每个节点的 span 与已有的
chat / tool / RAG span 共享同一棵链路树。

该包**刻意保持狭窄**——它展示的是组合
模式，而非生产级的流程目录。对于真正的生产用途，
请从 `llm-agent-flow` 仓库部署 `cmd/flowd`，并作为
远程服务调用它。

## When to use this vs `cmd/flowd`

| | `internal/flowrunner` | `cmd/flowd` |
|---|---|---|
| 进程边界 | 进程内，位于本二进制中 | 独立的 HTTP 服务 |
| 持久化 | 无（流程 JSON 按调用提供） | 基于 SQLite 的 CRUD + 运行历史 |
| 鉴权 | 隐式（只有本服务能访问到它） | Bearer token |
| 适用场景 | 流程是服务源码 / 配置一部分的应用内组合 | 解耦的编排；多消费者；运维管理的流程 |
| 链路集成 | 共享服务已有的 TracerProvider | 需要服务自行包装引擎，或通过 HTTP 调用 flowd 并传播 ctx |

对于客服参考服务，`flowrunner` 足以
满足演示需要。对于你自己的部署，请参照上表评估。

## API

```go
import "github.com/costa92/llm-agent-customer-support/internal/flowrunner"

cfg := flowrunner.Config{
    TracerProvider: tp,                                    // shared with the rest of the app
    Tools:          flow.FromAgentTools(myAgentsTools()),  // bridge from agents.Tool
    Cond:           celEval,                               // optional — for conditional flows
}
r, err := flowrunner.New(cfg)
```

两个执行方法：

```go
// Sync — single root span, returns outputs.
outputs, err := r.Execute(ctx, flowJSONReader, inputs)

// Streaming — root span + per-node child spans, yields FlowEvents.
ch, err := r.ExecuteStream(ctx, flowJSONReader, inputs)
for ev := range ch {
    // ev.Kind / ev.NodeID / ev.Input / ev.Output / ...
}
```

除内置的 `tool` kind 之外的额外节点类型，通过
`Register` 注册：

```go
r.Register("my_custom_kind", func(cfg json.RawMessage, deps flow.Deps) (flow.NodeKind, error) {
    // ... build the node
})
```

## What span tree looks like

一次同步 `Execute` 只产生单个 span——`otelflow` 在同步路径上不会
创建每个节点的 span（同步运行通常不
需要每步可见性）：

```
chat (root from upstream handler)
└── flow.run <id>             ← otelflow root
```

一次流式 `ExecuteStream` 会产生每个节点的子 span：

```
chat (root from upstream handler)
└── flow.run.stream <id>      ← otelflow root
    ├── flow.node upper       ← per-node lifecycle
    └── flow.node reverse
```

被跳过的节点（CEL 条件分支）会获得带
`flow.node.skipped=true` 的零时长 span，从而让链路把拓扑
显式表达出来。

## How it composes with the rest of the service

客服二进制已经在其 ChatModel 周围运行 `otelmodel.Wrap`，
在其 Agent 周围运行 `otelagent.Wrap`。`flowrunner`
将同一模式扩展到流程执行——当你在处理器中接线它时：

```go
func (h *handler) handleFlowRequest(ctx context.Context, ...) error {
    // The host app supplies its own tracer provider and tool set.
    tools := flow.FromAgentTools(myAgentTools())

    runner, err := flowrunner.New(flowrunner.Config{
        TracerProvider: tp,
        Tools:          tools,
    })
    if err != nil {
        return err
    }
    out, err := runner.Execute(ctx, flowJSON, inputs)
    // out / err propagate to the caller; spans are already in the tree.
    return nil
}
```

关键在于 `ctx` 参数就是上游 HTTP 处理器
所接收到的那个——span 上下文随 `ctx` 流动，因此流程 span 会自动
嵌套在 chat / 请求 span 之下。

## Scenarios this package does NOT cover

- **持久化的运行历史。** 每次 `Execute` 调用都会编译一个全新的
  引擎并运行它；不存储任何内容。如需运行历史，请部署
  `flowd` 并将流程 POST 到那里。
- **流程目录。** `flowrunner` 消费你按调用传入的 JSON；
  它没有「按名称取流程 X」的概念。那是 `flowd` 的工作。
- **向外部客户端流式传输。** `ExecuteStream` 返回一个
  Go channel；由调用方转发事件。对于 SSE-to-HTTP，请使用
  `flowd`。
- **跨请求的引擎缓存。** 每次 `Execute` 都会重新编译。对于
  本二进制中的热点流程，可将 `Runner` 实例化一次并在多个请求间保留
  它；即便如此，编译仍发生在每次 `Execute`
  调用上——没有按 flow-id 的记忆化。如果这很重要，请用你自己的缓存
  包装，或改用 `flowd`。

## Tests

`internal/flowrunner/flowrunner_test.go`（4 个用例）是该契约：

- 带 span 断言的端到端同步执行
- 端到端流式执行，断言 1 个 root + 每个节点的 span
- 编译错误的暴露
- nil TracerProvider 可在不 panic 的情况下工作

运行方式：

```bash
GOWORK=off go test ./internal/flowrunner/ -count=1 -v
```

## Dependency posture

该包为本 module 增加了两个直接依赖：

- `github.com/costa92/llm-agent-flow v0.1.1`（引擎）
- `github.com/costa92/llm-agent-otel v0.2.2`（这是本仓库**当前
  `go.mod` 中的锚定**，为客服当前的依赖快照而保留；
  更大的生态此后已发布更新的
  `llm-agent-otel` 版本）

两者都是同一 `costa92/*` 生态内的兄弟仓；它们的
冻结保证（分别为 v0.1.x 和 v0.2.x）限定了本包所消费的 API
面。

## Removing it

如果某个下游 fork 不想要这份依赖负担：删除
`internal/flowrunner/` 并从 `go.mod` 中移除 `llm-agent-flow` require
行。二进制中没有其他任何东西引用它。
