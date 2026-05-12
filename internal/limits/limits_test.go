package limits

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	agents "github.com/costa92/llm-agent"
)

func TestGuard_PreflightRejectsRequestOverTokenCap(t *testing.T) {
	guard := New(Config{
		MaxTokensPerRequest:       3,
		MaxToolCallsPerAgentLoop:  4,
		MaxRequestsPerIPPerMinute: 10,
		RetryMaxAttempts:          2,
		DailyTokenBudget:          100,
	})

	err := guard.Preflight(context.Background(), CheckInput{
		Message:    "one two three four",
		RemoteAddr: "127.0.0.1:9000",
	})
	if err == nil {
		t.Fatal("Preflight() error = nil, want token-cap error")
	}
	if got := HTTPStatus(err); got != http.StatusTooManyRequests {
		t.Fatalf("HTTPStatus() = %d, want %d", got, http.StatusTooManyRequests)
	}
}

func TestGuard_PreflightRejectsRateLimit(t *testing.T) {
	guard := New(Config{
		MaxTokensPerRequest:       100,
		MaxToolCallsPerAgentLoop:  4,
		MaxRequestsPerIPPerMinute: 1,
		RetryMaxAttempts:          2,
		DailyTokenBudget:          100,
	})

	in := CheckInput{Message: "hi", RemoteAddr: "127.0.0.1:9000"}
	if err := guard.Preflight(context.Background(), in); err != nil {
		t.Fatalf("first Preflight() error = %v", err)
	}
	err := guard.Preflight(context.Background(), in)
	if err == nil {
		t.Fatal("second Preflight() error = nil, want rate-limit error")
	}
	if got := HTTPStatus(err); got != http.StatusTooManyRequests {
		t.Fatalf("HTTPStatus() = %d, want %d", got, http.StatusTooManyRequests)
	}
}

func TestGuard_DisableLLMFlipsWithoutRestart(t *testing.T) {
	var disabled atomic.Bool
	guard := New(Config{
		MaxTokensPerRequest:       100,
		MaxToolCallsPerAgentLoop:  4,
		MaxRequestsPerIPPerMinute: 10,
		RetryMaxAttempts:          2,
		DailyTokenBudget:          100,
		DisableLLMVar:             "DISABLE_LLM",
	}, WithEnvLookup(func(key string) (string, bool) {
		if key != "DISABLE_LLM" || !disabled.Load() {
			return "", false
		}
		return "1", true
	}))

	in := CheckInput{Message: "hi", RemoteAddr: "127.0.0.1:9000"}
	if err := guard.Preflight(context.Background(), in); err != nil {
		t.Fatalf("Preflight() before disable error = %v", err)
	}

	disabled.Store(true)

	err := guard.Preflight(context.Background(), in)
	if err == nil {
		t.Fatal("Preflight() after disable error = nil, want disabled error")
	}
	if got := HTTPStatus(err); got != http.StatusServiceUnavailable {
		t.Fatalf("HTTPStatus() = %d, want %d", got, http.StatusServiceUnavailable)
	}
}

func TestGuard_WrappedAgentRejectsToolLoopOverflow(t *testing.T) {
	guard := New(Config{
		MaxTokensPerRequest:       100,
		MaxToolCallsPerAgentLoop:  1,
		MaxRequestsPerIPPerMinute: 10,
		RetryMaxAttempts:          2,
		DailyTokenBudget:          100,
	})
	agent := guard.WrapAgent(staticAgent{
		result: agents.Result{
			Answer: "too many tools",
			Trace: []agents.Step{
				{Kind: agents.StepAction, Tool: "a"},
				{Kind: agents.StepAction, Tool: "b"},
			},
			Usage: agents.Usage{LLMCalls: 1, Tokens: 8},
		},
	})

	_, err := agent.Run(context.Background(), "refund order 123")
	if err == nil {
		t.Fatal("Run() error = nil, want tool-loop cap error")
	}
	if got := HTTPStatus(err); got != http.StatusTooManyRequests {
		t.Fatalf("HTTPStatus() = %d, want %d", got, http.StatusTooManyRequests)
	}
}

func TestGuard_WrappedAgentRejectsRetryAttemptsOverflow(t *testing.T) {
	guard := New(Config{
		MaxTokensPerRequest:       100,
		MaxToolCallsPerAgentLoop:  4,
		MaxRequestsPerIPPerMinute: 10,
		RetryMaxAttempts:          1,
		DailyTokenBudget:          100,
	})
	agent := guard.WrapAgent(staticAgent{
		result: agents.Result{
			Answer: "too many attempts",
			Usage:  agents.Usage{LLMCalls: 2, Tokens: 8},
		},
	})

	_, err := agent.Run(context.Background(), "refund order 123")
	if err == nil {
		t.Fatal("Run() error = nil, want retry-attempt cap error")
	}
	if got := HTTPStatus(err); got != http.StatusTooManyRequests {
		t.Fatalf("HTTPStatus() = %d, want %d", got, http.StatusTooManyRequests)
	}
}

func TestGuard_DailyBudgetCarriesAcrossRequests(t *testing.T) {
	guard := New(Config{
		MaxTokensPerRequest:       100,
		MaxToolCallsPerAgentLoop:  4,
		MaxRequestsPerIPPerMinute: 10,
		RetryMaxAttempts:          2,
		DailyTokenBudget:          5,
	})
	agent := guard.WrapAgent(staticAgent{
		result: agents.Result{
			Answer: "ok",
			Usage:  agents.Usage{LLMCalls: 1, Tokens: 3},
		},
	})
	in := CheckInput{Message: "one two", RemoteAddr: "127.0.0.1:9000"}

	if err := guard.Preflight(context.Background(), in); err != nil {
		t.Fatalf("first Preflight() error = %v", err)
	}
	if _, err := agent.Run(context.Background(), in.Message); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if err := guard.Preflight(context.Background(), in); err != nil {
		t.Fatalf("second Preflight() before budget edge error = %v", err)
	}

	err := guard.Preflight(context.Background(), CheckInput{
		Message:    "three four five",
		RemoteAddr: "127.0.0.1:9000",
	})
	if err == nil {
		t.Fatal("third Preflight() error = nil, want daily budget error")
	}
	if got := HTTPStatus(err); got != http.StatusTooManyRequests {
		t.Fatalf("HTTPStatus() = %d, want %d", got, http.StatusTooManyRequests)
	}
}

type staticAgent struct {
	result agents.Result
	err    error
}

func (a staticAgent) Name() string { return "static-agent" }

func (a staticAgent) Run(context.Context, string) (agents.Result, error) {
	return a.result, a.err
}

func (a staticAgent) RunStream(context.Context, string) (<-chan agents.StepEvent, error) {
	ch := make(chan agents.StepEvent, 1)
	go func() {
		defer close(ch)
		if a.err != nil {
			ch <- agents.StepEvent{Done: true, Err: a.err}
			return
		}
		ch <- agents.StepEvent{Done: true, Final: &a.result}
	}()
	return ch, nil
}

var _ agents.Agent = staticAgent{}

func TestHTTPStatus_DefaultsToInternalServerError(t *testing.T) {
	if got := HTTPStatus(errors.New("boom")); got != http.StatusInternalServerError {
		t.Fatalf("HTTPStatus() = %d, want %d", got, http.StatusInternalServerError)
	}
}
