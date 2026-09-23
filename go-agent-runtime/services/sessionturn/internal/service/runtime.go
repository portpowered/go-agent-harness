package service

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/internal/seed"
	turns "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/internal/turns"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

type Dependencies struct {
	Allocator          sessionturn.Allocator
	PolicyFactory      tools.InteractiveToolPolicyFactory
	ImageStaging       tools.ImageStaging
	ToolService        tools.Service
	InstructionService session.InstructionService
	LifecycleFactory   func() sessiontrace.LifecycleService
}

type Service struct{ deps Dependencies }

func New(deps Dependencies) *Service { return &Service{deps: deps} }

var _ sessionturn.Service = (*Service)(nil)

func (s *Service) ResolveInstructions(ctx context.Context, request sessionturn.InstructionRequest) (string, error) {
	if ctx == nil {
		return "", errors.New("session turn instruction context is required")
	}
	var instructionService session.InstructionService
	if s != nil {
		instructionService = s.deps.InstructionService
	}
	request, err := s.withInstructionLoader(ctx, request)
	if err != nil {
		return "", err
	}
	return resolveInstructionRequest(ctx, request, instructionService)
}

func (s *Service) Prepare(ctx context.Context, request sessionturn.Request) (sessionturn.Runtime, error) {
	if ctx == nil {
		return nil, errors.New("session turn preparation context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.prepareImageCapabilities(&request); err != nil {
		return nil, err
	}

	allocator := request.SeedAllocator
	if allocator == nil && s != nil {
		allocator = s.deps.Allocator
	}
	var continuation sessiontrace.LifecycleService
	if s != nil && s.deps.LifecycleFactory != nil {
		continuation = s.deps.LifecycleFactory()
	}
	seedService := seed.New(allocator, seed.Options{Lifecycle: continuation, Observer: request.ToolLifecycle})
	policy, err := s.resolvePolicy(request)
	if err != nil {
		return nil, err
	}
	var instructionService session.InstructionService
	if s != nil {
		instructionService = s.deps.InstructionService
	}
	request.Instructions, err = s.withInstructionLoader(ctx, request.Instructions)
	if err != nil {
		return nil, err
	}
	inferencer, wirePrompt, err := prepareInferencer(ctx, request, seedService, instructionService)
	if err != nil {
		return nil, err
	}

	turnState := turns.New(turns.Dependencies{SessionInferencer: inferencer, EventSink: request.EventSink})
	return &runtime{
		service:         s,
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

func (s *Service) prepareImageCapabilities(request *sessionturn.Request) error {
	if request == nil || request.ImageCapabilities != nil || request.ImageCapabilityRequest == nil ||
		(request.Image == nil && !hasReadImageTool(request.ToolDefinitions)) {
		return nil
	}
	capabilities, err := s.ResolveImageCapabilities(*request.ImageCapabilityRequest)
	if err != nil {
		var capabilityErr *sessionturn.ImageCapabilityError
		if !errors.As(err, &capabilityErr) {
			return err
		}
		capabilities = sessionturn.ImageCapabilities{Model: capabilityErr.Model}
	}
	request.ImageCapabilities = &capabilities
	return nil
}

func (s *Service) resolvePolicy(request sessionturn.Request) (tools.InteractiveToolPolicy, error) {
	policy := request.InteractiveToolPolicy
	policyRequest := request.ToolPolicyRequest
	if policy != nil {
		return policy, nil
	}
	if policyRequest == nil && s != nil && s.deps.PolicyFactory != nil && requiresPolicy(request) {
		settings := tools.InteractiveToolPolicySettings{}
		if request.ToolPolicySettings != nil {
			settings = *request.ToolPolicySettings
		}
		policyRequest = &tools.InteractiveToolPolicyRequest{
			Settings:                 settings,
			Definitions:              request.ToolDefinitions,
			BaseDefinitions:          request.ToolDefinitionBase,
			ExplicitLongRunningNames: defaultLongRunningToolNames(),
			DynamicLongRunning:       request.DynamicToolPolicy,
		}
	}
	if policyRequest == nil {
		return nil, nil
	}
	if s == nil || s.deps.PolicyFactory == nil {
		return nil, errors.New("session turn policy factory is not configured")
	}
	return s.deps.PolicyFactory.Resolve(*policyRequest)
}

func requiresPolicy(request sessionturn.Request) bool {
	return request.ToolExecutor != nil || len(request.ToolDefinitions) != 0 ||
		request.ToolPolicySettings != nil || len(request.ToolDefinitionBase) != 0 || request.DynamicToolPolicy
}

func defaultLongRunningToolNames() []string {
	return []string{
		"webmcp_select_tab", "webmcp_invoke", "webmcp_list_tools", "webmcp_list_tabs",
		"webmcp_get_context", "webmcp_cancel", "webmcp_list_cast_devices", "webmcp_cast_tab",
		"webmcp_stop_casting",
	}
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
	if inferencer == nil {
		return "", nil, sessionturn.ErrMissingTurnInferencer
	}
	wirePrompt := seedService.Allocate()
	if wirePrompt == "" {
		return "", nil, errors.New("session turn seed allocator returned an empty prompt")
	}
	return wirePrompt, seedService.WrapInferencer(inferencer, wirePrompt, requested), nil
}

func resolveInstructions(ctx context.Context, request sessionturn.Request, instructionService session.InstructionService) (string, error) {
	return resolveInstructionRequest(ctx, sessionturn.InstructionRequest{
		Service:     request.Instructions.Service,
		Request:     request.Instructions.Request,
		Composition: request.Instructions.Composition,
		Text:        request.InstructionsText,
	}, instructionService)
}

func resolveInstructionRequest(ctx context.Context, request sessionturn.InstructionRequest, instructionService session.InstructionService) (string, error) {
	instructions := request.Text
	if instructions == "" {
		instructions = request.Request.Prompt
	}
	service := request.Service
	if service == nil {
		service = instructionService
	}
	if service != nil && (request.Service != nil || (request.Composition == nil && request.Text == "")) {
		resolved, err := service.Resolve(ctx, request.Request)
		if err != nil {
			return "", err
		}
		instructions = resolved.Instructions
	}
	if request.Composition != nil {
		if service == nil {
			service = instructionService
		}
		if service == nil {
			return "", errors.New("session turn instruction service is not configured")
		}
		composition := *request.Composition
		composition.Instructions = instructions
		instructions = service.Compose(composition)
	}
	return instructions, nil
}

type runtime struct {
	service         *Service
	inferencer      messages.SessionInferencer
	turns           *turns.Service
	toolExecutor    messages.ToolExecutor
	toolDefinitions []messages.ToolDefinition
	policy          tools.InteractiveToolPolicy
	output          *seed.Service
	wirePrompt      string
	continuation    sessiontrace.LifecycleService
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
func (r *runtime) Continuation() sessiontrace.LifecycleService        { return r.continuation }

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
	var publication sessionturn.Publication
	var err error
	if r.service == nil {
		publication, err = startPublication(ctx, request)
	} else {
		publication, err = r.service.StartPublication(ctx, request)
	}
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.publication = publication
	r.mu.Unlock()
	return publication, nil
}

func (*Service) StartPublication(ctx context.Context, request sessionturn.PublicationRequest) (sessionturn.Publication, error) {
	return startPublication(ctx, request)
}
func (r *runtime) NewOutput(writer io.Writer) sessionturn.Output { return r.output.NewOutput(writer) }

func (r *runtime) AttachAudioOutput(inferencer messages.SessionInferencer, observer sessionturn.AudioDeltaObserver) (sessionturn.AudioOutputRuntime, error) {
	if r == nil || inferencer == nil {
		return nil, sessionturn.ErrMissingTurnInferencer
	}
	if observer == nil {
		return nil, sessionturn.ErrMissingAudioOutputObserver
	}
	return newAudioOutputRuntime(inferencer, observer), nil
}

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
		if closer, ok := r.toolExecutor.(interface{ Close() error }); ok {
			// A tool deadline has already been projected as the correlated tool
			// failure on the session stream. Do not turn a best-effort bounded
			// executor drain into a second session failure while still retaining
			// all other shutdown causes.
			if err := closer.Close(); err != nil && !errors.Is(err, context.DeadlineExceeded) {
				closeErr = errors.Join(closeErr, err)
			}
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
