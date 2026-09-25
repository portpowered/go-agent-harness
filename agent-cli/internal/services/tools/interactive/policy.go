// Package interactive binds the configured voice/realtime tool latency policy
// to a live capability surface. It is a stateless host adapter: the
// session-turn runtime service owns classification, deadlines, panic
// isolation, correlated failure results, and operator diagnostics; the live
// session runtime owns the spoken acknowledgement for the same policy.
package interactive

import (
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	sessionturnwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/wire"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

// Binding describes one live invocation's tool latency inputs.
type Binding struct {
	// Config is the loaded configuration snapshot; nil selects the defaults.
	Config *config.Config
	// Timeout, when positive, overrides the per-tool policy for every call.
	Timeout time.Duration
	// BrowserToolsEnabled admits page tools registered after the initial
	// snapshot to the long-running budget.
	BrowserToolsEnabled bool
	// Cancellation keeps an operator SIGINT distinct from a tool failure.
	Cancellation sessiontrace.CancellationIntent
	// Diagnostics receives each original tool error for the operator; the
	// provider sees only the customer-safe correlated result.
	Diagnostics sessiontrace.ToolDiagnosticSink
}

// Bind wraps the capability executor with the session-turn executor so each
// call receives the interactive deadline for its tool class, and records the
// resolved policy so the live runtime acknowledges long-running calls. A
// capability without an executor is left unchanged.
func Bind(capabilities *runtimeSession.LiveCapabilities, binding Binding) error {
	if capabilities == nil || capabilities.Executor == nil {
		return nil
	}
	service := newTurnService()
	policy, err := ResolvePolicy(service, binding, capabilities)
	if err != nil {
		return err
	}
	request := sessionturn.ToolExecutorRequest{
		Inner: capabilities.Executor, Timeout: binding.Timeout, Policy: policy, Presentation: Presentation(),
		Diagnostics: binding.Diagnostics,
	}
	if binding.Cancellation != nil {
		request.Cancellation = binding.Cancellation
	}
	bounded := service.NewToolExecutor(request)
	if allowsUnadvertisedTools(capabilities.Executor) {
		bounded = unadvertisedToolExecutor{ToolExecutor: bounded}
	}
	capabilities.Executor = bounded
	capabilities.ToolPolicy = policy
	return nil
}

// unadvertisedToolExecutor preserves a test replacement executor's marker so
// the live runtime does not restrict it to the advertised surface.
type unadvertisedToolExecutor struct{ messages.ToolExecutor }

func (unadvertisedToolExecutor) AllowUnadvertisedTools() bool { return true }

func allowsUnadvertisedTools(executor messages.ToolExecutor) bool {
	marked, ok := executor.(interface{ AllowUnadvertisedTools() bool })
	return ok && marked.AllowUnadvertisedTools()
}

// ResolvePolicy classifies the capability's advertised definitions with the
// configured interactive settings.
func ResolvePolicy(service sessionturn.ToolService, binding Binding, capabilities *runtimeSession.LiveCapabilities) (runtimeTools.InteractiveToolPolicy, error) {
	settings := config.DefaultInteractiveToolConfig()
	if binding.Config != nil {
		resolved, err := binding.Config.ResolveInteractiveToolConfig()
		if err != nil {
			return nil, fmt.Errorf("resolve interactive tool policy: %w", err)
		}
		settings = resolved
	}
	policy, err := service.ResolveInteractivePolicy(sessionturn.InteractivePolicyRequest{
		Settings: &runtimeTools.InteractiveToolPolicySettings{
			FastReadTimeout: settings.FastReadTimeout, LongRunningTimeout: settings.LongRunningTimeout,
			AcknowledgementThreshold: settings.AcknowledgementThreshold,
		},
		Definitions: capabilities.Definitions, BaseDefinitions: capabilities.Definitions,
		DynamicLongRunning: binding.BrowserToolsEnabled,
	})
	if err != nil {
		return nil, fmt.Errorf("resolve interactive tool policy: %w", err)
	}
	return policy, nil
}

func newTurnService() sessionturn.Service {
	return sessionturnwire.NewService(runtimeToolsWire.NewInteractiveToolPolicy())
}
