package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	agents "github.com/costa92/llm-agent"
	"github.com/costa92/llm-agent-customer-support/internal/limits"
	"github.com/costa92/llm-agent-customer-support/internal/sessionstore"
	"github.com/costa92/llm-agent/llm"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestChatHandler_ReturnsJSONAndTraceHeader(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	otel.SetTracerProvider(tp)

	mux := NewMux(Handlers{
		Agent:  newStubAgent("hello from chat"),
		Ready:  func(context.Context) error { return nil },
		Tracer: tp.Tracer("test"),
	})

	req := httptest.NewRequest(http.MethodPost, "/chat", bytes.NewBufferString(`{"message":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("X-Trace-Id"); got == "" {
		t.Fatal("X-Trace-Id header is empty")
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}

	var resp ChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Answer != "hello from chat" {
		t.Fatalf("Answer = %q, want %q", resp.Answer, "hello from chat")
	}
	if resp.Agent != "stub-agent" {
		t.Fatalf("Agent = %q, want %q", resp.Agent, "stub-agent")
	}
}

func TestChatHandler_RejectsInvalidRequest(t *testing.T) {
	mux := NewMux(Handlers{
		Agent:  newStubAgent("unused"),
		Ready:  func(context.Context) error { return nil },
		Tracer: otel.Tracer("test"),
	})

	req := httptest.NewRequest(http.MethodPost, "/chat", bytes.NewBufferString(`{"message":"   "}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestChatHandler_ThreadsProvidedSessionIDIntoContext(t *testing.T) {
	agent := &sessionAwareAgent{answer: "ok"}
	mux := NewMux(Handlers{
		Agent:  agent,
		Ready:  func(context.Context) error { return nil },
		Tracer: otel.Tracer("test"),
	})

	req := httptest.NewRequest(http.MethodPost, "/chat", bytes.NewBufferString(`{"message":"hi","session_id":"sess-123"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if agent.lastSessionID != "sess-123" {
		t.Fatalf("agent saw session_id = %q, want %q", agent.lastSessionID, "sess-123")
	}
	if got := rec.Header().Get("X-Session-Id"); got != "sess-123" {
		t.Fatalf("X-Session-Id = %q, want %q", got, "sess-123")
	}
}

func TestChatHandler_GeneratesSessionIDWhenMissing(t *testing.T) {
	agent := &sessionAwareAgent{answer: "ok"}
	mux := NewMux(Handlers{
		Agent:  agent,
		Ready:  func(context.Context) error { return nil },
		Tracer: otel.Tracer("test"),
	})

	req := httptest.NewRequest(http.MethodPost, "/chat", bytes.NewBufferString(`{"message":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if agent.lastSessionID == "" {
		t.Fatal("agent lastSessionID is empty")
	}
	if got := rec.Header().Get("X-Session-Id"); got == "" {
		t.Fatal("X-Session-Id header is empty")
	} else if got != agent.lastSessionID {
		t.Fatalf("X-Session-Id = %q, want %q", got, agent.lastSessionID)
	}
}

func TestChatHandler_Returns429WhenGuardRejects(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	guard := limits.New(limits.Config{
		MaxTokensPerRequest:       2,
		MaxToolCallsPerAgentLoop:  4,
		MaxRequestsPerIPPerMinute: 10,
		RetryMaxAttempts:          2,
		DailyTokenBudget:          100,
	})
	mux := NewMux(Handlers{
		Agent:  guard.WrapAgent(newStubAgent("unused")),
		Guard:  guard,
		Ready:  func(context.Context) error { return nil },
		Tracer: tp.Tracer("test"),
	})

	req := httptest.NewRequest(http.MethodPost, "/chat", bytes.NewBufferString(`{"message":"one two three"}`))
	req.RemoteAddr = "127.0.0.1:9000"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	assertSingleSpanStatus(t, exp.GetSpans(), "POST /chat", codes.Error)
}

func TestChatHandler_Returns503WhenDisableSwitchFlipsWithoutRestart(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	var disabled atomic.Bool
	guard := limits.New(limits.Config{
		MaxTokensPerRequest:       10,
		MaxToolCallsPerAgentLoop:  4,
		MaxRequestsPerIPPerMinute: 10,
		RetryMaxAttempts:          2,
		DailyTokenBudget:          100,
		DisableLLMVar:             "DISABLE_LLM",
	}, limits.WithEnvLookup(func(key string) (string, bool) {
		if key != "DISABLE_LLM" || !disabled.Load() {
			return "", false
		}
		return "1", true
	}))
	mux := NewMux(Handlers{
		Agent:  guard.WrapAgent(newStubAgent("ok")),
		Guard:  guard,
		Ready:  func(context.Context) error { return nil },
		Tracer: tp.Tracer("test"),
	})

	req := httptest.NewRequest(http.MethodPost, "/chat", bytes.NewBufferString(`{"message":"hi"}`))
	req.RemoteAddr = "127.0.0.1:9000"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d", rec.Code, http.StatusOK)
	}

	disabled.Store(true)

	req = httptest.NewRequest(http.MethodPost, "/chat", bytes.NewBufferString(`{"message":"hi"}`))
	req.RemoteAddr = "127.0.0.1:9000"
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("second status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	spans := exp.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("len(spans) = %d, want 2", len(spans))
	}
	if spans[1].Name != "POST /chat" {
		t.Fatalf("second span name = %q, want POST /chat", spans[1].Name)
	}
	if spans[1].Status.Code != codes.Error {
		t.Fatalf("second span status = %v, want %v", spans[1].Status.Code, codes.Error)
	}
}

func TestStreamHandler_EmitsSSE(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	otel.SetTracerProvider(tp)

	mux := NewMux(Handlers{
		Agent:  newStubAgent("stream answer"),
		Ready:  func(context.Context) error { return nil },
		Tracer: tp.Tracer("test"),
	})

	req := httptest.NewRequest(http.MethodPost, "/chat/stream", bytes.NewBufferString(`{"message":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	if got := rec.Header().Get("X-Trace-Id"); got == "" {
		t.Fatal("X-Trace-Id header is empty")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: step") {
		t.Fatalf("body missing step event: %q", body)
	}
	if !strings.Contains(body, "event: done") {
		t.Fatalf("body missing done event: %q", body)
	}
	if !strings.Contains(body, "stream answer") {
		t.Fatalf("body missing final answer: %q", body)
	}
}

func TestHealthAndReadyHandlers(t *testing.T) {
	mux := NewMux(Handlers{
		Agent:  newStubAgent("ok"),
		Ready:  func(context.Context) error { return nil },
		Tracer: otel.Tracer("test"),
	})

	for _, tc := range []struct {
		path string
	}{
		{path: "/healthz"},
		{path: "/readyz"},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want %d", tc.path, rec.Code, http.StatusOK)
		}
		if got := rec.Header().Get("X-Trace-Id"); got == "" {
			t.Fatalf("%s X-Trace-Id header is empty", tc.path)
		}
	}
}

func TestReadyHandler_ReportsUnavailable(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	mux := NewMux(Handlers{
		Agent: newStubAgent("ok"),
		Ready: func(context.Context) error {
			return context.DeadlineExceeded
		},
		Tracer: tp.Tracer("test"),
	})

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	assertSingleSpanStatus(t, exp.GetSpans(), "GET /readyz", codes.Error)
}

// TestReadyHandler_PropagatesErrorMessageInBody locks the body-propagation
// contract: operators inspecting kubectl describe pod or compose logs must
// see the actual ReadyFunc failure reason (e.g. "session database
// unavailable: ...") instead of a bare 503.
func TestReadyHandler_PropagatesErrorMessageInBody(t *testing.T) {
	mux := NewMux(Handlers{
		Agent: newStubAgent("ok"),
		Ready: func(context.Context) error {
			return errors.New("custom upstream probe message xyz")
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "custom upstream probe message xyz") {
		t.Fatalf("body = %q, want substring of ReadyFunc error", rec.Body.String())
	}
}

type stubAgent struct {
	answer string
	err    error
}

func newStubAgent(answer string) agents.Agent {
	return &stubAgent{answer: answer}
}

func (a *stubAgent) Name() string { return "stub-agent" }

func (a *stubAgent) Run(_ context.Context, _ string) (agents.Result, error) {
	if a.err != nil {
		return agents.Result{}, a.err
	}
	return agents.Result{
		Answer: a.answer,
		Trace:  []agents.Step{{Kind: agents.StepFinal, Content: a.answer}},
		Usage:  agents.Usage{LLMCalls: 1},
	}, nil
}

func (a *stubAgent) RunStream(_ context.Context, _ string) (<-chan agents.StepEvent, error) {
	ch := make(chan agents.StepEvent, 2)
	go func() {
		defer close(ch)
		if a.err != nil {
			ch <- agents.StepEvent{Done: true, Err: a.err}
			return
		}
		ch <- agents.StepEvent{Step: agents.Step{Kind: agents.StepFinal, Content: a.answer}}
		ch <- agents.StepEvent{Done: true, Final: &agents.Result{
			Answer: a.answer,
			Trace:  []agents.Step{{Kind: agents.StepFinal, Content: a.answer}},
			Usage:  agents.Usage{LLMCalls: 1},
		}}
	}()
	return ch, nil
}

type sessionAwareAgent struct {
	answer        string
	lastSessionID string
}

func (a *sessionAwareAgent) Name() string { return "session-aware-agent" }

func (a *sessionAwareAgent) Run(ctx context.Context, _ string) (agents.Result, error) {
	a.lastSessionID = sessionstore.SessionIDFromContext(ctx)
	return agents.Result{Answer: a.answer}, nil
}

func (a *sessionAwareAgent) RunStream(ctx context.Context, _ string) (<-chan agents.StepEvent, error) {
	a.lastSessionID = sessionstore.SessionIDFromContext(ctx)
	ch := make(chan agents.StepEvent, 1)
	go func() {
		defer close(ch)
		ch <- agents.StepEvent{Done: true, Final: &agents.Result{Answer: a.answer}}
	}()
	return ch, nil
}

func TestStreamBody_IsLineDelimited(t *testing.T) {
	mux := NewMux(Handlers{
		Agent:  newStubAgent("delimited"),
		Ready:  func(context.Context) error { return nil },
		Tracer: otel.Tracer("test"),
	})

	req := httptest.NewRequest(http.MethodPost, "/chat/stream", bytes.NewBufferString(`{"message":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	scanner := bufio.NewScanner(strings.NewReader(rec.Body.String()))
	lines := 0
	for scanner.Scan() {
		lines++
	}
	if lines < 4 {
		t.Fatalf("SSE line count = %d, want at least 4", lines)
	}
}

func TestChatHandler_Returns500AndMarksTraceErrorOnAgentFailure(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	mux := NewMux(Handlers{
		Agent:  &stubAgent{err: errors.New("boom")},
		Ready:  func(context.Context) error { return nil },
		Tracer: tp.Tracer("test"),
	})

	req := httptest.NewRequest(http.MethodPost, "/chat", bytes.NewBufferString(`{"message":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	assertSingleSpanStatus(t, exp.GetSpans(), "POST /chat", codes.Error)
}

func assertSingleSpanStatus(t *testing.T, spans tracetest.SpanStubs, name string, want codes.Code) {
	t.Helper()
	if len(spans) != 1 {
		t.Fatalf("len(spans) = %d, want 1", len(spans))
	}
	if spans[0].Name != name {
		t.Fatalf("span name = %q, want %q", spans[0].Name, name)
	}
	if spans[0].Status.Code != want {
		t.Fatalf("span status = %v, want %v", spans[0].Status.Code, want)
	}
}

var _ agents.Agent = (*stubAgent)(nil)
var _ agents.Agent = (*sessionAwareAgent)(nil)
var _ llm.ChatModel = (*llm.ScriptedLLM)(nil)
