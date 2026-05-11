package supportflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agents "github.com/costa92/llm-agent"
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

func newTestFlow(t *testing.T, model llm.ChatModel) agents.Agent {
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
	flow, err := New(Options{
		Model: model,
		RAG:   system,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return flow
}
