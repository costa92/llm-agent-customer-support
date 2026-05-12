package supportflow

import (
	"context"
	"fmt"
	"strings"

	agents "github.com/costa92/llm-agent"
	"github.com/costa92/llm-agent/llm"
)

type toolAgentOptions struct {
	Name         string
	Registry     *agents.Registry
	SystemPrompt string
	MaxParallel  int
	OnStep       func(agents.Step)
}

type toolAgent struct {
	name   string
	model  llm.ToolCaller
	opts   toolAgentOptions
}

func newToolAgent(model llm.ChatModel, opts toolAgentOptions) (agents.Agent, error) {
	if opts.Name == "" {
		opts.Name = "function-call"
	}
	if opts.MaxParallel == 0 {
		opts.MaxParallel = 4
	}
	tc, ok := model.(llm.ToolCaller)
	if !ok || !model.Info().Capabilities.Tools {
		info := model.Info()
		return nil, fmt.Errorf("supportflow: %s/%s: tools: %w", info.Provider, info.Model, llm.ErrCapabilityNotSupported)
	}
	return &toolAgent{
		name:  opts.Name,
		model: tc,
		opts:  opts,
	}, nil
}

func (a *toolAgent) Name() string { return a.name }

func (a *toolAgent) Run(ctx context.Context, input string) (agents.Result, error) {
	return a.runInternal(ctx, input, normalizedOnStep(a.opts.OnStep))
}

func (a *toolAgent) RunStream(ctx context.Context, input string) (<-chan agents.StepEvent, error) {
	return agentsRunStream(ctx, func(ctx context.Context, onStep func(agents.Step)) (agents.Result, error) {
		return a.runInternal(ctx, input, onStep)
	}), nil
}

func (a *toolAgent) runInternal(ctx context.Context, input string, onStep func(agents.Step)) (agents.Result, error) {
	if strings.TrimSpace(input) == "" {
		return agents.Result{}, agents.ErrEmptyInput
	}
	if a.opts.Registry == nil {
		return agents.Result{}, fmt.Errorf("supportflow: tool agent requires a registry")
	}

	toolModel, err := a.model.WithTools(a.opts.Registry.AsLLMTools())
	if err != nil {
		return agents.Result{}, err
	}
	resp, err := toolModel.Generate(ctx, llm.Request{
		Messages: []llm.Message{
			{Role: "user", Content: prependSystemPrompt(a.opts.SystemPrompt, input)},
		},
	})
	if err != nil {
		return agents.Result{}, err
	}
	trace := make([]agents.Step, 0, len(resp.ToolCalls)*2+1)
	usage := agents.Usage{LLMCalls: 1, Tokens: resp.Usage.TotalTokens}

	if len(resp.ToolCalls) == 0 {
		final := agents.Step{Kind: agents.StepFinal, Content: resp.Text}
		onStep(final)
		trace = append(trace, final)
		return agents.Result{Answer: resp.Text, Trace: trace, Usage: usage}, nil
	}

	tasks := make([]agents.Task, 0, len(resp.ToolCalls))
	for _, tc := range resp.ToolCalls {
		tool, ok := a.opts.Registry.Get(tc.Name)
		if !ok {
			return agents.Result{}, fmt.Errorf("%w: %q", agents.ErrToolNotFound, tc.Name)
		}
		tasks = append(tasks, agents.Task{Tool: tool, Args: tc.Arguments})
	}

	results, err := agents.NewAsyncRunner(a.opts.MaxParallel).Execute(ctx, tasks)
	if err != nil {
		return agents.Result{}, err
	}
	for _, r := range results {
		if r.Err != nil {
			return agents.Result{}, r.Err
		}
	}

	var b strings.Builder
	for i, r := range results {
		tc := resp.ToolCalls[i]
		action := agents.Step{Kind: agents.StepAction, Tool: tc.Name, Args: string(tc.Arguments)}
		onStep(action)
		trace = append(trace, action)

		observation := agents.Step{Kind: agents.StepObservation, Result: r.Output}
		onStep(observation)
		trace = append(trace, observation)

		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(tc.Name)
		b.WriteString(": ")
		b.WriteString(r.Output)
	}

	final := agents.Step{Kind: agents.StepFinal, Content: b.String()}
	onStep(final)
	trace = append(trace, final)
	return agents.Result{
		Answer: final.Content,
		Trace:  trace,
		Usage:  usage,
	}, nil
}

func prependSystemPrompt(systemPrompt, input string) string {
	if systemPrompt == "" {
		return input
	}
	return systemPrompt + "\n\n" + input
}

func normalizedOnStep(cb func(agents.Step)) func(agents.Step) {
	if cb == nil {
		return func(agents.Step) {}
	}
	return cb
}

func agentsRunStream(
	ctx context.Context,
	runFn func(context.Context, func(agents.Step)) (agents.Result, error),
) <-chan agents.StepEvent {
	ch := make(chan agents.StepEvent, 16)
	go func() {
		defer close(ch)
		cb := func(step agents.Step) {
			select {
			case ch <- agents.StepEvent{Step: step}:
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
