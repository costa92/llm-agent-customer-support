package flowrunner_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/costa92/llm-agent-customer-support/internal/flowrunner"
	"github.com/costa92/llm-agent-flow/flow"
	"go.opentelemetry.io/otel/codes"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

const echoChainFlow = `{
	"id":"cs_echo",
	"name":"customer-support echo demo",
	"nodes":[
		{"id":"upper","type":"tool","config":{"tool":"upper"}},
		{"id":"reverse","type":"tool","config":{"tool":"reverse"}}
	],
	"edges":[
		{"source":{"node":"upper","port":"output"},
		 "target":{"node":"reverse","port":"input"}}
	],
	"inputs":[{"name":"in","node":"upper","port":"input"}],
	"outputs":[{"name":"out","node":"reverse","port":"output"}]
}`

type fakeTool struct {
	name string
	fn   func(string) string
}

func (t *fakeTool) Name() string { return t.name }
func (t *fakeTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Input string `json:"input"`
	}
	_ = json.Unmarshal(args, &p)
	return t.fn(p.Input), nil
}

func reverseStr(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

func newRunner(t *testing.T) (*flowrunner.Runner, *tracetest.InMemoryExporter) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := tracesdk.NewTracerProvider(tracesdk.WithSyncer(exp))
	r, err := flowrunner.New(flowrunner.Config{
		TracerProvider: tp,
		Tools: flow.ToolMap{
			"upper":   &fakeTool{name: "upper", fn: strings.ToUpper},
			"reverse": &fakeTool{name: "reverse", fn: reverseStr},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r, exp
}

func TestRunnerExecuteEndToEnd(t *testing.T) {
	r, exp := newRunner(t)
	out, err := r.Execute(context.Background(), strings.NewReader(echoChainFlow), map[string]string{"in": "hello"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := out["out"]; got != "OLLEH" {
		t.Fatalf("out = %q, want OLLEH", got)
	}
	// Sync path: exactly one root span.
	if got := len(exp.GetSpans()); got != 1 {
		t.Fatalf("spans = %d, want 1", got)
	}
	root := exp.GetSpans()[0]
	if !strings.HasPrefix(root.Name, "flow.run") {
		t.Fatalf("root span name = %q, want flow.run prefix", root.Name)
	}
	if root.Status.Code != codes.Ok {
		t.Fatalf("status = %v, want Ok", root.Status)
	}
}

func TestRunnerExecuteStreamPerNodeSpans(t *testing.T) {
	r, exp := newRunner(t)
	ch, err := r.ExecuteStream(context.Background(), strings.NewReader(echoChainFlow), map[string]string{"in": "hi"})
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	for range ch {
	}
	// 1 root + 2 node spans = 3.
	if got := len(exp.GetSpans()); got != 3 {
		t.Fatalf("spans = %d, want 3", got)
	}
}

func TestRunnerExecuteSurfacesCompileError(t *testing.T) {
	r, _ := newRunner(t)
	// Unknown node type → flow.LoadCompile fails before run.
	const bad = `{"id":"x","nodes":[{"id":"a","type":"unknown"}],"edges":[]}`
	_, err := r.Execute(context.Background(), strings.NewReader(bad), nil)
	if err == nil || !strings.Contains(err.Error(), "compile") {
		t.Fatalf("Execute(bad) = %v, want compile error", err)
	}
}

func TestRunnerNoTracerProviderNoOps(t *testing.T) {
	r, err := flowrunner.New(flowrunner.Config{
		Tools: flow.ToolMap{
			"upper":   &fakeTool{name: "upper", fn: strings.ToUpper},
			"reverse": &fakeTool{name: "reverse", fn: reverseStr},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := r.Execute(context.Background(), strings.NewReader(echoChainFlow), map[string]string{"in": "x"}); err != nil {
		t.Fatalf("Execute (no TP): %v", err)
	}
}
