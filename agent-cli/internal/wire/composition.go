package wire

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// PortName is the stable name used to identify a composition dependency.
// It is an alias so callers can use constants and dynamic names without
// conversions when building test swaps.
type PortName = string

const (
	// PortToolExecutor is the required executor used by the agent CLI.
	PortToolExecutor = "tool-executor"
	// PortInferencer is the optional one-shot inference override.
	PortInferencer = "inferencer"
	// PortSessionInferencer is the optional bidirectional session override.
	PortSessionInferencer = "session-inferencer"

	// The *PortName constants make the port contract discoverable to callers
	// that prefer a name-oriented vocabulary.
	ToolExecutorPortName      = PortToolExecutor
	InferencerPortName        = PortInferencer
	SessionInferencerPortName = PortSessionInferencer
)

var (
	// ErrMissingRequiredPort identifies a construction failure caused by a
	// missing required dependency.
	ErrMissingRequiredPort = errors.New("missing required composition port")
	// ErrUnknownPort identifies a swap for a name that is not live.
	ErrUnknownPort = errors.New("unknown composition port")
	// ErrInvalidPortSwap identifies a malformed or nil-required replacement.
	ErrInvalidPortSwap = errors.New("invalid composition port swap")
	// ErrIncompatiblePort identifies a replacement with the wrong type.
	ErrIncompatiblePort = errors.New("incompatible composition port")

	// ErrMissingDependency is a compatibility alias for callers that used the
	// older dependency terminology.
	ErrMissingDependency = ErrMissingRequiredPort
)

// MissingPortError is returned before assembly when a required live port is
// nil. Its unwrap target is stable for errors.Is checks.
type MissingPortError struct {
	Name string
}

func (e *MissingPortError) Error() string {
	return fmt.Sprintf("composition port %q is required", e.Name)
}

func (e *MissingPortError) Unwrap() error { return ErrMissingRequiredPort }

// PortSwapError identifies an invalid named replacement. Name is deliberately
// retained in the error so callers and logs can point to the exact port.
type PortSwapError struct {
	Name     string
	Reason   string
	Expected reflect.Type
	Actual   reflect.Type
	cause    error
}

func (e *PortSwapError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("composition port %q: %s", e.Name, e.Reason)
	}
	return fmt.Sprintf("composition port %q: invalid replacement", e.Name)
}

func (e *PortSwapError) Unwrap() error { return e.cause }

// PortDescriptor is the public, read-only description of a live composition
// port. LivePorts returns a fresh slice so callers cannot mutate composition
// state.
type PortDescriptor struct {
	Name     string
	Required bool
	Type     reflect.Type
}

// PortSwap is one named replacement consumed by the unified mock initializer.
type PortSwap struct {
	Name  string
	Value any
}

// NewPortSwap constructs a named replacement for a live port.
func NewPortSwap(name string, value any) PortSwap {
	return PortSwap{Name: name, Value: value}
}

// SwapPort is a concise alias for NewPortSwap.
func SwapPort(name string, value any) PortSwap {
	return NewPortSwap(name, value)
}

// CompositionOption configures an optional composition capability. Required
// ports remain explicit parameters to ComposeAgentCLI.
type CompositionOption func(*compositionOptions) error

type compositionOptions struct {
	inferencer           messages.Inferencer
	sessionInferencer    messages.SessionInferencer
	relaxModelValidation bool
}

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

// WithRelaxedModelValidation preserves the test-only behavior of the legacy
// mock initializer without making validation mode a dependency port.
func WithRelaxedModelValidation() CompositionOption {
	return func(options *compositionOptions) error {
		options.relaxModelValidation = true
		return nil
	}
}

// WithStrictModelValidation explicitly selects production validation behavior.
func WithStrictModelValidation() CompositionOption {
	return func(options *compositionOptions) error {
		options.relaxModelValidation = false
		return nil
	}
}

// ComposeAgentCLI constructs the singular CLI root from the required tool
// executor. Optional capabilities are supplied through CompositionOption.
// Validation runs before any graph constructor is called.
func ComposeAgentCLI(toolExecutor messages.ToolExecutor, options ...CompositionOption) (*cli.AgentCLI, error) {
	compositionOptions, err := applyCompositionOptions(options)
	if err != nil {
		return nil, err
	}

	values := compositionValues{
		toolExecutor:      toolExecutor,
		inferencer:        compositionOptions.inferencer,
		sessionInferencer: compositionOptions.sessionInferencer,
	}
	if err := validateDependencies(&values); err != nil {
		return nil, err
	}

	// The registry is an internal source of tool definitions. It is not a
	// caller-facing dependency bag and is created only after validation.
	registry := tools.NewToolRegistry()
	return assembleAgentCLI(
		values.toolExecutor,
		services.DefaultToolDefs(registry),
		values.inferencer,
		values.sessionInferencer,
		compositionOptions.relaxModelValidation,
	)
}

// InitializeAgentCLI builds the production CLI with the registry-backed tool
// executor. It shares the same explicit assembly helper as all test paths.
func InitializeAgentCLI() (*cli.AgentCLI, error) {
	registry := tools.NewToolRegistry()
	values := compositionValues{toolExecutor: tools.NewRegistryExecutor(registry)}
	if err := validateDependencies(&values); err != nil {
		return nil, err
	}
	return assembleAgentCLI(
		values.toolExecutor,
		services.DefaultToolDefs(registry),
		values.inferencer,
		values.sessionInferencer,
		false,
	)
}

