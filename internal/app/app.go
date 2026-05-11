package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	agents "github.com/costa92/llm-agent"
	"github.com/costa92/llm-agent-customer-support/internal/config"
	"github.com/costa92/llm-agent-customer-support/internal/httpapi"
	otelroot "github.com/costa92/llm-agent-otel"
	"github.com/costa92/llm-agent-otel/otelagent"
	"github.com/costa92/llm-agent-otel/otelmodel"
	anthropicprovider "github.com/costa92/llm-agent-providers/anthropic"
	ollamaprovider "github.com/costa92/llm-agent-providers/ollama"
	openaiprovider "github.com/costa92/llm-agent-providers/openai"
	"github.com/costa92/llm-agent/llm"
	"go.opentelemetry.io/otel/trace"
)

type TracerProvider interface {
	trace.TracerProvider
	Shutdown(context.Context) error
}

type ModelFactory func(config.Config) (llm.ChatModel, error)
type TracerProviderFactory func(context.Context, config.Config) (TracerProvider, error)

type Options struct {
	ModelFactory          ModelFactory
	TracerProviderFactory TracerProviderFactory
}

type Option func(*Options)

func WithModelFactory(factory ModelFactory) Option {
	return func(o *Options) { o.ModelFactory = factory }
}

func WithTracerProviderFactory(factory TracerProviderFactory) Option {
	return func(o *Options) { o.TracerProviderFactory = factory }
}

type App struct {
	cfg    config.Config
	server *http.Server
	tp     TracerProvider
	agent  agents.Agent
	model  llm.ChatModel
	mux    *http.ServeMux
}

func New(ctx context.Context, cfg config.Config, opts ...Option) (*App, error) {
	options := Options{
		ModelFactory:          DefaultModelFactory,
		TracerProviderFactory: defaultTracerProviderFactory,
	}
	for _, opt := range opts {
		opt(&options)
	}

	model, err := options.ModelFactory(cfg)
	if err != nil {
		return nil, err
	}
	tp, err := options.TracerProviderFactory(ctx, cfg)
	if err != nil {
		return nil, err
	}

	wrappedModel := otelmodel.Wrap(model, otelmodel.Config{TracerProvider: tp})
	agent := agents.NewSimpleAgent(wrappedModel, agents.SimpleOptions{
		Name:         "customer-support",
		SystemPrompt: cfg.SystemPrompt,
	})
	wrappedAgent := otelagent.Wrap(agent, otelagent.Config{TracerProvider: tp})

	mux := httpapi.NewMux(httpapi.Handlers{
		Agent:  wrappedAgent,
		Ready:  func(context.Context) error { return nil },
		Tracer: tp.Tracer("github.com/costa92/llm-agent-customer-support/httpapi"),
	})

	return &App{
		cfg: cfg,
		server: &http.Server{
			Addr:    cfg.HTTPAddr,
			Handler: mux,
		},
		tp:    tp,
		agent: wrappedAgent,
		model: wrappedModel,
		mux:   mux,
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		err := a.server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		shutdownErr := a.shutdown(context.Background())
		if err != nil {
			return err
		}
		return shutdownErr
	case <-ctx.Done():
		return a.shutdown(context.Background())
	}
}

func (a *App) Agent() agents.Agent { return a.agent }

func (a *App) ModelInfo() llm.ProviderInfo { return a.model.Info() }

func (a *App) shutdown(ctx context.Context) error {
	shutdownCtx, cancel := context.WithTimeout(ctx, a.cfg.ShutdownTimeout)
	defer cancel()

	var firstErr error
	if err := a.server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.Canceled) {
		firstErr = err
	}
	if err := a.tp.Shutdown(shutdownCtx); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func DefaultModelFactory(cfg config.Config) (llm.ChatModel, error) {
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

func defaultTracerProviderFactory(ctx context.Context, cfg config.Config) (TracerProvider, error) {
	tp, err := otelroot.NewTracerProvider(ctx, otelroot.ExporterConfig{
		Protocol: cfg.OTLPProtocol,
		Endpoint: cfg.OTLPEndpoint,
		Insecure: cfg.OTLPInsecure,
	})
	if err != nil {
		return nil, err
	}
	return tp, nil
}
