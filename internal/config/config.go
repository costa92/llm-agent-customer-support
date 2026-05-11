package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"
	ProviderOllama    = "ollama"
)

const defaultSystemPrompt = "You are a helpful customer-support assistant. Be concise, factual, and safe."

type Config struct {
	HTTPAddr          string
	Provider          string
	Model             string
	EmbeddingProvider string
	EmbeddingModel    string
	SystemPrompt      string
	ServiceName       string
	ShutdownTimeout   time.Duration

	OTLPProtocol string
	OTLPEndpoint string
	OTLPInsecure bool

	OpenAIAPIKey     string
	OpenAIBaseURL    string
	AnthropicAPIKey  string
	AnthropicBaseURL string
	OllamaBaseURL    string
}

func Load() (Config, error) {
	return LoadFromLookup(os.LookupEnv)
}

func LoadFromLookup(lookup func(string) (string, bool)) (Config, error) {
	cfg := Config{
		HTTPAddr:         envOrDefault(lookup, "HTTP_ADDR", ":8080"),
		Provider:         strings.ToLower(envOrDefault(lookup, "LLM_PROVIDER", ProviderOllama)),
		SystemPrompt:     envOrDefault(lookup, "SYSTEM_PROMPT", defaultSystemPrompt),
		ServiceName:      envOrDefault(lookup, "OTEL_SERVICE_NAME", "llm-agent-customer-support"),
		OTLPProtocol:     strings.ToLower(envOrDefault(lookup, "OTEL_EXPORTER_OTLP_PROTOCOL", "http")),
		OTLPEndpoint:     envOrDefault(lookup, "OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318"),
		OTLPInsecure:     envBoolOrDefault(lookup, "OTEL_EXPORTER_OTLP_INSECURE", true),
		OpenAIAPIKey:     envOrDefault(lookup, "OPENAI_API_KEY", ""),
		OpenAIBaseURL:    envOrDefault(lookup, "OPENAI_BASE_URL", ""),
		AnthropicAPIKey:  envOrDefault(lookup, "ANTHROPIC_API_KEY", ""),
		AnthropicBaseURL: envOrDefault(lookup, "ANTHROPIC_BASE_URL", ""),
		OllamaBaseURL:    envOrDefault(lookup, "OLLAMA_HOST", ""),
	}

	switch cfg.Provider {
	case ProviderOpenAI, ProviderAnthropic, ProviderOllama:
	default:
		return Config{}, fmt.Errorf("LLM_PROVIDER %q is invalid", cfg.Provider)
	}
	cfg.EmbeddingProvider = strings.ToLower(envOrDefault(lookup, "EMBEDDING_PROVIDER", defaultEmbeddingProviderForProvider(cfg.Provider)))
	switch cfg.EmbeddingProvider {
	case ProviderOpenAI, ProviderAnthropic, ProviderOllama:
	default:
		return Config{}, fmt.Errorf("EMBEDDING_PROVIDER %q is invalid", cfg.EmbeddingProvider)
	}

	cfg.Model = envOrDefault(lookup, "LLM_MODEL", defaultModelForProvider(cfg.Provider))
	cfg.EmbeddingModel = envOrDefault(lookup, "EMBEDDING_MODEL", defaultEmbeddingModelForProvider(cfg.EmbeddingProvider))
	cfg.ShutdownTimeout = envDurationOrDefault(lookup, "SHUTDOWN_TIMEOUT", 5*time.Second)
	return cfg, nil
}

func defaultModelForProvider(provider string) string {
	switch provider {
	case ProviderOpenAI:
		return "gpt-4o-mini"
	case ProviderAnthropic:
		return "claude-3-5-haiku-20241022"
	default:
		return "llama3.1:8b"
	}
}

func defaultEmbeddingProviderForProvider(provider string) string {
	switch provider {
	case ProviderAnthropic:
		return ProviderOpenAI
	default:
		return ProviderOllama
	}
}

func defaultEmbeddingModelForProvider(provider string) string {
	switch provider {
	case ProviderOpenAI:
		return "text-embedding-3-small"
	case ProviderAnthropic:
		return "unsupported"
	default:
		return "nomic-embed-text"
	}
}

func envOrDefault(lookup func(string) (string, bool), key, fallback string) string {
	if v, ok := lookup(key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

func envBoolOrDefault(lookup func(string) (string, bool), key string, fallback bool) bool {
	v, ok := lookup(key)
	if !ok || strings.TrimSpace(v) == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func envDurationOrDefault(lookup func(string) (string, bool), key string, fallback time.Duration) time.Duration {
	v, ok := lookup(key)
	if !ok || strings.TrimSpace(v) == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}
