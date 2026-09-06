package wire

import (
	"fmt"
	"reflect"

	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/observability"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

type portDefinition struct {
	descriptor   PortDescriptor
	value        func(*compositionValues) any
	assign       func(*compositionValues, any)
	defaultValue func(toolDefaults) any
}

// normalizeClock is called only by composition entry points. Named swaps are
// validated first, so an explicit nil clock replacement cannot be defaulted.
func normalizeClock(values *compositionValues) {
	values.clockSource = clock.Ensure(values.clockSource)
}

// LivePorts returns the authoritative live port list in deterministic order.
func LivePorts() []PortDescriptor {
	definitions := livePortDefinitions()
	ports := make([]PortDescriptor, len(definitions))
	for index, definition := range definitions {
		ports[index] = definition.descriptor
	}
	return ports
}

// RegisteredPorts is a descriptive alias for LivePorts.
func RegisteredPorts() []PortDescriptor { return LivePorts() }

func validateDependencies(values *compositionValues) error {
	return validateDependenciesWithDefinitions(values, livePortDefinitions())
}

func validateDependenciesWithDefinitions(values *compositionValues, definitions []portDefinition) error {
	for _, definition := range definitions {
		if definition.descriptor.Required && isNilPort(definition.value(values)) {
			return &MissingPortError{Name: definition.descriptor.Name}
		}
	}
	return nil
}

func findPortDefinitionIn(definitions []portDefinition, name string) (portDefinition, bool) {
	for _, definition := range definitions {
		if definition.descriptor.Name == name {
			return definition, true
		}
	}
	return portDefinition{}, false
}

func validatePortSwaps(definitions []portDefinition, swaps []PortSwap) error {
	seen := make(map[string]struct{}, len(swaps))
	for _, swap := range swaps {
		definition, ok := findPortDefinitionIn(definitions, swap.Name)
		if !ok {
			return &PortSwapError{Name: swap.Name, Reason: "unknown port", cause: ErrUnknownPort}
		}
		if _, duplicate := seen[swap.Name]; duplicate {
			return &PortSwapError{Name: swap.Name, Reason: "duplicate replacement", cause: ErrDuplicatePortSwap}
		}
		seen[swap.Name] = struct{}{}
		if err := validatePortSwap(definition, swap.Value); err != nil {
			return err
		}
	}
	return nil
}

func validatePortSwap(definition portDefinition, value any) error {
	if isNilPort(value) {
		if definition.descriptor.Required {
			return &PortSwapError{
				Name:   definition.descriptor.Name,
				Reason: "required replacement is nil",
				cause:  ErrInvalidPortSwap,
			}
		}
		return nil
	}

	actual := reflect.TypeOf(value)
	if !actual.Implements(definition.descriptor.Type) {
		return &PortSwapError{
			Name:     definition.descriptor.Name,
			Reason:   fmt.Sprintf("replacement type %s does not implement %s", actual, definition.descriptor.Type),
			Expected: definition.descriptor.Type,
			Actual:   actual,
			cause:    ErrIncompatiblePort,
		}
	}
	return nil
}

func isNilPort(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	case reflect.Invalid, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16,
		reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16,
		reflect.Uint32, reflect.Uint64, reflect.Uintptr, reflect.Float32,
		reflect.Float64, reflect.Complex64, reflect.Complex128, reflect.Array,
		reflect.String, reflect.Struct, reflect.UnsafePointer:
		return false
	default:
		return false
	}
}

// livePortDefinitions is the sole live port representation. Validation,
// public discovery, and mock swapping all iterate this function's result.
func livePortDefinitions() []portDefinition {
	return []portDefinition{
		metricSamplerPort(), loggerPort(), toolExecutorPort(), toolServicePort(), transportDialerPort(),
		inferencerPort(), sessionInferencerPort(), deviceRegistryPort(), audioSourcePort(),
		audioSinkPort(), clockPort(), sessionRuntimeObserverPort(),
	}
}

func toolServicePort() portDefinition {
	return portDefinition{
		descriptor:   PortDescriptor{Name: PortToolService, Required: false, Type: reflect.TypeOf((*serviceTools.Service)(nil)).Elem()},
		value:        func(values *compositionValues) any { return values.toolService },
		defaultValue: nil,
		assign: func(values *compositionValues, value any) {
			if value == nil {
				values.toolService = nil
				return
			}
			if service, ok := value.(serviceTools.Service); ok {
				values.toolService = service
			}
		},
	}
}

func metricSamplerPort() portDefinition {
	return portDefinition{
		descriptor:   PortDescriptor{Name: PortMetricSampler, Required: true, Type: reflect.TypeOf((*MetricSampler)(nil)).Elem()},
		value:        func(values *compositionValues) any { return values.metricSampler },
		defaultValue: func(toolDefaults) any { return observability.NewNoopMetricSampler() },
		assign: func(values *compositionValues, value any) {
			if value == nil {
				values.metricSampler = nil
				return
			}
			if metricSampler, ok := value.(MetricSampler); ok {
				values.metricSampler = metricSampler
			}
		},
	}
}

func loggerPort() portDefinition {
	return portDefinition{
		descriptor:   PortDescriptor{Name: PortLogger, Required: true, Type: reflect.TypeOf((*Logger)(nil)).Elem()},
		value:        func(values *compositionValues) any { return values.logger },
		defaultValue: func(toolDefaults) any { return observability.NewNoopLogger() },
		assign: func(values *compositionValues, value any) {
			if value == nil {
				values.logger = nil
				return
			}
			if logger, ok := value.(Logger); ok {
				values.logger = logger
			}
		},
	}
}

