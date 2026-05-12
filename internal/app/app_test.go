package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	agents "github.com/costa92/llm-agent"
	"github.com/costa92/llm-agent-customer-support/internal/config"
	"github.com/costa92/llm-agent-customer-support/internal/sessionstore"
	"github.com/costa92/llm-agent/llm"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestDefaultModelFactory_SelectsProvider(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
	}{
		{name: "openai", provider: config.ProviderOpenAI, model: "gpt-4o-mini"},
		{name: "anthropic", provider: config.ProviderAnthropic, model: "claude-3-5-haiku-20241022"},
		{name: "ollama", provider: config.ProviderOllama, model: "llama3.1:8b"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			model, err := DefaultModelFactory(config.Config{
				Provider:         tc.provider,
				Model:            tc.model,
				OpenAIBaseURL:    "http://example.com",
				AnthropicBaseURL: "http://example.com",
				OllamaBaseURL:    "http://example.com",
			})
			if err != nil {
				t.Fatalf("DefaultModelFactory() error = %v", err)
			}
			info := model.Info()
			if info.Provider != tc.provider {
				t.Fatalf("Info().Provider = %q, want %q", info.Provider, tc.provider)
			}
			if info.Model != tc.model {
				t.Fatalf("Info().Model = %q, want %q", info.Model, tc.model)
			}
		})
	}
}

func TestNew_BuildsRunnableAgent(t *testing.T) {
	cfg := config.Config{
		HTTPAddr:          "127.0.0.1:0",
		Provider:          config.ProviderOllama,
		Model:             "scripted-support",
		EmbeddingProvider: config.ProviderOpenAI,
		EmbeddingModel:    "scripted-embed",
		SystemPrompt:      "You are support",
		ShutdownTimeout:   200 * time.Millisecond,
	}

	app, err := New(context.Background(), cfg,
		WithModelFactory(func(config.Config) (llm.ChatModel, error) {
			return llm.NewScriptedLLM(
				llm.WithProvider("scripted"),
				llm.WithModel("scripted-support"),
				llm.WithCapabilities(llm.Capabilities{Tools: true, Embeddings: true}),
				llm.WithResponses(llm.ToolCallResponse("refund_policy", `{"order_id":"123"}`)),
			), nil
		}),
		WithEmbedderFactory(func(config.Config) (llm.Embedder, error) {
			return llm.NewScriptedLLM(
				llm.WithProvider("scripted-embed"),
				llm.WithModel("scripted-embed"),
				llm.WithEmbedDimensions(8),
			), nil
		}),
		WithSessionStoreFactory(newTestSessionStoreFactory(t)),
		WithTracerProviderFactory(func(context.Context, config.Config) (TracerProvider, error) {
			return &shutdownTracker{TracerProvider: noop.NewTracerProvider()}, nil
		}),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if app.Agent() == nil {
		t.Fatal("Agent() = nil, want runnable agent")
	}
	res, err := app.Agent().Run(context.Background(), "I need a refund for order 123")
	if err != nil {
		t.Fatalf("Agent().Run() error = %v", err)
	}
	if !strings.Contains(strings.ToLower(res.Answer), "refund guidance") {
		t.Fatalf("Agent().Run() answer = %q, want refund guidance", res.Answer)
	}
	if !strings.Contains(res.Answer, "24h") {
		t.Fatalf("Agent().Run() answer = %q, want seeded policy evidence", res.Answer)
	}
	if app.ModelInfo().Provider != "scripted" {
		t.Fatalf("ModelInfo().Provider = %q, want %q", app.ModelInfo().Provider, "scripted")
	}
	if app.EmbeddingInfo().Provider != "scripted-embed" {
		t.Fatalf("EmbeddingInfo().Provider = %q, want %q", app.EmbeddingInfo().Provider, "scripted-embed")
	}
	if app.EmbeddingInfo().Model != "scripted-embed" {
		t.Fatalf("EmbeddingInfo().Model = %q, want %q", app.EmbeddingInfo().Model, "scripted-embed")
	}
}

func TestDefaultEmbedderFactory_SelectsProvider(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
	}{
		{name: "openai", provider: config.ProviderOpenAI, model: "text-embedding-3-small"},
		{name: "ollama", provider: config.ProviderOllama, model: "nomic-embed-text"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			embedder, err := DefaultEmbedderFactory(config.Config{
				EmbeddingProvider: tc.provider,
				EmbeddingModel:    tc.model,
				OpenAIBaseURL:     "http://example.com",
				OllamaBaseURL:     "http://example.com",
			})
			if err != nil {
				t.Fatalf("DefaultEmbedderFactory() error = %v", err)
			}
			infoProvider, ok := embedder.(interface{ Info() llm.ProviderInfo })
			if !ok {
				t.Fatal("embedder does not expose Info()")
			}
			info := infoProvider.Info()
			if info.Provider != tc.provider {
				t.Fatalf("Info().Provider = %q, want %q", info.Provider, tc.provider)
			}
			if info.Model != tc.model {
				t.Fatalf("Info().Model = %q, want %q", info.Model, tc.model)
			}
			if !info.Capabilities.Embeddings {
				t.Fatal("Info().Capabilities.Embeddings = false, want true")
			}
		})
	}
}

