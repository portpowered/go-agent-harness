package service

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/internal/seed"
	turns "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/internal/turns"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

type Dependencies struct {
	Allocator sessionturn.Allocator
}

type Service struct{ deps Dependencies }

func New(deps Dependencies) *Service { return &Service{deps: deps} }

var _ sessionturn.Service = (*Service)(nil)

func (s *Service) Prepare(ctx context.Context, request sessionturn.Request) (sessionturn.Runtime, error) {
	if ctx == nil {
		return nil, errors.New("session turn preparation context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	allocator := request.SeedAllocator
	if allocator == nil && s != nil {
		allocator = s.deps.Allocator
	}
	seedService := seed.New(allocator)
	inferencer, wirePrompt, err := prepareInferencer(ctx, request, seedService)
	if err != nil {
		return nil, err
	}

	turnState := turns.New(turns.Dependencies{SessionInferencer: inferencer, EventSink: request.EventSink})
	return &runtime{
		inferencer:      inferencer,
		turns:           turnState,
		toolExecutor:    newToolExecutor(request.ToolExecutor, request.InteractiveToolPolicy, request.ToolExecutionTimeout, request.ToolCallObserver, request.ToolResultObserver, request.ToolDiagnostic, request.ToolFailurePresenter),
		toolDefinitions: cloneDefinitions(request.ToolDefinitions),
		policy:          clonePolicy(request.InteractiveToolPolicy),
		output:          seedService,
		wirePrompt:      wirePrompt,
	}, nil
}

func prepareInferencer(ctx context.Context, request sessionturn.Request, seedService *seed.Service) (messages.SessionInferencer, string, error) {
	inferencer := request.SessionInferencer
	if request.Image != nil {
		inferencer = newImageInferencer(inferencer, *request.Image)
	}
	wirePrompt, inferencer, err := applySeed(inferencer, request.Seed, seedService)
	if err != nil {
		return nil, "", err
	}
	instructions, err := resolveInstructions(ctx, request)
	if err != nil {
		return nil, "", err
	}
	if instructions != "" || len(request.ToolDefinitions) != 0 {
		inferencer = newInstructionsInferencer(inferencer, instructions, request.ToolDefinitions)
	}
	return inferencer, wirePrompt, nil
}

func applySeed(inferencer messages.SessionInferencer, requested sessionturn.Seed, seedService *seed.Service) (string, messages.SessionInferencer, error) {
	if !requested.Present {
		return "", inferencer, nil
	}
	wirePrompt := seedService.Allocate()
	if wirePrompt == "" {
		return "", nil, errors.New("session turn seed allocator returned an empty prompt")
	}
	return wirePrompt, seedService.WrapInferencer(inferencer, wirePrompt, requested), nil
}

func resolveInstructions(ctx context.Context, request sessionturn.Request) (string, error) {
	if request.Instructions.Service == nil {
		return request.InstructionsText, nil
	}
	resolved, err := request.Instructions.Service.Resolve(ctx, request.Instructions.Request)
	if err != nil {
		return "", err
	}
	instructions := resolved.Instructions
	if request.Instructions.Composition != nil {
		composition := *request.Instructions.Composition
		composition.Instructions = instructions
		instructions = request.Instructions.Service.Compose(composition)
	}
	return instructions, nil
}

type runtime struct {
	inferencer      messages.SessionInferencer
	turns           *turns.Service
	toolExecutor    messages.ToolExecutor
	toolDefinitions []messages.ToolDefinition
	policy          tools.InteractiveToolPolicy
	output          *seed.Service
	wirePrompt      string

	mu          sync.Mutex
	publication sessionturn.Publication
}

func (r *runtime) Inferencer() messages.SessionInferencer { return r.inferencer }
func (r *runtime) ToolExecutor() messages.ToolExecutor    { return r.toolExecutor }
func (r *runtime) ToolDefinitions() []messages.ToolDefinition {
	return cloneDefinitions(r.toolDefinitions)
}
func (r *runtime) InteractiveToolPolicy() tools.InteractiveToolPolicy { return clonePolicy(r.policy) }
func (r *runtime) WirePrompt() string                                 { return r.wirePrompt }

func (r *runtime) RunTurn(ctx context.Context, request sessionturn.TurnRequest) (sessionturn.TurnResult, error) {
	turn, err := r.turns.RunTurn(ctx, request.Input, request.Direction, request.StartTick, request.EndTick)
	if err != nil {
		return sessionturn.TurnResult{}, err
	}
	return sessionturn.TurnResult{Turn: turn}, nil
}

func (r *runtime) History() []sessionturn.SessionTurn { return r.turns.History() }
func (r *runtime) PublicationState() sessionturn.PublicationState {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.publication == nil {
		return sessionturn.PublicationState{}
	}
	return r.publication.State()
}
func (r *runtime) StartPublication(ctx context.Context, request sessionturn.PublicationRequest) (sessionturn.Publication, error) {
	publication, err := startPublication(ctx, request)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.publication = publication
	r.mu.Unlock()
	return publication, nil
}
func (r *runtime) NewOutput(writer io.Writer) sessionturn.Output { return r.output.NewOutput(writer) }
func (r *runtime) Close() error {
	r.mu.Lock()
	publication := r.publication
	r.mu.Unlock()
	if publication != nil {
		publication.Stop()
	}
	return r.turns.Close()
}

func cloneDefinitions(definitions []messages.ToolDefinition) []messages.ToolDefinition {
	return messages.CanonicalToolDefinitions(definitions)
}

func clonePolicy(policy tools.InteractiveToolPolicy) tools.InteractiveToolPolicy {
	if policy == nil {
		return nil
	}
	return policy.Clone()
}
