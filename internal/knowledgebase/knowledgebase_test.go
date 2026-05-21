package knowledgebase

import (
	"context"
	"strings"
	"testing"

	"github.com/costa92/llm-agent/llm"
)

func TestNewAndLookupRefundPolicy(t *testing.T) {
	kb, err := New(context.Background(), llm.NewScriptedLLM(
		llm.WithProvider("scripted-embed"),
		llm.WithModel("embed"),
		llm.WithCapabilities(llm.Capabilities{Embeddings: true}),
		llm.WithEmbedDimensions(8),
	))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	got, err := kb.LookupRefundPolicy(context.Background(), "123")
	if err != nil {
		t.Fatalf("LookupRefundPolicy() error = %v", err)
	}
	if got == "" {
		t.Fatal("LookupRefundPolicy() returned empty string")
	}
	if want := "refund guidance"; !strings.Contains(strings.ToLower(got), want) {
		t.Fatalf("LookupRefundPolicy() = %q, want mention of %q", got, want)
	}
}

func TestNewRejectsNilEmbedder(t *testing.T) {
	if _, err := New(context.Background(), nil); err == nil {
		t.Fatal("New(nil) error = nil, want error")
	}
}
