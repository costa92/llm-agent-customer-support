package supportflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	agents "github.com/costa92/llm-agent"
	"github.com/costa92/llm-agent-customer-support/internal/guardrails"
	"github.com/costa92/llm-agent-customer-support/internal/sessionstore"
	"github.com/costa92/llm-agent/llm"
	"github.com/costa92/llm-agent/orchestrate"
	"github.com/costa92/llm-agent/rag"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type Options struct {
	Model      llm.ChatModel
	RAG        *rag.RAGSystem
	Sessions   sessionstore.Store
	Guardrails *guardrails.Guardrails
}

type flow struct {
	graph       *orchestrate.CompiledGraph[state]
	selfService agents.Agent
	sessions    sessionstore.Store
	guardrails  *guardrails.Guardrails
}

type state struct {
	SessionID    string
	History      []sessionstore.Message
	Question     string
	OrderID      string
	NeedMoreInfo bool
	NeedsHuman   bool
	Answer       string
}

func New(opts Options) (agents.Agent, error) {
	if opts.RAG == nil {
		return nil, errors.New("supportflow: RAG is required")
	}

	reg := agents.NewRegistry(refundPolicyTool(opts.RAG))
	systemPrompt := "Use the available tools to answer refund and policy questions. Prefer the refund_policy tool when an order ID is present."
	if opts.Guardrails != nil {
		systemPrompt = opts.Guardrails.SystemPromptPrefix() + "\n\n" + systemPrompt
	}
	selfService, err := agents.NewFunctionCallAgent(opts.Model, agents.FunctionCallOptions{
		Name:         "support-self-service",
		Registry:     reg,
		SystemPrompt: systemPrompt,
	})
	if err != nil {
		return nil, err
	}

	graph, err := buildGraph(selfService, opts.Sessions)
	if err != nil {
		return nil, err
	}

	return &flow{
		graph:       graph,
		selfService: selfService,
		sessions:    opts.Sessions,
		guardrails:  opts.Guardrails,
	}, nil
}

func (f *flow) Name() string { return "support-flow" }

func (f *flow) Run(ctx context.Context, input string) (agents.Result, error) {
	return f.run(ctx, input, nil)
}

func (f *flow) RunStream(ctx context.Context, input string) (<-chan agents.StepEvent, error) {
	return runStreamFromRun(ctx, func(ctx context.Context, onStep func(agents.Step)) (agents.Result, error) {
		return f.run(ctx, input, onStep)
	}), nil
}

func (f *flow) run(ctx context.Context, input string, onStep func(agents.Step)) (agents.Result, error) {
	if strings.TrimSpace(input) == "" {
		return agents.Result{}, agents.ErrEmptyInput
	}
	if onStep == nil {
		onStep = func(agents.Step) {}
	}
	if ok, _ := f.allowInput(input); !ok {
		if span := trace.SpanFromContext(ctx); span != nil {
			span.SetAttributes(attribute.Bool("prompt_injection_attempt", true))
		}
		answer := f.guardrails.SafeFallback()
		final := agents.Step{Kind: agents.StepFinal, Content: answer}
		onStep(final)
		return agents.Result{
			Answer: answer,
			Trace:  []agents.Step{final},
			Usage:  agents.Usage{},
		}, nil
	}

	out, err := f.graph.Run(ctx, state{Question: input}, orchestrate.WithMaxSteps(8))
	if err != nil {
		return agents.Result{}, err
	}
	if err := f.persistSession(ctx, out); err != nil {
		return agents.Result{}, err
	}

	final := agents.Step{Kind: agents.StepFinal, Content: out.Answer}
	onStep(final)
	return agents.Result{
		Answer: out.Answer,
		Trace:  []agents.Step{final},
		Usage:  agents.Usage{LLMCalls: 1},
	}, nil
}

