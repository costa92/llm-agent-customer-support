package knowledgebase

import (
	"context"
	"fmt"

	ragembed "github.com/costa92/llm-agent-rag/embed"
	ragingest "github.com/costa92/llm-agent-rag/ingest"
	ragcore "github.com/costa92/llm-agent-rag/rag"
	ragstore "github.com/costa92/llm-agent-rag/store"
	"github.com/costa92/llm-agent-contract/llm"
)

type PolicyLookup interface {
	LookupRefundPolicy(ctx context.Context, orderID string) (string, error)
}

type policyKB struct {
	system *ragcore.System
}

type ragEmbedderAdapter struct {
	inner llm.Embedder
}

func (a ragEmbedderAdapter) Embed(ctx context.Context, text string) (ragembed.Vector, error) {
	vectors, _, err := a.inner.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vectors) == 0 {
		return nil, nil
	}
	return ragembed.Vector(vectors[0]), nil
}

func (a ragEmbedderAdapter) Dimension() int {
	return a.inner.EmbedDimensions()
}

// EmbedBatch implements ragembed.BatchEmbedder. The upstream
// llm.Embedder is already batch-shaped, so we delegate directly
// without per-text fan-out — closing the batch seam that the v1.0.1
// per-text Embed adapter destroyed. rag.System.Import auto-engages the
// batch fast path via type assertion when this method exists.
func (a ragEmbedderAdapter) EmbedBatch(ctx context.Context, texts []string) ([]ragembed.Vector, error) {
	vectors, _, err := a.inner.Embed(ctx, texts)
	if err != nil {
		return nil, err
	}
	out := make([]ragembed.Vector, len(vectors))
	for i, v := range vectors {
		out[i] = ragembed.Vector(v)
	}
	return out, nil
}

func New(ctx context.Context, embedder llm.Embedder) (PolicyLookup, error) {
	if embedder == nil {
		return nil, fmt.Errorf("knowledgebase: embedder is required")
	}
	adapted := ragEmbedderAdapter{inner: embedder}
	system := ragcore.New(ragcore.Options{
		Embedder: adapted,
		Store:    ragstore.NewInMemoryStore(adapted.Dimension()),
	})
	seedDocs := []struct {
		text string
		meta map[string]any
	}{
		{
			text: "Orders cancelled within 24h are eligible for a full refund.",
			meta: map[string]any{"topic": "refund_policy"},
		},
	}
	for _, doc := range seedDocs {
		if _, err := system.Import(ctx, []ragingest.Document{{
			ID:       "seed-refund-policy",
			Content:  doc.text,
			Metadata: doc.meta,
		}}, ragingest.ImportOptions{Namespace: "kb"}); err != nil {
			return nil, err
		}
	}
	return &policyKB{system: system}, nil
}

func NewForTesting(lookup PolicyLookup) PolicyLookup {
	return lookup
}

func (k *policyKB) LookupRefundPolicy(ctx context.Context, orderID string) (string, error) {
	hits, err := k.system.Retrieve(ctx, "refund policy order "+orderID, ragcore.SearchOptions{
		Namespace: "kb",
		TopK:      1,
	})
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return "No refund policy evidence found for this order.", nil
	}
	return fmt.Sprintf("Refund guidance for order %s: %s", orderID, hits[0].Chunk.Content), nil
}
