package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agents "github.com/costa92/llm-agent"
	"github.com/costa92/llm-agent-customer-support/internal/sessionstore"
	"github.com/costa92/llm-agent/llm"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
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
	mux := NewMux(Handlers{
		Agent: newStubAgent("ok"),
		Ready: func(context.Context) error {
			return context.DeadlineExceeded
		},
		Tracer: otel.Tracer("test"),
	})

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

type stubAgent struct {
	answer string
}

func newStubAgent(answer string) agents.Agent {
	return &stubAgent{answer: answer}
}

func (a *stubAgent) Name() string { return "stub-agent" }

func (a *stubAgent) Run(_ context.Context, _ string) (agents.Result, error) {
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

var _ agents.Agent = (*stubAgent)(nil)
var _ agents.Agent = (*sessionAwareAgent)(nil)
var _ llm.ChatModel = (*llm.ScriptedLLM)(nil)