func buildGraph(selfService agents.Agent, sessions sessionstore.Store) (*orchestrate.CompiledGraph[state], error) {
	g := orchestrate.NewStateGraph[state]()
	g.AddNode("load-session", func(ctx context.Context, s state) (state, error) {
		s.SessionID = sessionstore.SessionIDFromContext(ctx)
		if s.SessionID == "" || sessions == nil {
			return s, nil
		}
		session, err := sessions.Get(ctx, s.SessionID)
		if err != nil {
			if errors.Is(err, sessionstore.ErrNotFound) {
				return s, nil
			}
			return s, err
		}
		s.History = session.Messages
		s.Question = strings.TrimSpace(s.Question)
		if s.Question != "" {
			s.Question = mergeQuestionWithHistory(session.Messages, s.Question)
		}
		return s, nil
	})
	g.AddNode("classify", func(_ context.Context, s state) (state, error) {
		q := strings.ToLower(s.Question)
		s.OrderID = extractOrderID(q)
		switch {
		case strings.Contains(q, "chargeback"), strings.Contains(q, "fraud"):
			s.NeedsHuman = true
		case s.OrderID == "":
			s.NeedMoreInfo = true
		}
		return s, nil
	})
	g.AddNode("request-more-info", func(_ context.Context, s state) (state, error) {
		s.Answer = "Please share your order ID so I can check the refund policy."
		s.NeedMoreInfo = false
		return s, nil
	})
	g.AddNode("handover-human", func(_ context.Context, s state) (state, error) {
		s.Answer = "I’m escalating this case to a human agent for manual review."
		return s, nil
	})
	g.AddNode("self-service", func(ctx context.Context, s state) (state, error) {
		res, err := selfService.Run(ctx, s.Question)
		if err != nil {
			return s, err
		}
		s.Answer = res.Answer
		return s, nil
	})

	g.AddEdge(orchestrate.NodeStart, "load-session")
	g.AddEdge("load-session", "classify")
	g.AddConditionalEdge("classify", func(s state) string {
		switch {
		case s.NeedsHuman:
			return "handover-human"
		case s.NeedMoreInfo:
			return "request-more-info"
		default:
			return "self-service"
		}
	})
	g.AddEdge("request-more-info", orchestrate.NodeEnd)
	g.AddEdge("handover-human", orchestrate.NodeEnd)
	g.AddEdge("self-service", orchestrate.NodeEnd)
	return g.Compile()
}

func (f *flow) persistSession(ctx context.Context, s state) error {
	if f.sessions == nil || s.SessionID == "" {
		return nil
	}
	history := append([]sessionstore.Message(nil), s.History...)
	history = append(history,
		sessionstore.Message{Role: "user", Content: originalQuestionFromMerged(s.Question)},
		sessionstore.Message{Role: "assistant", Content: s.Answer},
	)
	return f.sessions.Save(ctx, sessionstore.Session{
		ID:       s.SessionID,
		Messages: history,
	})
}

func mergeQuestionWithHistory(history []sessionstore.Message, question string) string {
	if len(history) == 0 {
		return question
	}
	var b strings.Builder
	for _, msg := range history {
		if strings.TrimSpace(msg.Content) == "" {
			continue
		}
		b.WriteString(msg.Role)
		b.WriteString(": ")
		b.WriteString(msg.Content)
		b.WriteString("\n")
	}
	b.WriteString("user: ")
	b.WriteString(question)
	return b.String()
}

func originalQuestionFromMerged(question string) string {
	lines := strings.Split(strings.TrimSpace(question), "\n")
	if len(lines) == 0 {
		return question
	}
	last := strings.TrimSpace(lines[len(lines)-1])
	if strings.HasPrefix(strings.ToLower(last), "user: ") {
		return strings.TrimSpace(last[len("user: "):])
	}
	return question
}

func refundPolicyTool(r *rag.RAGSystem) agents.Tool {
	return agents.NewFuncTool(
		"refund_policy",
		"Look up refund-policy knowledge for an order and return a grounded answer.",
		json.RawMessage(`{"type":"object","properties":{"order_id":{"type":"string"}},"required":["order_id"]}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var req struct {
				OrderID string `json:"order_id"`
				UserID  string `json:"user_id"`
			}
			if err := json.Unmarshal(args, &req); err != nil {
				return "", fmt.Errorf("refund_policy: bad args: %w", err)
			}
			if strings.TrimSpace(req.UserID) != "" {
				return "", errors.New("refund_policy: user_id must be enforced server-side")
			}
			if strings.TrimSpace(req.OrderID) == "" {
				return "", errors.New("refund_policy: order_id is required")
			}
			hits, err := r.Search(ctx, "refund policy order "+req.OrderID, 1, rag.SearchOptions{})
			if err != nil {
				return "", err
			}
			if len(hits) == 0 {
				return "No refund policy evidence found for this order.", nil
			}
			return fmt.Sprintf("Refund guidance for order %s: %s", req.OrderID, hits[0].Doc.Content), nil
		},
	)
}

func (f *flow) allowInput(input string) (bool, string) {
	if f.guardrails == nil {
		return true, ""
	}
	return f.guardrails.FilterInput(input)
}

func extractOrderID(input string) string {
	fields := strings.FieldsFunc(input, func(r rune) bool {
		return r < '0' || r > '9'
	})
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func runStreamFromRun(ctx context.Context, runFn func(context.Context, func(agents.Step)) (agents.Result, error)) <-chan agents.StepEvent {
	ch := make(chan agents.StepEvent, 16)
	go func() {
		defer close(ch)
		cb := func(s agents.Step) {
			select {
			case ch <- agents.StepEvent{Step: s}:
			case <-ctx.Done():
			}
		}
		res, err := runFn(ctx, cb)
		if err != nil {
			select {
			case ch <- agents.StepEvent{Done: true, Err: err}:
			case <-ctx.Done():
			}
			return
		}
		select {
		case ch <- agents.StepEvent{Done: true, Final: &res}:
		case <-ctx.Done():
		}
	}()
	return ch
}
