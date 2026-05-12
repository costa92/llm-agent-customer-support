package limits

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	agents "github.com/costa92/llm-agent"
)

type Config struct {
	MaxTokensPerRequest       int
	MaxToolCallsPerAgentLoop  int
	MaxRequestsPerIPPerMinute int
	RetryMaxAttempts          int
	DailyTokenBudget          int
	DisableLLMVar             string
}

type CheckInput struct {
	Message    string
	RemoteAddr string
}

type Guard struct {
	cfg       Config
	envLookup func(string) (string, bool)

	mu          sync.Mutex
	requests    map[string]*bucket
	dailyTokens int
}

type bucket struct {
	minute int64
	count  int
}

type Option func(*Guard)

type limitError struct {
	status int
	msg    string
}

func (e *limitError) Error() string { return e.msg }

func New(cfg Config, opts ...Option) *Guard {
	if cfg.DisableLLMVar == "" {
		cfg.DisableLLMVar = "DISABLE_LLM"
	}
	g := &Guard{
		cfg:       cfg,
		envLookup: os.LookupEnv,
		requests:  map[string]*bucket{},
	}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

func WithEnvLookup(lookup func(string) (string, bool)) Option {
	return func(g *Guard) {
		if lookup != nil {
			g.envLookup = lookup
		}
	}
}

func (g *Guard) Preflight(_ context.Context, in CheckInput) error {
	if g.disabled() {
		return &limitError{status: http.StatusServiceUnavailable, msg: "llm is disabled"}
	}
	tokens := estimateTokens(in.Message)
	if g.cfg.MaxTokensPerRequest > 0 && tokens > g.cfg.MaxTokensPerRequest {
		return &limitError{status: http.StatusTooManyRequests, msg: "request exceeds max tokens per request"}
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.cfg.DailyTokenBudget > 0 && g.dailyTokens+tokens > g.cfg.DailyTokenBudget {
		return &limitError{status: http.StatusTooManyRequests, msg: "daily token budget exceeded"}
	}

	if g.cfg.MaxRequestsPerIPPerMinute > 0 {
		host := remoteHost(in.RemoteAddr)
		nowMinute := time.Now().UTC().Unix() / 60
		b := g.requests[host]
		if b == nil || b.minute != nowMinute {
			b = &bucket{minute: nowMinute}
			g.requests[host] = b
		}
		if b.count >= g.cfg.MaxRequestsPerIPPerMinute {
			return &limitError{status: http.StatusTooManyRequests, msg: "rate limit exceeded"}
		}
		b.count++
	}
	return nil
}

func (g *Guard) WrapAgent(inner agents.Agent) agents.Agent {
	if inner == nil {
		return nil
	}
	return wrappedAgent{inner: inner, guard: g}
}

func HTTPStatus(err error) int {
	var le *limitError
	if errors.As(err, &le) {
		return le.status
	}
	return http.StatusInternalServerError
}

func (g *Guard) disabled() bool {
	v, ok := g.envLookup(g.cfg.DisableLLMVar)
	return ok && strings.TrimSpace(v) == "1"
}

func (g *Guard) postflight(res agents.Result) error {
	if g.cfg.MaxToolCallsPerAgentLoop > 0 && countToolCalls(res.Trace) > g.cfg.MaxToolCallsPerAgentLoop {
		return &limitError{status: http.StatusTooManyRequests, msg: "tool loop limit exceeded"}
	}
	if g.cfg.RetryMaxAttempts > 0 && res.Usage.LLMCalls > g.cfg.RetryMaxAttempts {
		return &limitError{status: http.StatusTooManyRequests, msg: "retry attempts limit exceeded"}
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.dailyTokens += res.Usage.Tokens
	return nil
}

type wrappedAgent struct {
	inner agents.Agent
	guard *Guard
}

func (a wrappedAgent) Name() string { return a.inner.Name() }

func (a wrappedAgent) Run(ctx context.Context, input string) (agents.Result, error) {
	if a.guard.disabled() {
		return agents.Result{}, &limitError{status: http.StatusServiceUnavailable, msg: "llm is disabled"}
	}
	res, err := a.inner.Run(ctx, input)
	if err != nil {
		return agents.Result{}, err
	}
	if err := a.guard.postflight(res); err != nil {
		return agents.Result{}, err
	}
	return res, nil
}

func (a wrappedAgent) RunStream(ctx context.Context, input string) (<-chan agents.StepEvent, error) {
	if a.guard.disabled() {
		ch := make(chan agents.StepEvent, 1)
		go func() {
			defer close(ch)
			ch <- agents.StepEvent{Done: true, Err: &limitError{status: http.StatusServiceUnavailable, msg: "llm is disabled"}}
		}()
		return ch, nil
	}
	return a.inner.RunStream(ctx, input)
}

func estimateTokens(text string) int {
	return len(strings.Fields(strings.TrimSpace(text)))
}

func countToolCalls(trace []agents.Step) int {
	count := 0
	for _, step := range trace {
		if step.Kind == agents.StepAction {
			count++
		}
	}
	return count
}

func remoteHost(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err == nil {
		return host
	}
	return addr
}
