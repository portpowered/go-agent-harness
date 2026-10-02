package wire

import (
	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// WithInferencer supplies the optional one-shot inference override. Passing
// nil explicitly leaves the override unavailable.
func WithInferencer(inferencer messages.Inferencer) CompositionOption {
	return func(options *compositionOptions) error {
		options.inferencer = inferencer
		return nil
	}
}

// WithSessionInferencer supplies the optional session inference override.
func WithSessionInferencer(inferencer messages.SessionInferencer) CompositionOption {
	return func(options *compositionOptions) error {
		options.sessionInferencer = inferencer
		return nil
	}
}

// WithToolService supplies a complete request-scoped tool capability service
// for a composed CLI. A nil value leaves the built-in service graph selected.
func WithToolService(service serviceTools.Service) CompositionOption {
	return func(options *compositionOptions) error {
		options.toolService = service
		return nil
	}
}

// WithSessionRuntimeObserver supplies the optional command-runtime evidence
// sink used by hermetic callers that need clock-stamped session observations.
func WithSessionRuntimeObserver(observer SessionRuntimeObserver) CompositionOption {
	return func(options *compositionOptions) error {
		options.runtimeObserver = observer
		return nil
	}
}
