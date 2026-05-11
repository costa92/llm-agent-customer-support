package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agents "github.com/costa92/llm-agent"
	"github.com/costa92/llm-agent-customer-support/internal/config"
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
		HTTPAddr:        "127.0.0.1:0",
		Provider:        config.ProviderOllama,
		Model:           "scripted-support",
		SystemPrompt:    "You are support",
		ShutdownTimeout: 200 * time.Millisecond,
	}

	app, err := New(context.Background(), cfg, WithModelFactory(func(config.Config) (llm.ChatModel, error) {
		return llm.NewScriptedLLM(
			llm.WithProvider("scripted"),
			llm.WithModel("scripted-support"),
			llm.WithResponses(llm.TextResponse("hello from support")),
		), nil
	}), WithTracerProviderFactory(func(context.Context, config.Config) (TracerProvider, error) {
		return &shutdownTracker{TracerProvider: noop.NewTracerProvider()}, nil
	}))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if app.Agent() == nil {
		t.Fatal("Agent() = nil, want runnable agent")
	}
	res, err := app.Agent().Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Agent().Run() error = %v", err)
	}
	if res.Answer != "hello from support" {
		t.Fatalf("Agent().Run() answer = %q, want %q", res.Answer, "hello from support")
	}
	if app.ModelInfo().Provider != "scripted" {
		t.Fatalf("ModelInfo().Provider = %q, want %q", app.ModelInfo().Provider, "scripted")
	}
}

func TestRun_ShutsDownTracerProviderOnCancel(t *testing.T) {
	tracker := &shutdownTracker{TracerProvider: noop.NewTracerProvider()}
	cfg := config.Config{
		HTTPAddr:        "127.0.0.1:0",
		Provider:        config.ProviderOllama,
		Model:           "scripted-support",
		SystemPrompt:    "You are support",
		ShutdownTimeout: 500 * time.Millisecond,
	}

	app, err := New(context.Background(), cfg, WithModelFactory(func(config.Config) (llm.ChatModel, error) {
		return llm.NewScriptedLLM(
			llm.WithProvider("scripted"),
			llm.WithModel("scripted-support"),
			llm.WithResponses(llm.TextResponse("ok")),
		), nil
	}), WithTracerProviderFactory(func(context.Context, config.Config) (TracerProvider, error) {
		return tracker, nil
	}))
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
		HTTPAddr:        "127.0.0.1:0",
		Provider:        config.ProviderOllama,
		Model:           "scripted-support",
		SystemPrompt:    "You are support",
		ShutdownTimeout: 200 * time.Millisecond,
	}

	app, err := New(context.Background(), cfg, WithModelFactory(func(config.Config) (llm.ChatModel, error) {
		return llm.NewScriptedLLM(
			llm.WithProvider("scripted"),
			llm.WithModel("scripted-support"),
			llm.WithResponses(llm.TextResponse("route answer"), llm.TextResponse("stream answer")),
		), nil
	}), WithTracerProviderFactory(func(context.Context, config.Config) (TracerProvider, error) {
		return &shutdownTracker{TracerProvider: noop.NewTracerProvider()}, nil
	}))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	app.server.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz status = %d, want %d", rec.Code, http.StatusOK)
	}

	req = httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(`{"message":"hi"}`))
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
	if resp.Answer != "route answer" {
		t.Fatalf("Answer = %q, want %q", resp.Answer, "route answer")
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

var _ TracerProvider = (*shutdownTracker)(nil)
var _ agents.Agent = interface{ agents.Agent }(nil)
