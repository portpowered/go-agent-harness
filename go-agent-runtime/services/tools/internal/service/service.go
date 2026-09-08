// Package service owns the default tools capability implementation. It is
// reachable only from the tools service composition boundary.
package service

import (
	"context"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/browser"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/composition"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/filesystem"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/registry"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/skills"
)

// Service resolves the copied tool implementations against normalized public
// request values. The implementation package is deliberately kept private so
// callers cannot depend on registry or tool concrete types.
type Service struct{}

type invoker struct{ executor messages.ToolExecutor }

var _ public.Service = (*Service)(nil)

func New() *Service { return &Service{} }

// BrowserContract returns the pure browser protocol/result policy. The
// connection and platform adapters remain outside this implementation.
func (s *Service) BrowserContract() public.BrowserContract {
	return browser.Contract{}
}

// BuildSkillsSummary returns prompt-sized metadata from the host-resolved
// skill roots. The loader is read-only and keeps path discovery outside the
// reusable runtime boundary.
func (s *Service) BuildSkillsSummary(ctx context.Context, request public.SkillSummaryRequest) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	roots := make([]string, 0, len(request.SkillRoots))
	for _, root := range request.SkillRoots {
		if root.Directory != "" {
			roots = append(roots, root.Directory)
		}
	}
	return skills.NewLoaderFromRoots(roots...).BuildSummary()
}

// NewCleanupCoordinator transfers host capability cleanup into the tools
// service so session finalization has one owner for ordering and idempotence.
func (s *Service) NewCleanupCoordinator(cleanups ...func() error) public.CleanupCoordinator {
	return newCleanupCoordinator(cleanups...)
}

// NewCleanupCoordinatorWithTimeout is the deterministic/test-friendly form
// of NewCleanupCoordinator. A non-positive timeout uses the service default.
func (s *Service) NewCleanupCoordinatorWithTimeout(timeout time.Duration, cleanups ...func() error) public.CleanupCoordinator {
	return newCleanupCoordinatorWithTimeout(timeout, cleanups...)
}

func (s *Service) ValidateToolDefinitionNamespaces(staticDefinitions, browserDefinitions []messages.ToolDefinition) error {
	return composition.ValidateToolDefinitionNamespaces(staticDefinitions, browserDefinitions)
}

func (s *Service) Resolve(ctx context.Context, request public.Request) (public.Capability, error) {
	if err := ctx.Err(); err != nil {
		return public.Capability{}, err
	}
	if err := validateRequest(request); err != nil {
		return public.Capability{}, err
	}
	if request.Executor == nil && request.Browser == nil && !request.UseDefaultTool {
		return capabilityFromExecutor(request, nil, nil), nil
	}
	policy, err := resolveRequestPolicy(request)
	if err != nil {
		return public.Capability{}, err
	}
	if request.Executor != nil || request.Browser != nil {
		if request.Executor == nil && request.UseDefaultTool {
			defaultSurface := defaultCapability(request, policy)
			request.Executor = defaultSurface.Executor
			request.Definitions = defaultSurface.Definitions
			request.FilesystemPolicyApplied = true
		}
		return resolvedExternalCapability(request, policy)
	}
	return defaultCapability(request, policy), nil
}

func validateRequest(request public.Request) error {
	if request.Executor == nil && len(request.Definitions) > 0 && request.Browser == nil {
		return fmt.Errorf("tool definitions require an execution route")
	}
	if request.Browser != nil && request.Browser.Executor == nil && len(request.Browser.Definitions) > 0 {
		return fmt.Errorf("browser tool definitions require an execution route")
	}
	if request.Executor == nil && request.UseDefaultTool && request.WorkDir == "" {
		return fmt.Errorf("resolve filesystem scope: workdir is required for the default tool surface")
	}
	if request.WorkDir == "" && len(request.AllowPaths) > 0 {
		return fmt.Errorf("resolve filesystem scope: workdir is required when additional roots are supplied")
	}
	return nil
}

func resolveRequestPolicy(request public.Request) (*filesystem.FilesystemPolicy, error) {
	if request.WorkDir == "" {
		return nil, nil
	}
	policy, err := filesystem.ResolveFilesystemPolicy(request.WorkDir, request.AllowPaths...)
	if err != nil {
		return nil, fmt.Errorf("resolve filesystem scope: %w", err)
	}
	return policy, nil
}

