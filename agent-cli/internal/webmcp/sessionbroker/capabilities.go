package sessionbroker

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtime "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation"
)

// ToolCapabilities is the config-scoped tool surface used by a composed
// session command. Executor and Definitions are derived from the same loaded
// config snapshot so the session cannot advertise a tool that its executor
// does not expose.
type ToolCapabilities struct {
	Executor    messages.ToolExecutor
	Definitions []messages.ToolDefinition
	// BrowserCapabilityState is the session-owned browser state used to
	// compose model-facing grounding. It is independent from whether the
	// current definition snapshot happens to contain first-class page tools.
	BrowserCapabilityState webmcp.BrowserCapabilityState
	// DisplayCapability is the immutable host-surface admission result used
	// to derive both display-dependent definitions and executor routes.
	DisplayCapability cliTools.DisplayCapability
	// RefreshDefinitions returns the final definition list after Initialize
	// has run: the composed static and stable broker definitions plus any
	// first-class page tools read from the connected browser catalog. Nil
	// means Definitions is already final.
	RefreshDefinitions func(context.Context) []messages.ToolDefinition
	// RefreshDefinitionsWithError is the error-preserving form used by the
	// live session publisher. A catalog read failure must not be collapsed into
	// an empty page surface and treated as a successful provider update.
	RefreshDefinitionsWithError func(context.Context) ([]messages.ToolDefinition, error)
	// Initialize is called synchronously after capability construction and
	// before the session provider can issue a browser tool call. An initialization
	// error prevents provider startup, because advertising browser tools without
	// a usable browser leaves the model in an unrecoverable session.
	Initialize func(context.Context) error
	// Status reports the explicit lifecycle state of an optional capability.
	Status func() Status
	// BrowserWatch exposes the already-owned broker observation stream to an
	// opt-in live session input boundary. It is nil for non-browser capability
	// sets; callers must use the returned context to stop the watch.
	BrowserWatch func(context.Context) <-chan webmcp.BrokerEvent
	// BrowserEventWatch exposes the richer adapter-owned semantic browser event
	// stream to the opt-in recording observer. It is independent from
	// BrowserWatch and never participates in tool execution or continuation.
	BrowserEventWatch func(context.Context) <-chan webmcp.BrowserEvent
	// Close transfers ownership of any capability resources to the session
	// coordinator. Nil means this capability has no closeable resources.
	Close func() error
}

// ToolCapabilitiesFactory builds the session tool surface from the config selected by
// --config-dir. It is optional so direct command constructors and callers
// that intentionally inject a no-tools session keep their existing behavior.
type ToolCapabilitiesFactory func(*config.Config) (ToolCapabilities, error)

// FactoryFromService adapts the injected service contract to the command's
// lifecycle value without constructing a registry or browser.
func FactoryFromService(resolver serviceTools.Service) ToolCapabilitiesFactory {
	return func(cfg *config.Config) (ToolCapabilities, error) {
		if resolver == nil {
			return ToolCapabilities{}, errors.New("session tool capability service is not configured")
		}
		capabilities, err := resolver.Resolve(cfg)
		if err != nil {
			return ToolCapabilities{}, err
		}
		return fromService(capabilities), nil
	}
}

// Resolve adapts the capability factory to the injected tool service
// contract. Production composition injects its private service.
func (factory ToolCapabilitiesFactory) Resolve(cfg *config.Config) (serviceTools.Capabilities, error) {
	if factory == nil {
		return serviceTools.Capabilities{}, errors.New("session capability factory is required")
	}
	capabilities, err := factory(cfg)
	if err != nil {
		return serviceTools.Capabilities{}, err
	}
	status := capabilities.Status
	return serviceTools.Capabilities{
		Executor: capabilities.Executor, Definitions: capabilities.Definitions,
		BrowserCapabilityState:      capabilities.BrowserCapabilityState,
		DisplayCapability:           capabilities.DisplayCapability,
		RefreshDefinitions:          capabilities.RefreshDefinitions,
		RefreshDefinitionsWithError: capabilities.RefreshDefinitionsWithError,
		Initialize:                  capabilities.Initialize,
		BrowserWatch:                capabilities.BrowserWatch,
		BrowserEventWatch:           runtimeEventWatch(capabilities.BrowserEventWatch),
		Close:                       capabilities.Close,
		Status: func() Status {
			if status == nil {
				return Status{}
			}
			return status()
		},
	}, nil
}

// ServiceCapability adapts a session broker to the tool service's browser
// capability seam, exposing its lifecycle hooks when it has them.
func ServiceCapability(broker webmcp.Broker) serviceTools.BrowserCapability {
	result := serviceTools.BrowserCapability{Broker: broker}
	if broker == nil {
		return result
	}
	if initializer, ok := broker.(Initializer); ok {
		result.Initialize = initializer.InitializeSession
		result.Status = initializer.SessionCapabilityStatus
	} else {
		result.Status = func() Status {
			return Status{BrowserCapabilityState: webmcp.BrowserCapabilityInitializing}
		}
	}
	result.BrowserWatch = broker.Watch
	if watcher, ok := broker.(webmcp.BrowserEventWatcher); ok {
		result.BrowserEventWatch = func(ctx context.Context) <-chan runtime.BrowserEvent {
			return ToRuntimeEvents(ctx, watcher.WatchBrowserEvents(ctx))
		}
	}
	result.Close = broker.Close
	return result
}

func fromService(capabilities serviceTools.Capabilities) ToolCapabilities {
	watch := capabilities.BrowserEventWatch
	return ToolCapabilities{
		Executor: capabilities.Executor, Definitions: capabilities.Definitions,
		BrowserCapabilityState:      capabilities.BrowserCapabilityState,
		DisplayCapability:           capabilities.DisplayCapability,
		RefreshDefinitions:          capabilities.RefreshDefinitions,
		RefreshDefinitionsWithError: capabilities.RefreshDefinitionsWithError,
		Initialize:                  capabilities.Initialize, Status: capabilities.Status,
		BrowserWatch: capabilities.BrowserWatch,
		BrowserEventWatch: func(ctx context.Context) <-chan webmcp.BrowserEvent {
			if watch == nil {
				return nil
			}
			return ToWebMCPEvents(ctx, watch(ctx))
		},
		Close: capabilities.Close,
	}
}

func runtimeEventWatch(watch func(context.Context) <-chan webmcp.BrowserEvent) func(context.Context) <-chan runtime.BrowserEvent {
	return func(ctx context.Context) <-chan runtime.BrowserEvent {
		if watch == nil {
			return nil
		}
		return ToRuntimeEvents(ctx, watch(ctx))
	}
}
