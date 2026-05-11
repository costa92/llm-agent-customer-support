package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadFromLookup_Defaults(t *testing.T) {
	cfg, err := LoadFromLookup(func(string) (string, bool) {
		return "", false
	})
	if err != nil {
		t.Fatalf("LoadFromLookup() error = %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Fatalf("HTTPAddr = %q, want %q", cfg.HTTPAddr, ":8080")
	}
	if cfg.Provider != ProviderOllama {
		t.Fatalf("Provider = %q, want %q", cfg.Provider, ProviderOllama)
	}
	if cfg.Model != "llama3.1:8b" {
		t.Fatalf("Model = %q, want %q", cfg.Model, "llama3.1:8b")
	}
	if cfg.EmbeddingProvider != ProviderOllama {
		t.Fatalf("EmbeddingProvider = %q, want %q", cfg.EmbeddingProvider, ProviderOllama)
	}
	if cfg.EmbeddingModel != "nomic-embed-text" {
		t.Fatalf("EmbeddingModel = %q, want %q", cfg.EmbeddingModel, "nomic-embed-text")
	}
	if cfg.SessionBackend != "sqlite" {
		t.Fatalf("SessionBackend = %q, want %q", cfg.SessionBackend, "sqlite")
	}
	if cfg.SessionDSN == "" {
		t.Fatal("SessionDSN is empty")
	}
	if cfg.SystemPrompt == "" {
		t.Fatal("SystemPrompt is empty, want default prompt")
	}
	if cfg.OTLPProtocol != "http" {
		t.Fatalf("OTLPProtocol = %q, want %q", cfg.OTLPProtocol, "http")
	}
	if cfg.OTLPEndpoint != "http://localhost:4318" {
		t.Fatalf("OTLPEndpoint = %q, want %q", cfg.OTLPEndpoint, "http://localhost:4318")
	}
	if !cfg.OTLPInsecure {
		t.Fatal("OTLPInsecure = false, want true")
	}
	if cfg.ServiceName != "llm-agent-customer-support" {
		t.Fatalf("ServiceName = %q, want %q", cfg.ServiceName, "llm-agent-customer-support")
	}
	if cfg.ShutdownTimeout != 5*time.Second {
		t.Fatalf("ShutdownTimeout = %v, want %v", cfg.ShutdownTimeout, 5*time.Second)
	}
}

func TestLoadFromLookup_ProviderSpecificDefaults(t *testing.T) {
	cfg, err := LoadFromLookup(func(key string) (string, bool) {
		switch key {
		case "LLM_PROVIDER":
			return ProviderAnthropic, true
		case "EMBEDDING_PROVIDER":
			return ProviderOpenAI, true
		default:
			return "", false
		}
	})
	if err != nil {
		t.Fatalf("LoadFromLookup() error = %v", err)
	}
	if cfg.Model != "claude-3-5-haiku-20241022" {
		t.Fatalf("Model = %q, want %q", cfg.Model, "claude-3-5-haiku-20241022")
	}
	if cfg.EmbeddingModel != "text-embedding-3-small" {
		t.Fatalf("EmbeddingModel = %q, want %q", cfg.EmbeddingModel, "text-embedding-3-small")
	}
}

func TestLoadFromLookup_InvalidProvider(t *testing.T) {
	_, err := LoadFromLookup(func(key string) (string, bool) {
		if key == "LLM_PROVIDER" {
			return "bogus", true
		}
		return "", false
	})
	if err == nil {
		t.Fatal("LoadFromLookup() error = nil, want invalid provider error")
	}
	if !strings.Contains(err.Error(), "LLM_PROVIDER") {
		t.Fatalf("LoadFromLookup() error = %q, want mention of LLM_PROVIDER", err)
	}
}

func TestLoadFromLookup_InvalidEmbeddingProvider(t *testing.T) {
	_, err := LoadFromLookup(func(key string) (string, bool) {
		if key == "EMBEDDING_PROVIDER" {
			return "bogus", true
		}
		return "", false
	})
	if err == nil {
		t.Fatal("LoadFromLookup() error = nil, want invalid embedding provider error")
	}
	if !strings.Contains(err.Error(), "EMBEDDING_PROVIDER") {
		t.Fatalf("LoadFromLookup() error = %q, want mention of EMBEDDING_PROVIDER", err)
	}
}

func TestLoadFromLookup_InvalidSessionBackend(t *testing.T) {
	_, err := LoadFromLookup(func(key string) (string, bool) {
		if key == "SESSION_BACKEND" {
			return "bogus", true
		}
		return "", false
	})
	if err == nil {
		t.Fatal("LoadFromLookup() error = nil, want invalid session backend error")
	}
	if !strings.Contains(err.Error(), "SESSION_BACKEND") {
		t.Fatalf("LoadFromLookup() error = %q, want mention of SESSION_BACKEND", err)
	}
}
