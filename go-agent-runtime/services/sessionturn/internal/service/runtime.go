package service

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiondiagnostics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/internal/seed"
	turns "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/internal/turns"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

type Dependencies struct {
	Allocator          sessionturn.Allocator
	PolicyFactory      tools.InteractiveToolPolicyFactory
	ImageStaging       tools.ImageStaging
	InstructionService session.InstructionService
	LifecycleFactory   func() sessiondiagnostics.Service
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
	policy := request.InteractiveToolPolicy
	if policy == nil && request.ToolPolicyRequest != nil {
		if s == nil || s.deps.PolicyFactory == nil {
			return nil, errors.New("session turn policy factory is not configured")
		}
		var err error
		policy, err = s.deps.PolicyFactory.Resolve(*request.ToolPolicyRequest)
		if err != nil {
			return nil, err
		}
	}
	var instructionService session.InstructionService
	if s != nil {
		instructionService = s.deps.InstructionService
	}
	inferencer, wirePrompt, err := prepareInferencer(ctx, request, seedService, instructionService)
	if err != nil {
		return nil, err
	}

	turnState := turns.New(turns.Dependencies{SessionInferencer: inferencer, EventSink: request.EventSink})
	var continuation sessiondiagnostics.Service
	if s != nil && s.deps.LifecycleFactory != nil {
		continuation = s.deps.LifecycleFactory()
	}
	return &runtime{
		inferencer:      inferencer,
		turns:           turnState,
		toolExecutor:    s.prepareToolExecutor(request, policy),
		toolDefinitions: cloneDefinitions(request.ToolDefinitions),
		policy:          clonePolicy(policy),
		output:          seedService,
		wirePrompt:      wirePrompt,
		continuation:    continuation,
		imageCleanup:    request.ImageCleanup,
	}, nil
}

func (s *Service) prepareToolExecutor(request sessionturn.Request, policy tools.InteractiveToolPolicy) messages.ToolExecutor {
	toolExecutor := request.ToolExecutor
	if request.ImageCapabilities != nil {
		toolExecutor = s.BindImageToolExecutor(toolExecutor, *request.ImageCapabilities)
	}
	return newToolExecutor(toolExecutor, policy, request.ToolExecutionTimeout, request.ToolCallObserver, request.ToolResultObserver, request.ToolDiagnostic, request.ToolFailurePresenter)
}

func (s *Service) StageImageTools(ctx context.Context, request tools.ImageStagingRequest) (tools.ImageStagingResult, error) {
	if s == nil || s.deps.ImageStaging == nil {
		return tools.ImageStagingResult{}, errors.New("session turn image staging is not configured")
	}
	return s.deps.ImageStaging.Stage(ctx, request)
}

func (s *Service) BindImageToolExecutor(executor messages.ToolExecutor, capabilities sessionturn.ImageCapabilities) messages.ToolExecutor {
	if executor == nil {
		return nil
	}
	binder, ok := executor.(tools.SessionImagePreparerBinder)
	if !ok {
		return executor
	}
	return binder.WithSessionImagePreparer(func(paths []string) ([]messages.ImagePart, error) {
		return s.PrepareImageParts(paths, capabilities)
	})
}

func prepareInferencer(ctx context.Context, request sessionturn.Request, seedService *seed.Service, instructionService session.InstructionService) (messages.SessionInferencer, string, error) {
	inferencer := request.SessionInferencer
	instructions, err := resolveInstructions(ctx, request, instructionService)
	if err != nil {
		return nil, "", err
	}
	providerConfigured := false
	if instructions != "" || len(request.ToolDefinitions) != 0 {
		inferencer, providerConfigured = configureProviderRequest(inferencer, instructions, request.ToolDefinitions)
	}
	if request.Image != nil {
		inferencer = newImageInferencer(inferencer, *request.Image)
	}
	wirePrompt, inferencer, err := applySeed(inferencer, request.Seed, seedService)
	if err != nil {
		return nil, "", err
	}
	if (instructions != "" || len(request.ToolDefinitions) != 0) && !providerConfigured {
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

func resolveInstructions(ctx context.Context, request sessionturn.Request, instructionService session.InstructionService) (string, error) {
	instructions := request.InstructionsText
	service := request.Instructions.Service
	if service != nil {
		resolved, err := service.Resolve(ctx, request.Instructions.Request)
		if err != nil {
			return "", err
		}
		instructions = resolved.Instructions
	}
	if request.Instructions.Composition != nil {
		if service == nil {
			service = instructionService
		}
		if service == nil {
			return "", errors.New("session turn instruction service is not configured")
		}
		composition := *request.Instructions.Composition
		composition.Instructions = instructions
		instructions = service.Compose(composition)
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
	continuation    sessiondiagnostics.Service
	imageCleanup    func() error

	mu          sync.Mutex
	publication sessionturn.Publication
	closeOnce   sync.Once
	closeErr    error
}

func (r *runtime) Inferencer() messages.SessionInferencer { return r.inferencer }
func (r *runtime) ToolExecutor() messages.ToolExecutor    { return r.toolExecutor }
func (r *runtime) ToolDefinitions() []messages.ToolDefinition {
	return cloneDefinitions(r.toolDefinitions)
}
func (r *runtime) InteractiveToolPolicy() tools.InteractiveToolPolicy { return clonePolicy(r.policy) }
func (r *runtime) WirePrompt() string                                 { return r.wirePrompt }
func (r *runtime) Continuation() sessiondiagnostics.Service           { return r.continuation }

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
	r.closeOnce.Do(func() {
		r.mu.Lock()
		publication := r.publication
		r.mu.Unlock()
		var closeErr error
		if publication != nil {
			publication.Stop()
			closeErr = errors.Join(closeErr, publication.State().Err)
		}
		if r.continuation != nil {
			closeErr = errors.Join(closeErr, r.continuation.Close())
		}
		closeErr = errors.Join(closeErr, r.turns.Close())
		if r.imageCleanup != nil {
			closeErr = errors.Join(closeErr, r.imageCleanup())
		}
		r.mu.Lock()
		r.closeErr = closeErr
		r.mu.Unlock()
	})
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closeErr
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
