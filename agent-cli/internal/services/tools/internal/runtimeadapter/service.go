package runtimeadapter

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

// New bridges the CLI's config-resolved capability
// service into the reusable runtime contract. The adapter lives at the host
// composition edge: session execution receives only runtimeTools.Service and
// never imports CLI configuration or browser implementations.
func New(host serviceTools.Service, fallback runtimeTools.Service) runtimeTools.Service {
	if host == nil {
		return fallback
	}
	return &runtimeToolServiceAdapter{host: host, fallback: fallback}
}

type runtimeToolServiceAdapter struct {
	host     serviceTools.Service
	fallback runtimeTools.Service
}

var _ runtimeTools.Service = (*runtimeToolServiceAdapter)(nil)

func (a *runtimeToolServiceAdapter) Resolve(ctx context.Context, request runtimeTools.Request) (runtimeTools.Capability, error) {
	if a == nil || a.host == nil {
		if a == nil || a.fallback == nil {
			return runtimeTools.Capability{}, errors.New("runtime tool service is required")
		}
		return a.fallback.Resolve(ctx, request)
	}
	if ctx == nil {
		return runtimeTools.Capability{}, errors.New("tool resolution context is required")
	}
	if err := ctx.Err(); err != nil {
		return runtimeTools.Capability{}, err
	}
	capabilities, err := a.host.Resolve(cliToolConfig(request))
	if err != nil {
		return runtimeTools.Capability{}, fmt.Errorf("resolve host tool capabilities: %w", err)
	}
	return runtimeToolCapability(capabilities), nil
}

func (a *runtimeToolServiceAdapter) BuildSkillsSummary(ctx context.Context, request runtimeTools.SkillSummaryRequest) (string, error) {
	if a == nil || a.fallback == nil {
		return "", errors.New("runtime tool service is required")
	}
	return a.fallback.BuildSkillsSummary(ctx, request)
}

func (a *runtimeToolServiceAdapter) BrowserContract() runtimeTools.BrowserContract {
	if a == nil || a.fallback == nil {
		return nil
	}
	return a.fallback.BrowserContract()
}

func (a *runtimeToolServiceAdapter) NewCleanupCoordinator(cleanups ...func() error) runtimeTools.CleanupCoordinator {
	if a == nil || a.fallback == nil {
		return nil
	}
	return a.fallback.NewCleanupCoordinator(cleanups...)
}

func (a *runtimeToolServiceAdapter) NewCleanupCoordinatorWithTimeout(timeout time.Duration, cleanups ...func() error) runtimeTools.CleanupCoordinator {
	if a == nil || a.fallback == nil {
		return nil
	}
	return a.fallback.NewCleanupCoordinatorWithTimeout(timeout, cleanups...)
}

func cliToolConfig(request runtimeTools.Request) *config.Config {
	result := &config.Config{
		Browser:              config.DefaultBrowserConfig(),
		FilesystemWorkDir:    request.WorkDir,
		FilesystemAllowPaths: append([]string(nil), request.AllowPaths...),
	}
	if len(request.Selections) > 0 {
		result.Tools.List = make([]config.ToolEntry, 0, len(request.Selections))
		for _, selection := range request.Selections {
			result.Tools.List = append(result.Tools.List, config.ToolEntry{ID: selection.ID, Enabled: selection.Enabled})
		}
	}
	result.Tools.Exec.EnableDenyPatterns = request.Exec.EnableDenyPatterns
	result.Tools.Exec.CustomDenyPatterns = append([]string(nil), request.Exec.CustomDenyPatterns...)
	return result
}

func runtimeToolCapability(capabilities serviceTools.Capabilities) runtimeTools.Capability {
	definitions := messages.CanonicalToolDefinitions(capabilities.Definitions)
	result := runtimeTools.Capability{
		Executor:    capabilities.Executor,
		Definitions: definitions,
	}
	if capabilities.Executor != nil {
		result.Invoker = runtimeToolInvoker{executor: capabilities.Executor}
	}
	if capabilities.Initialize != nil || capabilities.RefreshDefinitions != nil || capabilities.RefreshDefinitionsWithError != nil || capabilities.Close != nil {
		result.Handle = &runtimeToolCapabilityHandle{
			definitions: definitions,
			initialize:  capabilities.Initialize,
			refresh:     capabilities.RefreshDefinitions,
			refreshErr:  capabilities.RefreshDefinitionsWithError,
			close:       capabilities.Close,
		}
	}
	return result
}

type runtimeToolInvoker struct{ executor messages.ToolExecutor }

func (i runtimeToolInvoker) Invoke(ctx context.Context, invocation runtimeTools.Invocation) (runtimeTools.InvocationResult, error) {
	if i.executor == nil {
		return runtimeTools.InvocationResult{}, errors.New("tool capability has no executor")
	}
	response, err := i.executor.Execute(ctx, messages.ToolCall{
		ID: invocation.ID, Name: invocation.Name, Arguments: invocation.Arguments,
	})
	if err != nil {
		return runtimeTools.InvocationResult{}, err
	}
	return runtimeTools.InvocationResult{
		ID:           response.ToolCallID,
		Content:      response.Content,
		ContentParts: response.ContentParts,
	}, nil
}

type runtimeToolCapabilityHandle struct {
	definitions []messages.ToolDefinition
	initialize  func(context.Context) error
	refresh     func(context.Context) []messages.ToolDefinition
	refreshErr  func(context.Context) ([]messages.ToolDefinition, error)
	close       func() error
	closeOnce   sync.Once
	closeErr    error
}

var _ runtimeTools.CapabilityHandle = (*runtimeToolCapabilityHandle)(nil)

func (h *runtimeToolCapabilityHandle) Initialize(ctx context.Context) error {
	if h == nil || h.initialize == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("capability initialization context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return h.initialize(ctx)
}

func (h *runtimeToolCapabilityHandle) RefreshDefinitions(ctx context.Context) ([]messages.ToolDefinition, error) {
	if h == nil {
		return nil, nil
	}
	if ctx == nil {
		return nil, errors.New("capability refresh context is required")
	}
	if h.refreshErr != nil {
		definitions, err := h.refreshErr(ctx)
		if err != nil {
			return nil, err
		}
		return messages.CanonicalToolDefinitions(definitions), nil
	}
	if h.refresh != nil {
		return messages.CanonicalToolDefinitions(h.refresh(ctx)), nil
	}
	return messages.CanonicalToolDefinitions(h.definitions), nil
}

func (h *runtimeToolCapabilityHandle) Close() error {
	if h == nil {
		return nil
	}
	h.closeOnce.Do(func() {
		if h.close != nil {
			h.closeErr = invokeToolClose(h.close)
		}
	})
	return h.closeErr
}

func invokeToolClose(closeFn func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: %v", runtimeTools.ErrCapabilityClosePanic, recovered)
		}
	}()
	return closeFn()
}
