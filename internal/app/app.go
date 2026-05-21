package app

import (
	"context"
	"errors"
	"net/http"

	agents "github.com/costa92/llm-agent"
	"github.com/costa92/llm-agent-customer-support/internal/config"
	"github.com/costa92/llm-agent-customer-support/internal/guardrails"
	"github.com/costa92/llm-agent-customer-support/internal/httpapi"
	"github.com/costa92/llm-agent-customer-support/internal/knowledgebase"
	"github.com/costa92/llm-agent-customer-support/internal/limits"
	"github.com/costa92/llm-agent-customer-support/internal/providers"
	"github.com/costa92/llm-agent-customer-support/internal/sessionstore"
	"github.com/costa92/llm-agent-customer-support/internal/supportflow"
	otelroot "github.com/costa92/llm-agent-otel"
	"github.com/costa92/llm-agent-otel/otelagent"
	"github.com/costa92/llm-agent-otel/otelmodel"
	"github.com/costa92/llm-agent/llm"
	"go.opentelemetry.io/otel/trace"
)

type TracerProvider interface {
	trace.TracerProvider
	Shutdown(context.Context) error
}

type ModelFactory func(config.Config) (llm.ChatModel, error)
type EmbedderFactory func(config.Config) (llm.Embedder, error)
type SessionStoreFactory func(context.Context, config.Config) (sessionstore.Store, error)
type TracerProviderFactory func(context.Context, config.Config) (TracerProvider, error)

type Options struct {
	ModelFactory          ModelFactory
	EmbedderFactory       EmbedderFactory
	SessionStoreFactory   SessionStoreFactory
	TracerProviderFactory TracerProviderFactory
}

type Option func(*Options)

func WithModelFactory(factory ModelFactory) Option {
	return func(o *Options) { o.ModelFactory = factory }
}

func WithEmbedderFactory(factory EmbedderFactory) Option {
	return func(o *Options) { o.EmbedderFactory = factory }
}

func WithSessionStoreFactory(factory SessionStoreFactory) Option {
	return func(o *Options) { o.SessionStoreFactory = factory }
}

func WithTracerProviderFactory(factory TracerProviderFactory) Option {
	return func(o *Options) { o.TracerProviderFactory = factory }
}

type App struct {
	cfg      config.Config
	server   *http.Server
	tp       TracerProvider
	agent    agents.Agent
	model    llm.ChatModel
	embedder llm.Embedder
	sessions sessionstore.Store
	guard    *limits.Guard
	mux      *http.ServeMux
}

func New(ctx context.Context, cfg config.Config, opts ...Option) (*App, error) {
	options := Options{
		ModelFactory:          DefaultModelFactory,
		EmbedderFactory:       DefaultEmbedderFactory,
		SessionStoreFactory:   defaultSessionStoreFactory,
		TracerProviderFactory: defaultTracerProviderFactory,
	}
	for _, opt := range opts {
		opt(&options)
	}

	model, err := options.ModelFactory(cfg)
	if err != nil {
		return nil, err
	}
	embedder, err := options.EmbedderFactory(cfg)
	if err != nil {
		return nil, err
	}
	sessions, err := options.SessionStoreFactory(ctx, cfg)
	if err != nil {
		return nil, err
	}
	tp, err := options.TracerProviderFactory(ctx, cfg)
	if err != nil {
		_ = sessions.Close()
		return nil, err
	}

	wrappedModel := otelmodel.Wrap(model, otelmodel.Config{TracerProvider: tp})
	knowledge, err := knowledgebase.New(ctx, embedder)
	if err != nil {
		_ = sessions.Close()
		_ = tp.Shutdown(context.Background())
		return nil, err
	}
	agent, err := supportflow.New(supportflow.Options{
		Model:      wrappedModel,
		Knowledge:  knowledge,
		Sessions:   sessions,
		Guardrails: guardrails.New(guardrails.Config{}),
	})
	if err != nil {
		_ = sessions.Close()
		_ = tp.Shutdown(context.Background())
		return nil, err
	}
	guard := limits.New(limits.Config{
		MaxTokensPerRequest:       cfg.MaxTokensPerRequest,
		MaxToolCallsPerAgentLoop:  cfg.MaxToolCallsPerAgentLoop,
		MaxRequestsPerIPPerMinute: cfg.MaxRequestsPerIPPerMinute,
		RetryMaxAttempts:          cfg.RetryMaxAttempts,
		DailyTokenBudget:          cfg.DailyTokenBudget,
		DisableLLMVar:             "DISABLE_LLM",
	})
	guardedAgent := guard.WrapAgent(agent)
	wrappedAgent := otelagent.Wrap(guardedAgent, otelagent.Config{TracerProvider: tp})

	mux := httpapi.NewMux(httpapi.Handlers{
		Agent:  wrappedAgent,
		Guard:  guard,
		Ready:  func(context.Context) error { return nil },
		Tracer: tp.Tracer("github.com/costa92/llm-agent-customer-support/httpapi"),
	})

	return &App{
		cfg: cfg,
		server: &http.Server{
			Addr:    cfg.HTTPAddr,
			Handler: mux,
		},
		tp:       tp,
		agent:    wrappedAgent,
		model:    wrappedModel,
		embedder: embedder,
		sessions: sessions,
		guard:    guard,
		mux:      mux,
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

func (a *App) EmbeddingInfo() llm.ProviderInfo {
	if infoProvider, ok := a.embedder.(interface{ Info() llm.ProviderInfo }); ok {
		return infoProvider.Info()
	}
	return llm.ProviderInfo{}
}

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
	if a.sessions != nil {
		if err := a.sessions.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func DefaultModelFactory(cfg config.Config) (llm.ChatModel, error) {
	return providers.NewChatModel(cfg)
}

func DefaultEmbedderFactory(cfg config.Config) (llm.Embedder, error) {
	return providers.NewEmbedder(cfg)
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

func defaultSessionStoreFactory(ctx context.Context, cfg config.Config) (sessionstore.Store, error) {
	if cfg.SessionBackend == "postgres" {
		return sessionstore.OpenPostgres(ctx, cfg.SessionDSN)
	}
	return sessionstore.OpenSQLite(ctx, cfg.SessionDSN)
}
