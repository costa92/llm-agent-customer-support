package supportflow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	agents "github.com/costa92/llm-agent"
	"github.com/costa92/llm-agent-customer-support/internal/sessionstore"
	"github.com/costa92/llm-agent/llm"
	"github.com/costa92/llm-agent/rag"
)

func TestFlow_AutoReplyUsesKnowledgeLookup(t *testing.T) {
	flow := newTestFlow(t,
		llm.NewScriptedLLM(
			llm.WithProvider("scripted-chat"),
			llm.WithModel("chat"),
			llm.WithCapabilities(llm.Capabilities{Tools: true}),
			llm.WithResponses(llm.Response{
				Provider: "scripted-chat",
				ToolCalls: []llm.ToolCall{
					{Name: "refund_policy", Arguments: json.RawMessage(`{"order_id":"123"}`)},
				},
			}),
		),
	)

	res, err := flow.Run(context.Background(), "I need a refund for my order 123")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(strings.ToLower(res.Answer), "refund") {
		t.Fatalf("Answer = %q, want refund guidance", res.Answer)
	}
	if !strings.Contains(res.Answer, "24h") {
		t.Fatalf("Answer = %q, want policy evidence from knowledge lookup", res.Answer)
	}
}

func TestFlow_MissingOrderIDRequestsMoreInfo(t *testing.T) {
	flow := newTestFlow(t, llm.NewScriptedLLM(
		llm.WithProvider("scripted-chat"),
		llm.WithModel("chat"),
		llm.WithCapabilities(llm.Capabilities{Tools: true}),
	))

	res, err := flow.Run(context.Background(), "I need a refund")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(strings.ToLower(res.Answer), "order id") {
		t.Fatalf("Answer = %q, want order ID clarification", res.Answer)
	}
}

func TestFlow_ChargebackEscalatesToHuman(t *testing.T) {
	flow := newTestFlow(t, llm.NewScriptedLLM(
		llm.WithProvider("scripted-chat"),
		llm.WithModel("chat"),
		llm.WithCapabilities(llm.Capabilities{Tools: true}),
	))

	res, err := flow.Run(context.Background(), "This is a chargeback on order 123")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(strings.ToLower(res.Answer), "human") {
		t.Fatalf("Answer = %q, want human escalation", res.Answer)
	}
}

func TestFlow_RunStreamEmitsFinal(t *testing.T) {
	flow := newTestFlow(t,
		llm.NewScriptedLLM(
			llm.WithProvider("scripted-chat"),
			llm.WithModel("chat"),
			llm.WithCapabilities(llm.Capabilities{Tools: true}),
			llm.WithResponses(llm.Response{
				Provider: "scripted-chat",
				ToolCalls: []llm.ToolCall{
					{Name: "refund_policy", Arguments: json.RawMessage(`{"order_id":"123"}`)},
				},
			}),
		),
	)

	ch, err := flow.RunStream(context.Background(), "Refund order 123")
	if err != nil {
		t.Fatalf("RunStream() error = %v", err)
	}
	lastDone := false
	for ev := range ch {
		if ev.Done && ev.Final != nil {
			lastDone = true
			if ev.Final.Answer == "" {
				t.Fatal("Final answer is empty")
			}
		}
	}
	if !lastDone {
		t.Fatal("stream did not emit final done event")
	}
}

func TestFlow_SessionHistoryPersistsOutsideAgentInstances(t *testing.T) {
	store, err := sessionstore.OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := sessionstore.ContextWithSessionID(context.Background(), "session-1")
	flow1 := newTestFlow(t, &contextAwareToolModel{}, withSessionStore(store))
	res, err := flow1.Run(ctx, "I need a refund")
	if err != nil {
		t.Fatalf("Run() first error = %v", err)
	}
	if !strings.Contains(strings.ToLower(res.Answer), "order id") {
		t.Fatalf("first answer = %q, want order ID clarification", res.Answer)
	}

	flow2 := newTestFlow(t, &contextAwareToolModel{}, withSessionStore(store))
	res, err = flow2.Run(ctx, "it's 123")
	if err != nil {
		t.Fatalf("Run() second error = %v", err)
	}
	if !strings.Contains(res.Answer, "24h") {
		t.Fatalf("second answer = %q, want refund policy using persisted history", res.Answer)
	}
}

type testFlowOption func(*Options)

func withSessionStore(store sessionstore.Store) testFlowOption {
	return func(opts *Options) {
		opts.Sessions = store
	}
}

func newTestFlow(t *testing.T, model llm.ChatModel, opts ...testFlowOption) agents.Agent {
	t.Helper()
	store := rag.NewInMemoryStore(32)
	system := rag.New(rag.Options{
		Embedder: rag.NewHashEmbedder(32),
		Store:    store,
	})
	_, err := system.AddText(context.Background(), "Orders cancelled within 24h are eligible for a full refund.", map[string]any{
		"topic": "refund_policy",
	})
	if err != nil {
		t.Fatalf("AddText() error = %v", err)
	}
	options := Options{
		Model: model,
		RAG:   system,
	}
	for _, opt := range opts {
		opt(&options)
	}
	flow, err := New(options)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return flow
}

type contextAwareToolModel struct{}

func (m *contextAwareToolModel) Generate(_ context.Context, req llm.Request) (llm.Response, error) {
	if len(req.Messages) == 0 {
		return llm.Response{}, fmt.Errorf("no messages")
	}
	prompt := strings.ToLower(req.Messages[0].Content)
	if strings.Contains(prompt, "refund") && strings.Contains(prompt, "123") {
		return llm.Response{
			Provider: "context-aware",
			ToolCalls: []llm.ToolCall{
				{Name: "refund_policy", Arguments: json.RawMessage(`{"order_id":"123"}`)},
			},
		}, nil
	}
	return llm.TextResponse("missing prior context"), nil
}

func (m *contextAwareToolModel) Stream(_ context.Context, _ llm.Request) (llm.StreamReader, error) {
	return nil, io.EOF
}

func (m *contextAwareToolModel) Info() llm.ProviderInfo {
	return llm.ProviderInfo{
		Provider: "context-aware",
		Model:    "test",
		Capabilities: llm.Capabilities{
			Tools: true,
		},
	}
}

func (m *contextAwareToolModel) WithTools(_ []llm.Tool) (llm.ToolCaller, error) {
	return m, nil
}
