package supportflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	agents "github.com/costa92/llm-agent"
	"github.com/costa92/llm-agent/llm"
	"github.com/costa92/llm-agent/orchestrate"
	"github.com/costa92/llm-agent/rag"
)

type Options struct {
	Model llm.ChatModel
	RAG   *rag.RAGSystem
}

type flow struct {
	graph       *orchestrate.CompiledGraph[state]
	selfService agents.Agent
}

type state struct {
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
	selfService, err := agents.NewFunctionCallAgent(opts.Model, agents.FunctionCallOptions{
		Name:         "support-self-service",
		Registry:     reg,
		SystemPrompt: "Use the available tools to answer refund and policy questions. Prefer the refund_policy tool when an order ID is present.",
	})
	if err != nil {
		return nil, err
	}

	graph, err := buildGraph(selfService)
	if err != nil {
		return nil, err
	}

	return &flow{
		graph:       graph,
		selfService: selfService,
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

	out, err := f.graph.Run(ctx, state{Question: input}, orchestrate.WithMaxSteps(8))
	if err != nil {
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

func buildGraph(selfService agents.Agent) (*orchestrate.CompiledGraph[state], error) {
	g := orchestrate.NewStateGraph[state]()
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

	g.AddEdge(orchestrate.NodeStart, "classify")
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

func refundPolicyTool(r *rag.RAGSystem) agents.Tool {
	return agents.NewFuncTool(
		"refund_policy",
		"Look up refund-policy knowledge for an order and return a grounded answer.",
		json.RawMessage(`{"type":"object","properties":{"order_id":{"type":"string"}},"required":["order_id"]}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var req struct {
				OrderID string `json:"order_id"`
			}
			if err := json.Unmarshal(args, &req); err != nil {
				return "", fmt.Errorf("refund_policy: bad args: %w", err)
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