func TestDefaultEmbedderFactory_RejectsUnsupportedProvider(t *testing.T) {
	_, err := DefaultEmbedderFactory(config.Config{
		EmbeddingProvider: config.ProviderAnthropic,
		EmbeddingModel:    "claude-3-5-haiku-20241022",
	})
	if err == nil {
		t.Fatal("DefaultEmbedderFactory() error = nil, want unsupported provider error")
	}
	if !strings.Contains(err.Error(), "embedding provider") {
		t.Fatalf("DefaultEmbedderFactory() error = %q, want mention of embedding provider", err)
	}
}

func TestRun_ShutsDownTracerProviderOnCancel(t *testing.T) {
	tracker := &shutdownTracker{TracerProvider: noop.NewTracerProvider()}
	cfg := config.Config{
		HTTPAddr:          "127.0.0.1:0",
		Provider:          config.ProviderOllama,
		Model:             "scripted-support",
		EmbeddingProvider: config.ProviderOllama,
		EmbeddingModel:    "scripted-embed",
		SystemPrompt:      "You are support",
		ShutdownTimeout:   500 * time.Millisecond,
	}

	app, err := New(context.Background(), cfg,
		WithModelFactory(func(config.Config) (llm.ChatModel, error) {
			return llm.NewScriptedLLM(
				llm.WithProvider("scripted"),
				llm.WithModel("scripted-support"),
				llm.WithCapabilities(llm.Capabilities{Tools: true, Embeddings: true}),
			), nil
		}),
		WithEmbedderFactory(func(config.Config) (llm.Embedder, error) {
			return llm.NewScriptedLLM(
				llm.WithProvider("scripted-embed"),
				llm.WithModel("scripted-embed"),
				llm.WithEmbedDimensions(8),
			), nil
		}),
		WithSessionStoreFactory(newTestSessionStoreFactory(t)),
		WithTracerProviderFactory(func(context.Context, config.Config) (TracerProvider, error) {
			return tracker, nil
		}),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- app.Run(ctx)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return after context cancellation")
	}

	if !tracker.shutdownCalled {
		t.Fatal("tracer provider shutdown was not called")
	}
}

