# `internal/flowrunner` — embedding `llm-agent-flow` in the service

The `flowrunner` package is the downstream-consumer example for
[`github.com/costa92/llm-agent-flow`](https://github.com/costa92/llm-agent-flow):
it composes a flow `Engine` with the customer-support service's
shared OTel `TracerProvider` via
[`github.com/costa92/llm-agent-otel/otelflow`](https://github.com/costa92/llm-agent-otel),
so per-flow and per-node spans share a trace tree with the existing
chat / tool / RAG spans.

The package is **deliberately narrow** — it shows the composition
pattern, not a production flow catalog. For real production use,
deploy `cmd/flowd` from the `llm-agent-flow` repo and call it as a
remote service.

## When to use this vs `cmd/flowd`

| | `internal/flowrunner` | `cmd/flowd` |
|---|---|---|
| Process boundary | In-process, in this binary | Separate HTTP service |
| Persistence | None (flow JSON supplied per call) | SQLite-backed CRUD + run history |
| Auth | Implicit (only this service can reach it) | Bearer token |
| Use case | In-app composition where the flow is part of the service's source / config | Decoupled orchestration; multiple consumers; ops-managed flows |
| Trace integration | Shares the service's existing TracerProvider | Needs the service to wrap engines itself, OR call flowd over HTTP and propagate ctx |

For the customer-support reference service, `flowrunner` is enough
for the demo. For your own deployment, evaluate above.

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

Two execution methods:

```go
// Sync — single root span, returns outputs.
outputs, err := r.Execute(ctx, flowJSONReader, inputs)

// Streaming — root span + per-node child spans, yields FlowEvents.
ch, err := r.ExecuteStream(ctx, flowJSONReader, inputs)
for ev := range ch {
    // ev.Kind / ev.NodeID / ev.Input / ev.Output / ...
}
```

Additional node types beyond the bundled `tool` kind register via
`Register`:

```go
r.Register("my_custom_kind", func(cfg json.RawMessage, deps flow.Deps) (flow.NodeKind, error) {
    // ... build the node
})
```

## What span tree looks like

A sync `Execute` produces a single span — `otelflow` does not
create per-node spans on the sync path (sync runs typically don't
need per-step visibility):

```
chat (root from upstream handler)
└── flow.run <id>             ← otelflow root
```

A streaming `ExecuteStream` produces per-node children:

```
chat (root from upstream handler)
└── flow.run.stream <id>      ← otelflow root
    ├── flow.node upper       ← per-node lifecycle
    └── flow.node reverse
```

Skipped nodes (CEL conditional branches) get zero-duration spans
with `flow.node.skipped=true` so the trace makes the topology
explicit.

## How it composes with the rest of the service

The customer-support binary already runs `otelmodel.Wrap` around its
ChatModel and `otelagent.Wrap` around its Agent. `flowrunner`
extends the same pattern to flow execution — when you wire it in a
handler:

```go
func (h *handler) handleFlowRequest(ctx context.Context, ...) error {
    // Optional: pull tools from the app's existing tool catalog.
    tools := flow.FromAgentTools(h.app.Tools())

    runner, err := flowrunner.New(flowrunner.Config{
        TracerProvider: h.app.TracerProvider(),
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

Critically the `ctx` parameter is the one the upstream HTTP handler
received — span context flows through `ctx`, so flow spans nest
under the chat / request span automatically.

## Scenarios this package does NOT cover

- **Persisted run history.** Each `Execute` call compiles a fresh
  engine and runs it; nothing is stored. For run history, deploy
  `flowd` and POST flows there.
- **A flow catalog.** `flowrunner` consumes JSON you pass per call;
  it has no notion of "flow X by name." That's `flowd`'s job.
- **Streaming to an external client.** `ExecuteStream` returns a
  Go channel; the caller forwards events. For SSE-to-HTTP, use
  `flowd`.
- **Engine cache across requests.** Every `Execute` recompiles. For
  hot flows in this binary, instantiate the `Runner` once and keep
  it across requests; even so, compilation happens per `Execute`
  call — there's no per-flow-id memoization. If that matters, wrap
  with your own cache or call `flowd`.

## Tests

`internal/flowrunner/flowrunner_test.go` (4 cases) is the contract:

- end-to-end sync execution with span assertions
- end-to-end stream execution asserting 1 root + per-node spans
- compile-error surfacing
- nil TracerProvider works without panics

Run with:

```bash
GOWORK=off go test ./internal/flowrunner/ -count=1 -v
```

## Dependency posture

The package adds two direct dependencies to this module:

- `github.com/costa92/llm-agent-flow v0.1.1` (the engine)
- `github.com/costa92/llm-agent-otel v0.2.2` (already-present, bumped
  for the new `otelflow` sub-package)

Both are sister repos within the same `costa92/*` ecosystem; their
freeze guarantees (v0.1.x and v0.2.x respectively) bound the API
surface this package consumes.

## Removing it

If a downstream fork doesn't want the dep weight: delete
`internal/flowrunner/` and drop the `llm-agent-flow` require line
from `go.mod`. Nothing else in the binary references it.
