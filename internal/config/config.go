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
	HTTPAddr                  string
	Provider                  string
	Model                     string
	EmbeddingProvider         string
	EmbeddingModel            string
	SessionBackend            string
	SessionDSN                string
	MaxTokensPerRequest       int
	MaxToolCallsPerAgentLoop  int
	MaxRequestsPerIPPerMinute int
	RetryMaxAttempts          int
	DailyTokenBudget          int
	DisableLLM                bool
	SystemPrompt              string
	ServiceName               string
	ShutdownTimeout           time.Duration

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
		HTTPAddr:                  envOrDefault(lookup, "HTTP_ADDR", ":8080"),
		Provider:                  strings.ToLower(envOrDefault(lookup, "LLM_PROVIDER", ProviderOllama)),
		SessionBackend:            strings.ToLower(envOrDefault(lookup, "SESSION_BACKEND", "sqlite")),
		SystemPrompt:              envOrDefault(lookup, "SYSTEM_PROMPT", defaultSystemPrompt),
		ServiceName:               envOrDefault(lookup, "OTEL_SERVICE_NAME", "llm-agent-customer-support"),
		OTLPProtocol:              strings.ToLower(envOrDefault(lookup, "OTEL_EXPORTER_OTLP_PROTOCOL", "http")),
		OTLPEndpoint:              envOrDefault(lookup, "OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318"),
		OTLPInsecure:              envBoolOrDefault(lookup, "OTEL_EXPORTER_OTLP_INSECURE", true),
		OpenAIAPIKey:              envOrDefault(lookup, "OPENAI_API_KEY", ""),
		OpenAIBaseURL:             envOrDefault(lookup, "OPENAI_BASE_URL", ""),
		AnthropicAPIKey:           envOrDefault(lookup, "ANTHROPIC_API_KEY", ""),
		AnthropicBaseURL:          envOrDefault(lookup, "ANTHROPIC_BASE_URL", ""),
		OllamaBaseURL:             envOrDefault(lookup, "OLLAMA_HOST", ""),
		MaxTokensPerRequest:       envIntOrDefault(lookup, "MAX_TOKENS_PER_REQUEST", 1024),
		MaxToolCallsPerAgentLoop:  envIntOrDefault(lookup, "MAX_TOOL_CALLS_PER_AGENT_LOOP", 4),
		MaxRequestsPerIPPerMinute: envIntOrDefault(lookup, "MAX_REQUESTS_PER_IP_PER_MINUTE", 60),
		RetryMaxAttempts:          envIntOrDefault(lookup, "RETRY_MAX_ATTEMPTS", 2),
		DailyTokenBudget:          envIntOrDefault(lookup, "DAILY_TOKEN_BUDGET", 100000),
		DisableLLM:                envBoolOrDefault(lookup, "DISABLE_LLM", false),
	}

	switch cfg.Provider {
	case ProviderOpenAI, ProviderAnthropic, ProviderOllama:
	default:
		return Config{}, fmt.Errorf("LLM_PROVIDER %q is invalid", cfg.Provider)
	}
	switch cfg.SessionBackend {
	case "sqlite", "postgres":
	default:
		return Config{}, fmt.Errorf("SESSION_BACKEND %q is invalid", cfg.SessionBackend)
	}
	cfg.EmbeddingProvider = strings.ToLower(envOrDefault(lookup, "EMBEDDING_PROVIDER", defaultEmbeddingProviderForProvider(cfg.Provider)))
	switch cfg.EmbeddingProvider {
	case ProviderOpenAI, ProviderAnthropic, ProviderOllama:
	default:
		return Config{}, fmt.Errorf("EMBEDDING_PROVIDER %q is invalid", cfg.EmbeddingProvider)
	}

	cfg.Model = envOrDefault(lookup, "LLM_MODEL", defaultModelForProvider(cfg.Provider))
	cfg.EmbeddingModel = envOrDefault(lookup, "EMBEDDING_MODEL", defaultEmbeddingModelForProvider(cfg.EmbeddingProvider))
	cfg.SessionDSN = envOrDefault(lookup, "SESSION_DSN", defaultSessionDSNForBackend(cfg.SessionBackend))
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

func defaultSessionDSNForBackend(backend string) string {
	if backend == "postgres" {
		return "postgres://localhost:5432/llm_agent_customer_support?sslmode=disable"
	}
	return "file:support_sessions.db?_pragma=journal_mode(WAL)"
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

func envIntOrDefault(lookup func(string) (string, bool), key string, fallback int) int {
	v, ok := lookup(key)
	if !ok || strings.TrimSpace(v) == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