func TestNew_RegistersTransportRoutes(t *testing.T) {
	cfg := config.Config{
		HTTPAddr:          "127.0.0.1:0",
		Provider:          config.ProviderOllama,
		Model:             "scripted-support",
		EmbeddingProvider: config.ProviderOpenAI,
		EmbeddingModel:    "scripted-embed",
		SystemPrompt:      "You are support",
		ShutdownTimeout:   200 * time.Millisecond,
	}

	app, err := New(context.Background(), cfg,
		WithModelFactory(func(config.Config) (llm.ChatModel, error) {
			return llm.NewScriptedLLM(
				llm.WithProvider("scripted"),
				llm.WithModel("scripted-support"),
				llm.WithCapabilities(llm.Capabilities{Tools: true, Embeddings: true}),
				llm.WithResponses(
					llm.ToolCallResponse("refund_policy", `{"order_id":"123"}`),
					llm.ToolCallResponse("refund_policy", `{"order_id":"123"}`),
				),
			), nil
		}),
		WithEmbedderFactory(func(config.Config) (llm.Embedder, error) {
			return llm.NewScriptedLLM(
				llm.WithProvider("scripted-embed"),
				llm.WithModel("scripted-embed"),
				llm.WithEmbedDimensions(8),
			), nil
		}),
		WithSessionStoreFactory(newTestSessionStoreFactory(t)),
		WithTracerProviderFactory(func(context.Context, config.Config) (TracerProvider, error) {
			return &shutdownTracker{TracerProvider: noop.NewTracerProvider()}, nil
		}),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	app.server.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz status = %d, want %d", rec.Code, http.StatusOK)
	}

	req = httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(`{"message":"refund order 123"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	app.server.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/chat status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.Contains(resp.Answer, "24h") {
		t.Fatalf("Answer = %q, want seeded refund policy answer", resp.Answer)
	}
}

func TestNew_MissingOrderIDRequestsMoreInfo(t *testing.T) {
	cfg := config.Config{
		HTTPAddr:          "127.0.0.1:0",
		Provider:          config.ProviderOllama,
		Model:             "scripted-support",
		EmbeddingProvider: config.ProviderOpenAI,
		EmbeddingModel:    "scripted-embed",
		SystemPrompt:      "You are support",
		ShutdownTimeout:   200 * time.Millisecond,
	}

	app, err := New(context.Background(), cfg,
		WithModelFactory(func(config.Config) (llm.ChatModel, error) {
			return llm.NewScriptedLLM(
				llm.WithProvider("scripted"),
				llm.WithModel("scripted-support"),
				llm.WithCapabilities(llm.Capabilities{Tools: true, Embeddings: true}),
			), nil
		}),
		WithEmbedderFactory(func(config.Config) (llm.Embedder, error) {
			return llm.NewScriptedLLM(
				llm.WithProvider("scripted-embed"),
				llm.WithModel("scripted-embed"),
				llm.WithEmbedDimensions(8),
			), nil
		}),
		WithSessionStoreFactory(newTestSessionStoreFactory(t)),
		WithTracerProviderFactory(func(context.Context, config.Config) (TracerProvider, error) {
			return &shutdownTracker{TracerProvider: noop.NewTracerProvider()}, nil
		}),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	res, err := app.Agent().Run(context.Background(), "I need a refund")
	if err != nil {
		t.Fatalf("Agent().Run() error = %v", err)
	}
	if !strings.Contains(strings.ToLower(res.Answer), "order id") {
		t.Fatalf("Agent().Run() answer = %q, want order ID clarification", res.Answer)
	}
}

func TestNew_ChargebackEscalatesToHuman(t *testing.T) {
	cfg := config.Config{
		HTTPAddr:          "127.0.0.1:0",
		Provider:          config.ProviderOllama,
		Model:             "scripted-support",
		EmbeddingProvider: config.ProviderOpenAI,
		EmbeddingModel:    "scripted-embed",
		SystemPrompt:      "You are support",
		ShutdownTimeout:   200 * time.Millisecond,
	}

	app, err := New(context.Background(), cfg,
		WithModelFactory(func(config.Config) (llm.ChatModel, error) {
			return llm.NewScriptedLLM(
				llm.WithProvider("scripted"),
				llm.WithModel("scripted-support"),
				llm.WithCapabilities(llm.Capabilities{Tools: true, Embeddings: true}),
			), nil
		}),
		WithEmbedderFactory(func(config.Config) (llm.Embedder, error) {
			return llm.NewScriptedLLM(
				llm.WithProvider("scripted-embed"),
				llm.WithModel("scripted-embed"),
				llm.WithEmbedDimensions(8),
			), nil
		}),
		WithSessionStoreFactory(newTestSessionStoreFactory(t)),
		WithTracerProviderFactory(func(context.Context, config.Config) (TracerProvider, error) {
			return &shutdownTracker{TracerProvider: noop.NewTracerProvider()}, nil
		}),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	res, err := app.Agent().Run(context.Background(), "This is a chargeback on order 123")
	if err != nil {
		t.Fatalf("Agent().Run() error = %v", err)
	}
	if !strings.Contains(strings.ToLower(res.Answer), "human") {
		t.Fatalf("Agent().Run() answer = %q, want human escalation", res.Answer)
	}
}

func TestNew_DisableLLMFlipsWithoutRestart(t *testing.T) {
	t.Setenv("DISABLE_LLM", "")
	cfg := config.Config{
		HTTPAddr:                  "127.0.0.1:0",
		Provider:                  config.ProviderOllama,
		Model:                     "scripted-support",
		EmbeddingProvider:         config.ProviderOpenAI,
		EmbeddingModel:            "scripted-embed",
		SystemPrompt:              "You are support",
		ShutdownTimeout:           200 * time.Millisecond,
		MaxTokensPerRequest:       100,
		MaxToolCallsPerAgentLoop:  4,
		MaxRequestsPerIPPerMinute: 10,
		RetryMaxAttempts:          2,
		DailyTokenBudget:          100,
	}

	app, err := New(context.Background(), cfg,
		WithModelFactory(func(config.Config) (llm.ChatModel, error) {
			return llm.NewScriptedLLM(
				llm.WithProvider("scripted"),
				llm.WithModel("scripted-support"),
				llm.WithCapabilities(llm.Capabilities{Tools: true, Embeddings: true}),
				llm.WithResponses(llm.ToolCallResponse("refund_policy", `{"order_id":"123"}`)),
			), nil
		}),
		WithEmbedderFactory(func(config.Config) (llm.Embedder, error) {
			return llm.NewScriptedLLM(
				llm.WithProvider("scripted-embed"),
				llm.WithModel("scripted-embed"),
				llm.WithEmbedDimensions(8),
			), nil
		}),
		WithSessionStoreFactory(newTestSessionStoreFactory(t)),
		WithTracerProviderFactory(func(context.Context, config.Config) (TracerProvider, error) {
			return &shutdownTracker{TracerProvider: noop.NewTracerProvider()}, nil
		}),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(`{"message":"refund order 123"}`))
	req.RemoteAddr = "127.0.0.1:9000"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.server.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d", rec.Code, http.StatusOK)
	}

	if err := os.Setenv("DISABLE_LLM", "1"); err != nil {
		t.Fatalf("Setenv() error = %v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(`{"message":"refund order 123"}`))
	req.RemoteAddr = "127.0.0.1:9000"
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	app.server.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("second status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

type shutdownTracker struct {
	trace.TracerProvider
	shutdownCalled bool
}

func (s *shutdownTracker) Shutdown(context.Context) error {
	s.shutdownCalled = true
	return nil
}

func newTestSessionStoreFactory(t *testing.T) SessionStoreFactory {
	t.Helper()
	return func(context.Context, config.Config) (sessionstore.Store, error) {
		return sessionstore.OpenSQLite(context.Background(), "file::memory:?cache=shared")
	}
}

var _ TracerProvider = (*shutdownTracker)(nil)
var _ agents.Agent = interface{ agents.Agent }(nil)
