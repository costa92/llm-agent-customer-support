package knowledgebase

import (
	"context"
	"strings"
	"testing"

	ragembed "github.com/costa92/llm-agent-rag/embed"
	"github.com/costa92/llm-agent-contract/llm"
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

// TestRagEmbedderAdapterImplementsBatchEmbedder pins the v1.0.2 sibling
// capability at compile time: the adapter must satisfy
// embed.BatchEmbedder so rag.System.Import engages the batch fast path.
// The upstream llm.Embedder is already batch-shaped, so the adapter
// delegates directly without per-text fan-out.
func TestRagEmbedderAdapterImplementsBatchEmbedder(t *testing.T) {
	var _ ragembed.BatchEmbedder = ragEmbedderAdapter{}
}

// recordingLLMEmbedder is a minimal llm.Embedder that records every
// Embed call so the adapter test can verify (a) the texts pass through
// in input order and (b) the upstream is called exactly once per
// EmbedBatch — no per-text fan-out.
type recordingLLMEmbedder struct {
	dim   int
	calls int
	last  []string
}

func (r *recordingLLMEmbedder) Embed(_ context.Context, texts []string) ([]llm.Vector, llm.Usage, error) {
	r.calls++
	r.last = append([]string(nil), texts...)
	out := make([]llm.Vector, len(texts))
	for i := range texts {
		v := make(llm.Vector, r.dim)
		if r.dim > 0 {
			v[0] = float32(len(texts[i]))
		}
		out[i] = v
	}
	return out, llm.Usage{}, nil
}

func (r *recordingLLMEmbedder) EmbedDimensions() int { return r.dim }

// TestRagEmbedderAdapterEmbedBatchDelegatesToUpstream asserts the
// adapter's EmbedBatch implementation calls the upstream batch-shaped
// llm.Embedder exactly once with the same texts in order, and returns
// vectors aligned to the input.
func TestRagEmbedderAdapterEmbedBatchDelegatesToUpstream(t *testing.T) {
	rec := &recordingLLMEmbedder{dim: 8}
	adapter := ragEmbedderAdapter{inner: rec}
	texts := []string{"a", "bb", "ccc"}
	got, err := adapter.EmbedBatch(context.Background(), texts)
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if rec.calls != 1 {
		t.Fatalf("upstream Embed calls = %d, want 1 (no per-text fan-out)", rec.calls)
	}
	if len(rec.last) != len(texts) {
		t.Fatalf("upstream got %d texts, want %d", len(rec.last), len(texts))
	}
	for i, want := range texts {
		if rec.last[i] != want {
			t.Fatalf("upstream texts[%d] = %q, want %q (order broken)", i, rec.last[i], want)
		}
	}
	if len(got) != len(texts) {
		t.Fatalf("len(EmbedBatch result) = %d, want %d", len(got), len(texts))
	}
	for i, t1 := range texts {
		if got[i][0] != float32(len(t1)) {
			t.Fatalf("vector[%d][0] = %v, want %v (input order broken)", i, got[i][0], float32(len(t1)))
		}
	}
}
