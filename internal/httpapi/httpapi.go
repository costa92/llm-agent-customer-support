package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	agents "github.com/costa92/llm-agent"
	"go.opentelemetry.io/otel/trace"
)

const traceHeader = "X-Trace-Id"

type ReadyFunc func(context.Context) error

type Handlers struct {
	Agent  agents.Agent
	Ready  ReadyFunc
	Tracer trace.Tracer
}

type ChatRequest struct {
	Message string `json:"message"`
}

type ChatResponse struct {
	Answer string `json:"answer"`
	Agent  string `json:"agent"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

type StreamEnvelope struct {
	Kind   string `json:"kind"`
	Answer string `json:"answer,omitempty"`
	Error  string `json:"error,omitempty"`
}

func NewMux(h Handlers) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/chat", withTrace(h.Tracer, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleChat(h.Agent, w, r)
	})))
	mux.Handle("/chat/stream", withTrace(h.Tracer, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleStream(h.Agent, w, r)
	})))
	mux.Handle("/healthz", withTrace(h.Tracer, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleHealth(w, r)
	})))
	mux.Handle("/readyz", withTrace(h.Tracer, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleReady(h.Ready, w, r)
	})))
	return mux
}

func withTrace(tracer trace.Tracer, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tracer != nil {
			ctx, span := tracer.Start(r.Context(), r.Method+" "+r.URL.Path)
			defer span.End()
			r = r.WithContext(ctx)
		}

		if sc := trace.SpanContextFromContext(r.Context()); sc.IsValid() {
			w.Header().Set(traceHeader, sc.TraceID().String())
		} else {
			w.Header().Set(traceHeader, "bootstrap-trace")
		}
		next.ServeHTTP(w, r)
	})
}

func handleChat(agent agents.Agent, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	req, err := decodeChatRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	res, err := agent.Run(r.Context(), req.Message)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, ChatResponse{
		Answer: res.Answer,
		Agent:  agent.Name(),
	})
}

func handleStream(agent agents.Agent, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	req, err := decodeChatRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	ch, err := agent.RunStream(r.Context(), req.Message)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	for ev := range ch {
		if ev.Done {
			if ev.Err != nil {
				writeSSE(w, "error", StreamEnvelope{Kind: "error", Error: ev.Err.Error()})
			} else if ev.Final != nil {
				writeSSE(w, "done", StreamEnvelope{Kind: "done", Answer: ev.Final.Answer})
			} else {
				writeSSE(w, "done", StreamEnvelope{Kind: "done"})
			}
			flusher.Flush()
			return
		}

		writeSSE(w, "step", StreamEnvelope{
			Kind:   string(ev.Step.Kind),
			Answer: ev.Step.Content,
		})
		flusher.Flush()
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func handleReady(ready ReadyFunc, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if ready != nil {
		if err := ready(r.Context()); err != nil {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func decodeChatRequest(r *http.Request) (ChatRequest, error) {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.Contains(ct, "application/json") {
		return ChatRequest{}, errors.New("content type must be application/json")
	}

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return ChatRequest{}, fmt.Errorf("invalid json: %w", err)
	}
	if strings.TrimSpace(req.Message) == "" {
		return ChatRequest{}, errors.New("message is required")
	}
	return req, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}

func writeSSE(w http.ResponseWriter, event string, payload any) {
	data, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(w, "event: %s\n", event)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
}