func scopedExecutor(executor messages.ToolExecutor, policy *filesystem.FilesystemPolicy) messages.ToolExecutor {
	if policy == nil {
		return executor
	}
	return registry.ApplyFilesystemPolicy(executor, policy)
}

func resolvedExternalCapability(request public.Request, policy *filesystem.FilesystemPolicy) (public.Capability, error) {
	staticExecutor := request.Executor
	if !request.FilesystemPolicyApplied {
		staticExecutor = scopedExecutor(staticExecutor, policy)
	}
	if request.Browser == nil {
		return capabilityFromExecutor(request, policy, staticExecutor), nil
	}

	browser := request.Browser
	surface, err := composition.ComposeToolSurface(
		staticExecutor,
		request.Definitions,
		browser.Executor,
		browser.Definitions,
	)
	if err != nil {
		return public.Capability{}, fmt.Errorf("compose browser tools: %w", err)
	}

	capability := public.Capability{
		Executor:    surface.Executor,
		Definitions: append([]messages.ToolDefinition(nil), surface.Definitions...),
	}
	if policy != nil {
		capability.WorkspaceDir = policy.PrimaryRoot()
		capability.AdditionalDirs = policy.AdditionalRoots()
	}
	capability.Invoker = invoker{executor: capability.Executor}
	capability.Handle = newBrowserCapabilityHandle(request.Definitions, staticExecutor, *browser)
	return capability, nil
}

func defaultCapability(request public.Request, policy *filesystem.FilesystemPolicy) public.Capability {
	var toolRegistry *registry.ToolRegistry
	if request.DisplayCapabilitySet {
		toolRegistry = registry.NewToolRegistryWithDisplayCapabilityAndPolicyAndSkillRoots(
			registry.RegistryOptions{Selections: request.Selections, Exec: request.Exec},
			request.DisplayCapability,
			request.DisplaySurface,
			policy,
			skillRootDirectories(request.SkillRoots),
			request.DiagnosticWriter,
		)
	} else {
		toolRegistry = registry.NewToolRegistryWithPolicyAndSkillRoots(
			registry.RegistryOptions{Selections: request.Selections, Exec: request.Exec},
			policy,
			skillRootDirectories(request.SkillRoots),
			request.DiagnosticWriter,
		)
	}
	if registry.SelectionEnabled(request.Selections, "dispatch_agent") && request.Inferencer != nil {
		if err := toolRegistry.Register(registry.NewDispatchAgentTool(request.Inferencer, toolRegistry)); err != nil {
			panic(fmt.Errorf("register dispatch_agent tool: %w", err))
		}
	}
	capability := public.Capability{
		Executor:       registry.NewRegistryExecutor(toolRegistry),
		Definitions:    toolRegistry.ToAgentLoopDefs(),
		WorkspaceDir:   policy.PrimaryRoot(),
		AdditionalDirs: policy.AdditionalRoots(),
	}
	capability.Invoker = invoker{executor: capability.Executor}
	capability.Handle = newCapabilityHandle(capability.Definitions)
	return capability
}

func skillRootDirectories(roots []public.SkillRoot) []string {
	directories := make([]string, 0, len(roots))
	for _, root := range roots {
		if root.Directory != "" {
			directories = append(directories, root.Directory)
		}
	}
	return directories
}

func capabilityFromExecutor(request public.Request, policy *filesystem.FilesystemPolicy, executor messages.ToolExecutor) public.Capability {
	capability := public.Capability{
		Executor:    executor,
		Definitions: append([]messages.ToolDefinition(nil), request.Definitions...),
	}
	if policy != nil {
		capability.WorkspaceDir = policy.PrimaryRoot()
		capability.AdditionalDirs = policy.AdditionalRoots()
	}
	if executor != nil {
		capability.Invoker = invoker{executor: executor}
	}
	if executor != nil || len(capability.Definitions) > 0 {
		capability.Handle = newCapabilityHandle(capability.Definitions)
	}
	return capability
}

func (i invoker) Invoke(ctx context.Context, invocation public.Invocation) (public.InvocationResult, error) {
	if i.executor == nil {
		return public.InvocationResult{}, fmt.Errorf("tool capability has no executor")
	}
	response, err := i.executor.Execute(ctx, messages.ToolCall{ID: invocation.ID, Name: invocation.Name, Arguments: invocation.Arguments})
	if err != nil {
		return public.InvocationResult{}, err
	}
	return public.InvocationResult{ID: response.ToolCallID, Content: response.Content, ContentParts: response.ContentParts}, nil
}
