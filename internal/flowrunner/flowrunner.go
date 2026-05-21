package flowrunner

import (
	"context"
	"fmt"
	"io"

	"github.com/costa92/llm-agent-flow/flow"
	"github.com/costa92/llm-agent-otel/otelflow"
	"go.opentelemetry.io/otel/trace"
)

// Config is the runner's input. TracerProvider, when non-nil, wires
// per-flow + per-node spans through otelflow into the host's OTel
// pipeline. A nil TracerProvider yields a no-op tracer — the runner
// still works, just without telemetry.
type Config struct {
	TracerProvider trace.TracerProvider
	Tools          flow.ToolMap
	Cond           flow.ConditionEvaluator
}

// Runner composes a flow.Engine (compiled per-call) with an
// otelflow-wrapped flow.Runner. Each Execute call produces a fresh
// Engine; for production use a flowd-style cache, this package
// favors clarity over performance.
type Runner struct {
	cfg Config
	reg *flow.NodeRegistry
}

// New returns a Runner with the bundled tool node type pre-registered.
// Additional node types may be wired by the caller via Register.
func New(cfg Config) (*Runner, error) {
	reg := flow.NewNodeRegistry()
	if err := flow.RegisterToolNode(reg); err != nil {
		return nil, fmt.Errorf("flowrunner: register tool node: %w", err)
	}
	return &Runner{cfg: cfg, reg: reg}, nil
}

// Register adds a custom node type to the runner's registry.
func (r *Runner) Register(typ string, factory flow.NodeFactory) error {
	return r.reg.Register(typ, factory)
}

// Execute compiles flowJSON, wraps the resulting engine with
// otelflow, and runs it synchronously against inputs.
//
// Spans emitted (when TracerProvider is non-nil):
//
//	flow.run <id>                  — root
//	└── flow.node <id>             — per-node (omitted in sync path —
//	                                 otelflow's per-node spans fire
//	                                 only on RunStream)
//
// For a flow where per-node tracing matters, use ExecuteStream.
func (r *Runner) Execute(ctx context.Context, flowJSON io.Reader, inputs map[string]string) (map[string]string, error) {
	wrapped, err := r.compileAndWrap(flowJSON)
	if err != nil {
		return nil, err
	}
	return wrapped.Run(ctx, inputs)
}

// ExecuteStream is the streaming variant. Returns the same channel
// shape llm-agent-flow's Engine.RunStream produces; consumers drain
// the channel to collect events / outputs. otelflow creates one
// child span per node lifecycle.
func (r *Runner) ExecuteStream(ctx context.Context, flowJSON io.Reader, inputs map[string]string) (<-chan flow.FlowEvent, error) {
	wrapped, err := r.compileAndWrap(flowJSON)
	if err != nil {
		return nil, err
	}
	return wrapped.RunStream(ctx, inputs)
}

func (r *Runner) compileAndWrap(flowJSON io.Reader) (flow.Runner, error) {
	opts := []flow.EngineOption{}
	if r.cfg.Cond != nil {
		opts = append(opts, flow.WithConditionEvaluator(r.cfg.Cond))
	}
	engine, err := flow.LoadCompile(flowJSON, r.reg, flow.Deps{Tools: r.cfg.Tools}, opts...)
	if err != nil {
		return nil, fmt.Errorf("flowrunner: compile: %w", err)
	}
	return otelflow.Wrap(engine, otelflow.Config{TracerProvider: r.cfg.TracerProvider}), nil
}
