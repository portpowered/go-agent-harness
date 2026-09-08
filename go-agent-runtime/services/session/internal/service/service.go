// Package service implements the public session contract. It is private to
// session composition so the runtime can change its planning and lifecycle
// packages without changing embedders.
package service

import (
	"context"
	"fmt"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	agent "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/execution"
)

var _ session.Service = (*Service)(nil)

// Dependencies describes the cohesive execution role required by the session
// service. Provider and tool construction happens in the outer wire package.
type Dependencies struct {
	Executor        *agent.Executor
	Resolver        session.Resolver
	Store           session.SessionStore
	TraceStore      session.TraceStore
	ProviderService providers.Service
}

// Service owns session invocation dispatch behind the public contract.
type Service struct {
	executor        *agent.Executor
	resolver        session.Resolver
	store           session.SessionStore
	traceStore      session.TraceStore
	providerService providers.Service
}

// New constructs the session service without starting a provider or any
// invocation resources.
func New(deps Dependencies) *Service {
	store := deps.Store
	if store == nil {
		store = newMemoryStore()
	}
	traceStore := deps.TraceStore
	if traceStore == nil {
		if shared, ok := store.(session.TraceStore); ok {
			traceStore = shared
		} else {
			traceStore = newMemoryStore()
		}
	}
	return &Service{executor: deps.Executor, resolver: deps.Resolver, store: store, traceStore: traceStore, providerService: deps.ProviderService}
}

// Run executes one request and returns its terminal text result.
func (s *Service) Run(ctx context.Context, request session.Request) (session.Result, error) {
	if s == nil || s.executor == nil {
		return session.Result{}, fmt.Errorf("session executor is required")
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return session.Result{}, err
	}
	resolution, err := s.resolve(ctx, request)
	if err != nil {
		return session.Result{}, err
	}
	config := toExecutionConfig(request, resolution)
	executor := s.executor.WithResolution(toRuntimeResolution(ctx, resolution))
	text, produced, err := executor.RunAskDetailed(ctx, &config, request.Input, io.Discard)
	return session.Result{Text: text, Messages: produced}, err
}

// Open builds one invocation and returns a handle for streaming turns. The
// handle owns the loop and its request resources until Close is called.
func (s *Service) Open(ctx context.Context, request session.Request) (session.SessionHandle, error) {
	if s == nil || s.executor == nil {
		return nil, fmt.Errorf("session executor is required")
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolution, err := s.resolve(ctx, request)
	if err != nil {
		return nil, err
	}
	config := toExecutionConfig(request, resolution)
	executor := s.executor.WithResolution(toRuntimeResolution(ctx, resolution))
	runData, err := executor.BuildLoop(ctx, &config)
	if err != nil {
		return nil, err
	}
	lifetimeCtx, cancel := context.WithCancel(ctx)
	return &handle{
		executor:  executor,
		runData:   runData,
		config:    config,
		ctx:       lifetimeCtx,
		cancel:    cancel,
		closeDone: make(chan struct{}),
		active:    make(map[*ownedStream]struct{}),
	}, nil
}

// RunIterative executes the transport-neutral iterative loop and maps its
// private implementation result to the public result contract.
func (s *Service) RunIterative(ctx context.Context, request session.Request, options session.IterativeRequest) (session.IterativeResult, error) {
	if s == nil || s.executor == nil {
		return session.IterativeResult{}, fmt.Errorf("session executor is required")
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return session.IterativeResult{}, err
	}
	resolution, err := s.resolve(ctx, request)
	if err != nil {
		return session.IterativeResult{}, err
	}
	if options.TraceStore != nil {
		resolution.TraceStore = options.TraceStore
	}
	config := toExecutionConfig(request, resolution)
	executor := s.executor.WithResolution(toRuntimeResolution(ctx, resolution))
	loopConfig := agent.IterativeLoopConfig{
		MaxIterations: options.MaxIterations, StopWord: options.StopWord,
		ContextPressureThreshold: options.ContextPressureThreshold,
		ContextPressureMessage:   options.ContextPressureMessage, TraceID: options.TraceID,
	}
	var result agent.IterativeRunResult
	var runErr error
	interaction := toIterativeInteraction(options.Interaction)
	if interaction == nil {
		result, runErr = executor.RunIterativeLoop(ctx, &config, loopConfig, request.Input, io.Discard)
	} else {
		result, runErr = executor.RunIterativeLoopWithInteraction(ctx, &config, loopConfig, request.Input, io.Discard, interaction)
	}
	converted := session.IterativeResult{TraceID: result.TraceID, Completed: result.Completed}
	converted.Iterations = make([]session.IterationResult, 0, len(result.Iterations))
	for _, iteration := range result.Iterations {
		converted.Iterations = append(converted.Iterations, session.IterationResult{
			Iteration: iteration.Iteration, SessionID: iteration.SessionID,
			Text: iteration.Text, Err: iteration.Err,
			Interrupted: iteration.Interrupted, StopWordMatched: iteration.StopWordMatched,
		})
	}
	return converted, runErr
}

func toIterativeInteraction(source *session.IterativeInteraction) *agent.IterativeInteraction {
	if source == nil {
		return nil
	}
	return &agent.IterativeInteraction{
		InitialPrompt: source.InitialPrompt,
		TraceReady: func(ctx context.Context, trace agent.IterativeTrace) error {
			return source.TraceReady(ctx, session.IterativeTrace{
				TraceID: trace.TraceID, StartIteration: trace.StartIteration,
				MaxIterations: trace.MaxIterations, Resumed: trace.Resumed,
			})
		},
		IterationContext: source.IterationContext,
		OnIteration: func(ctx context.Context, iteration agent.IterationRunResult) (agent.IterativeDecision, error) {
			decision, err := source.OnIteration(ctx, session.IterationResult{
				Iteration: iteration.Iteration, SessionID: iteration.SessionID,
				Text: iteration.Text, Err: iteration.Err,
				Interrupted: iteration.Interrupted, StopWordMatched: iteration.StopWordMatched,
			})
			return agent.IterativeDecision{Action: agent.IterativeAction(decision.Action), Prompt: decision.Prompt}, err
		},
	}
}

// NewSessionID allocates an ID without starting a model session.
func (s *Service) NewSessionID(ctx context.Context, request session.Request) (string, error) {
	if s == nil || s.executor == nil {
		return "", fmt.Errorf("session executor is required")
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	resolution, err := s.resolve(ctx, request)
	if err != nil {
		return "", err
	}
	config := toExecutionConfig(request, resolution)
	executor := s.executor.WithResolution(toRuntimeResolution(ctx, resolution))
	return executor.NewChatSessionID(&config)
}
