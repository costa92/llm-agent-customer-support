package providers

import (
	"fmt"

	"github.com/costa92/llm-agent-customer-support/internal/config"
	anthropicprovider "github.com/costa92/llm-agent-providers/anthropic"
	ollamaprovider "github.com/costa92/llm-agent-providers/ollama"
	openaiprovider "github.com/costa92/llm-agent-providers/openai"
	"github.com/costa92/llm-agent-contract/llm"
)

func NewChatModel(cfg config.Config) (llm.ChatModel, error) {
	switch cfg.Provider {
	case config.ProviderOpenAI:
		return openaiprovider.New(
			openaiprovider.WithModel(cfg.Model),
			openaiprovider.WithAPIKey(cfg.OpenAIAPIKey),
			openaiprovider.WithBaseURL(cfg.OpenAIBaseURL),
		)
	case config.ProviderAnthropic:
		return anthropicprovider.New(
			anthropicprovider.WithModel(cfg.Model),
			anthropicprovider.WithAPIKey(cfg.AnthropicAPIKey),
			anthropicprovider.WithBaseURL(cfg.AnthropicBaseURL),
		)
	case config.ProviderOllama:
		opts := []ollamaprovider.Option{ollamaprovider.WithModel(cfg.Model)}
		if cfg.OllamaBaseURL != "" {
			opts = append(opts, ollamaprovider.WithBaseURL(cfg.OllamaBaseURL))
		}
		return ollamaprovider.New(opts...)
	default:
		return nil, fmt.Errorf("unsupported provider %q", cfg.Provider)
	}
}

func NewEmbedder(cfg config.Config) (llm.Embedder, error) {
	switch cfg.EmbeddingProvider {
	case config.ProviderOpenAI:
		return openaiprovider.New(
			openaiprovider.WithModel(cfg.EmbeddingModel),
			openaiprovider.WithAPIKey(cfg.OpenAIAPIKey),
			openaiprovider.WithBaseURL(cfg.OpenAIBaseURL),
		)
	case config.ProviderOllama:
		opts := []ollamaprovider.Option{ollamaprovider.WithModel(cfg.EmbeddingModel)}
		if cfg.OllamaBaseURL != "" {
			opts = append(opts, ollamaprovider.WithBaseURL(cfg.OllamaBaseURL))
		}
		return ollamaprovider.New(opts...)
	case config.ProviderAnthropic:
		return nil, fmt.Errorf("embedding provider %q is unsupported in v0.3", cfg.EmbeddingProvider)
	default:
		return nil, fmt.Errorf("unsupported embedding provider %q", cfg.EmbeddingProvider)
	}
}
