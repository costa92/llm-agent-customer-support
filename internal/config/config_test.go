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