// InitializeMockAgentCLIWithPorts is the one uniform mock-injection entry
// point. Its cases are named replacements, validated against the same live
// port definitions used by required-port validation.
func InitializeMockAgentCLIWithPorts(swaps ...PortSwap) (*cli.AgentCLI, error) {
	registry := tools.NewToolRegistry()
	values := compositionValues{toolExecutor: tools.NewRegistryExecutor(registry)}
	for _, swap := range swaps {
		if err := applyPortSwap(&values, swap); err != nil {
			return nil, err
		}
	}
	if err := validateDependencies(&values); err != nil {
		return nil, err
	}
	return assembleAgentCLI(
		values.toolExecutor,
		services.DefaultToolDefs(registry),
		values.inferencer,
		values.sessionInferencer,
		true,
	)
}

// InitializeMockAgentCLI is retained for existing integration callers and
// forwards to the same composition path as InitializeMockAgentCLIWithPorts.
func InitializeMockAgentCLI(executor messages.ToolExecutor, inferencer messages.Inferencer) (*cli.AgentCLI, error) {
	return composeInjectedAgentCLI(executor, inferencer, nil, true)
}

// InitializeMockAgentCLIWithSessionInferencer is a compatibility forwarder;
// it does not create a second mock swap mechanism.
func InitializeMockAgentCLIWithSessionInferencer(executor messages.ToolExecutor, inferencer messages.Inferencer, sessionInferencer messages.SessionInferencer) (*cli.AgentCLI, error) {
	return composeInjectedAgentCLI(executor, inferencer, sessionInferencer, true)
}

// InitializeAgentCLIWithInferencerOverride is retained for tests that need
// strict model validation while replacing the one-shot inferencer.
func InitializeAgentCLIWithInferencerOverride(executor messages.ToolExecutor, inferencer messages.Inferencer) (*cli.AgentCLI, error) {
	return composeInjectedAgentCLI(executor, inferencer, nil, false)
}

func composeInjectedAgentCLI(toolExecutor messages.ToolExecutor, inferencer messages.Inferencer, sessionInferencer messages.SessionInferencer, relaxModelValidation bool) (*cli.AgentCLI, error) {
	values := compositionValues{
		toolExecutor:      toolExecutor,
		inferencer:        inferencer,
		sessionInferencer: sessionInferencer,
	}
	if err := validateDependencies(&values); err != nil {
		return nil, err
	}
	registry := tools.NewToolRegistry()
	return assembleAgentCLI(
		values.toolExecutor,
		services.DefaultToolDefs(registry),
		values.inferencer,
		values.sessionInferencer,
		relaxModelValidation,
	)
}

func applyCompositionOptions(options []CompositionOption) (compositionOptions, error) {
	var values compositionOptions
	for index, option := range options {
		if option == nil {
			return compositionOptions{}, fmt.Errorf("composition option %d is nil", index)
		}
		if err := option(&values); err != nil {
			return compositionOptions{}, fmt.Errorf("apply composition option %d: %w", index, err)
		}
	}
	return values, nil
}

type compositionValues struct {
	toolExecutor      messages.ToolExecutor
	inferencer        messages.Inferencer
	sessionInferencer messages.SessionInferencer
}

type portDefinition struct {
	descriptor PortDescriptor
	value      func(*compositionValues) any
	assign     func(*compositionValues, any)
}

// livePortDefinitions is the sole live port representation. Validation,
// public discovery, and mock swapping all iterate this function's result.
func livePortDefinitions() []portDefinition {
	return []portDefinition{
		{
			descriptor: PortDescriptor{
				Name:     PortToolExecutor,
				Required: true,
				Type:     reflect.TypeOf((*messages.ToolExecutor)(nil)).Elem(),
			},
			value: func(values *compositionValues) any { return values.toolExecutor },
			assign: func(values *compositionValues, value any) {
				if value == nil {
					values.toolExecutor = nil
					return
				}
				values.toolExecutor = value.(messages.ToolExecutor)
			},
		},
		{
			descriptor: PortDescriptor{
				Name:     PortInferencer,
				Required: false,
				Type:     reflect.TypeOf((*messages.Inferencer)(nil)).Elem(),
			},
			value: func(values *compositionValues) any { return values.inferencer },
			assign: func(values *compositionValues, value any) {
				if value == nil {
					values.inferencer = nil
					return
				}
				values.inferencer = value.(messages.Inferencer)
			},
		},
		{
			descriptor: PortDescriptor{
				Name:     PortSessionInferencer,
				Required: false,
				Type:     reflect.TypeOf((*messages.SessionInferencer)(nil)).Elem(),
			},
			value: func(values *compositionValues) any { return values.sessionInferencer },
			assign: func(values *compositionValues, value any) {
				if value == nil {
					values.sessionInferencer = nil
					return
				}
				values.sessionInferencer = value.(messages.SessionInferencer)
			},
		},
	}
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
	for _, definition := range livePortDefinitions() {
		if definition.descriptor.Required && isNilPort(definition.value(values)) {
			return &MissingPortError{Name: definition.descriptor.Name}
		}
	}
	return nil
}

func applyPortSwap(values *compositionValues, swap PortSwap) error {
	definition, ok := findPortDefinition(swap.Name)
	if !ok {
		return &PortSwapError{Name: swap.Name, Reason: "unknown port", cause: ErrUnknownPort}
	}
	if err := validatePortSwap(definition, swap.Value); err != nil {
		return err
	}
	definition.assign(values, swap.Value)
	return nil
}

func findPortDefinition(name string) (portDefinition, bool) {
	for _, definition := range livePortDefinitions() {
		if definition.descriptor.Name == name {
			return definition, true
		}
	}
	return portDefinition{}, false
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
	default:
		return false
	}
}