func toolExecutorPort() portDefinition {
	return portDefinition{
		descriptor: PortDescriptor{Name: PortToolExecutor, Required: true, Type: reflect.TypeOf((*messages.ToolExecutor)(nil)).Elem()},
		value:      func(values *compositionValues) any { return values.toolExecutor },
		defaultValue: func(defaults toolDefaults) any {
			return defaults.executor
		},
		assign: func(values *compositionValues, value any) {
			if value == nil {
				values.toolExecutor = nil
				return
			}
			if executor, ok := value.(messages.ToolExecutor); ok {
				values.toolExecutor = executor
			}
		},
	}
}

func transportDialerPort() portDefinition {
	return portDefinition{
		descriptor:   PortDescriptor{Name: PortTransportDialer, Required: true, Type: reflect.TypeOf((*transport.Dialer)(nil)).Elem()},
		value:        func(values *compositionValues) any { return values.transportDialer },
		defaultValue: func(toolDefaults) any { return defaultTransportDialer() },
		assign: func(values *compositionValues, value any) {
			if value == nil {
				values.transportDialer = nil
				return
			}
			if dialer, ok := value.(transport.Dialer); ok {
				values.transportDialer = dialer
			}
		},
	}
}

func inferencerPort() portDefinition {
	return portDefinition{
		descriptor:   PortDescriptor{Name: PortInferencer, Required: false, Type: reflect.TypeOf((*messages.Inferencer)(nil)).Elem()},
		value:        func(values *compositionValues) any { return values.inferencer },
		defaultValue: func(toolDefaults) any { return nil },
		assign: func(values *compositionValues, value any) {
			if value == nil {
				values.inferencer = nil
				return
			}
			if inferencer, ok := value.(messages.Inferencer); ok {
				values.inferencer = inferencer
			}
		},
	}
}

func sessionInferencerPort() portDefinition {
	return portDefinition{
		descriptor:   PortDescriptor{Name: PortSessionInferencer, Required: false, Type: reflect.TypeOf((*messages.SessionInferencer)(nil)).Elem()},
		value:        func(values *compositionValues) any { return values.sessionInferencer },
		defaultValue: func(toolDefaults) any { return nil },
		assign: func(values *compositionValues, value any) {
			if value == nil {
				values.sessionInferencer = nil
				return
			}
			if inferencer, ok := value.(messages.SessionInferencer); ok {
				values.sessionInferencer = inferencer
			}
		},
	}
}

func deviceRegistryPort() portDefinition {
	return portDefinition{
		descriptor:   PortDescriptor{Name: PortDeviceRegistry, Required: true, Type: reflect.TypeOf((*DeviceRegistry)(nil)).Elem()},
		value:        func(values *compositionValues) any { return values.deviceRegistry },
		defaultValue: func(toolDefaults) any { return defaultDeviceRegistry() },
		assign: func(values *compositionValues, value any) {
			if value == nil {
				values.deviceRegistry = nil
				return
			}
			if registry, ok := value.(DeviceRegistry); ok {
				values.deviceRegistry = registry
			}
		},
	}
}

func audioSourcePort() portDefinition {
	return portDefinition{
		descriptor:   PortDescriptor{Name: PortAudioSource, Required: true, Type: reflect.TypeOf((*AudioSource)(nil)).Elem()},
		value:        func(values *compositionValues) any { return values.audioSource },
		defaultValue: func(toolDefaults) any { return defaultAudioSource() },
		assign: func(values *compositionValues, value any) {
			if value == nil {
				values.audioSource = nil
				return
			}
			if source, ok := value.(AudioSource); ok {
				values.audioSource = source
			}
		},
	}
}

func audioSinkPort() portDefinition {
	return portDefinition{
		descriptor:   PortDescriptor{Name: PortAudioSink, Required: true, Type: reflect.TypeOf((*AudioSink)(nil)).Elem()},
		value:        func(values *compositionValues) any { return values.audioSink },
		defaultValue: func(toolDefaults) any { return defaultAudioSink() },
		assign: func(values *compositionValues, value any) {
			if value == nil {
				values.audioSink = nil
				return
			}
			if sink, ok := value.(AudioSink); ok {
				values.audioSink = sink
			}
		},
	}
}

func clockPort() portDefinition {
	return portDefinition{
		descriptor:   PortDescriptor{Name: PortClock, Required: true, Type: reflect.TypeOf((*Clock)(nil)).Elem()},
		value:        func(values *compositionValues) any { return values.clockSource },
		defaultValue: func(toolDefaults) any { return clock.Ensure(nil) },
		assign: func(values *compositionValues, value any) {
			if value == nil {
				values.clockSource = nil
				return
			}
			if source, ok := value.(Clock); ok {
				values.clockSource = source
			}
		},
	}
}

func sessionRuntimeObserverPort() portDefinition {
	return portDefinition{
		descriptor:   PortDescriptor{Name: PortSessionRuntimeObserver, Required: false, Type: reflect.TypeOf((*SessionRuntimeObserver)(nil)).Elem()},
		value:        func(values *compositionValues) any { return values.runtimeObserver },
		defaultValue: func(toolDefaults) any { return nil },
		assign: func(values *compositionValues, value any) {
			if value == nil {
				values.runtimeObserver = nil
				return
			}
			if observer, ok := value.(SessionRuntimeObserver); ok {
				values.runtimeObserver = observer
			}
		},
	}
}
