package wire

import (
	"context"
	"errors"
	"fmt"
	rtcontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentruntime/transports"
	"reflect"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	hostServices "github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/observability"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// PortName is the stable name used to identify a composition dependency.
// It is an alias so callers can use constants and dynamic names without
// conversions when building test swaps.
type PortName = string

const (
	// PortToolExecutor is the required executor used by the agent CLI.
	PortToolExecutor = "tool-executor"
	// PortTransportDialer is the required provider-neutral transport seam.
	PortTransportDialer = "transport-dialer"
	// PortInferencer is the optional one-shot inference override.
	PortInferencer = "inferencer"
	// PortSessionInferencer is the optional bidirectional session override.
	PortSessionInferencer = "session-inferencer"
	// PortDeviceRegistry is the required audio-device registry seam.
	PortDeviceRegistry = "device-registry"
	// PortAudioSource is the required PCM input seam.
	PortAudioSource = "audio-source"
	// PortAudioSink is the required PCM output seam.
	PortAudioSink = "audio-sink"
	// PortClock is the required time source after defaulting.
	PortClock = "clock"
	// PortSessionRuntimeObserver is the optional command-runtime evidence sink.
	PortSessionRuntimeObserver = "session-runtime-observer"
	// PortMetricSampler is the required, defaulted application metrics seam.
	PortMetricSampler = "metric-sampler"
	// PortLogger is the required, defaulted structured logging seam.
	PortLogger = "logger"

	// The *PortName constants make the port contract discoverable to callers
	// that prefer a name-oriented vocabulary.
	ToolExecutorPortName           = PortToolExecutor
	TransportDialerPortName        = PortTransportDialer
	InferencerPortName             = PortInferencer
	SessionInferencerPortName      = PortSessionInferencer
	DeviceRegistryPortName         = PortDeviceRegistry
	AudioSourcePortName            = PortAudioSource
	AudioSinkPortName              = PortAudioSink
	ClockPortName                  = PortClock
	SessionRuntimeObserverPortName = PortSessionRuntimeObserver
	MetricSamplerPortName          = PortMetricSampler
	LoggerPortName                 = PortLogger
)

var (
	// ErrMissingRequiredPort identifies a construction failure caused by a
	// missing required dependency.
	ErrMissingRequiredPort = errors.New("missing required composition port")
	// ErrUnknownPort identifies a swap for a name that is not live.
	ErrUnknownPort = errors.New("unknown composition port")
	// ErrInvalidPortSwap identifies a malformed or nil-required replacement.
	ErrInvalidPortSwap = errors.New("invalid composition port swap")
	// ErrDuplicatePortSwap identifies a request that names one live port more
	// than once. Rejecting duplicates keeps a multi-port request unambiguous.
	ErrDuplicatePortSwap = errors.New("duplicate composition port swap")
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

// inertTransportDialer satisfies the required production port without
// starting transport work during composition. Transport consumers can replace
// it through InitializeMockAgentCLIWithPorts until a runtime transport owner
// is threaded into the CLI graph.
type inertTransportDialer struct{}

func (inertTransportDialer) Dial(string, map[string]string) (transport.Conn, error) {
	return nil, errors.New("transport dialer is not configured")
}

func defaultTransportDialer() transport.Dialer { return inertTransportDialer{} }

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
	runtimeObserver      SessionRuntimeObserver
	metricSampler        MetricSampler
	logger               Logger
	rtcComponents        rtcontract.SessionRTCComponents
	rtcComponentsSet     bool
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

// WithSessionRuntimeObserver supplies the optional command-runtime evidence
// sink used by hermetic callers that need clock-stamped session observations.
func WithSessionRuntimeObserver(observer SessionRuntimeObserver) CompositionOption {
	return func(options *compositionOptions) error {
		options.runtimeObserver = observer
		return nil
	}
}

// WithMetricSampler supplies the application metrics seam to direct
// composition callers. Nil is normalized to the no-op implementation.
func WithMetricSampler(sampler MetricSampler) CompositionOption {
	return func(options *compositionOptions) error {
		options.metricSampler = observability.EnsureMetricSampler(sampler)
		return nil
	}
}

// WithLogger supplies the structured application logger to direct
// composition callers. Nil is normalized to the no-op implementation.
func WithLogger(logger Logger) CompositionOption {
	return func(options *compositionOptions) error {
		options.logger = observability.EnsureLogger(logger)
		return nil
	}
}

// WithSessionRTCComponents replaces only the external RTC component edges
// while retaining the production service-owned runtime factory and CLI graph.
// It is intended for hermetic command tests; omitted callers receive the
// concrete production composition.
func WithSessionRTCComponents(components rtcontract.SessionRTCComponents) CompositionOption {
	return func(options *compositionOptions) error {
		options.rtcComponents = components
		options.rtcComponentsSet = true
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

// ComposeAgentCLI constructs the singular CLI root from the required tool and
// transport and audio-side ports. An omitted clock is normalized to clock.Real.
// Optional inference capabilities are supplied through CompositionOption.
// Validation runs before any graph constructor is called.
func ComposeAgentCLI(
	toolExecutor messages.ToolExecutor,
	transportDialer transport.Dialer,
	deviceRegistry DeviceRegistry,
	audioSource AudioSource,
	audioSink AudioSink,
	clockSource Clock,
	options ...CompositionOption,
) (*cli.AgentCLI, error) {
	compositionOptions, err := applyCompositionOptions(options)
	if err != nil {
		return nil, err
	}

	values := compositionValues{
		toolExecutor:      toolExecutor,
		transportDialer:   transportDialer,
		deviceRegistry:    deviceRegistry,
		audioSource:       audioSource,
		audioSink:         audioSink,
		clockSource:       clockSource,
		runtimeObserver:   compositionOptions.runtimeObserver,
		metricSampler:     observability.EnsureMetricSampler(compositionOptions.metricSampler),
		logger:            observability.EnsureLogger(compositionOptions.logger),
		inferencer:        compositionOptions.inferencer,
		sessionInferencer: compositionOptions.sessionInferencer,
		rtcComponents:     effectiveSessionRTCComponents(compositionOptions),
	}
	normalizeClock(&values)
	if err := validateDependencies(&values); err != nil {
		return nil, err
	}

	toolDefaults, err := newToolDefaults()
	if err != nil {
		return nil, err
	}
	return assembleAgentCLI(
		values.toolExecutor,
		values.transportDialer,
		values.deviceRegistry,
		values.audioSource,
		values.audioSink,
		values.clockSource,
		values.runtimeObserver,
		values.metricSampler,
		values.logger,
		toolDefaults.definitions,
		values.inferencer,
		values.sessionInferencer,
		values.rtcComponents,
		compositionOptions.relaxModelValidation,
		nil,
	)
}

// InitializeAgentCLI builds the production CLI with the registry-backed tool
// executor. It shares the same live-port defaults and explicit assembly helper
// as all test paths.
func InitializeAgentCLI() (*cli.AgentCLI, error) {
	return initializeAgentCLIWithPorts(false, nil)
}

// InitializeMockAgentCLIWithPorts is the one uniform mock-injection entry
// point. Its cases are named replacements, validated against the same live
// port definitions used by defaults, discovery, and required-port validation.
func InitializeMockAgentCLIWithPorts(swaps ...PortSwap) (*cli.AgentCLI, error) {
	return initializeAgentCLIWithPorts(true, nil, swaps...)
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
	return initializeAgentCLIWithPorts(
		relaxModelValidation,
		nil,
		NewPortSwap(PortToolExecutor, toolExecutor),
		NewPortSwap(PortInferencer, inferencer),
		NewPortSwap(PortSessionInferencer, sessionInferencer),
	)
}

// assemblyObserver is an optional, package-local observation seam for the
// conformance suite. Production composition passes nil; the observer is
// carried into the generated assembly graph and sees the exact port values
// that cross the normal assembly boundary.
type assemblyObserver func(compositionValues)

// initializeAgentCLIWithPorts is the single assembly path for default and
// mock composition. Swap validation happens before the registry or any port
// default is constructed. Defaults are then created only for ports that were
// not explicitly replaced, which keeps a displaced real implementation from
// running its constructor alongside a supplied double. The package-local
// observer is nil for all production entry points.
func initializeAgentCLIWithPorts(relaxModelValidation bool, observer assemblyObserver, swaps ...PortSwap) (*cli.AgentCLI, error) {
	definitions := livePortDefinitions()
	if err := validatePortSwaps(definitions, swaps); err != nil {
		return nil, err
	}
	toolDefaults, err := newToolDefaults()
	if err != nil {
		return nil, err
	}
	values, err := compositionValuesWithPorts(definitions, toolDefaults, swaps)
	if err != nil {
		return nil, err
	}
	return assembleAgentCLI(
		values.toolExecutor,
		values.transportDialer,
		values.deviceRegistry,
		values.audioSource,
		values.audioSink,
		values.clockSource,
		values.runtimeObserver,
		values.metricSampler,
		values.logger,
		toolDefaults.definitions,
		values.inferencer,
		values.sessionInferencer,
		values.rtcComponents,
		relaxModelValidation,
		withDefaultCallCounts(observer, values.defaultCalls),
	)
}

func withDefaultCallCounts(observer assemblyObserver, defaultCalls map[string]int) assemblyObserver {
	if observer == nil {
		return nil
	}
	return func(values compositionValues) {
		values.defaultCalls = defaultCalls
		observer(values)
	}
}

type toolDefaults struct {
	executor    messages.ToolExecutor
	definitions []messages.ToolDefinition
}

// newToolDefaults resolves the reusable runtime's built-in surface at the CLI
// composition edge. The CLI owns the process working directory resolution;
// the reusable service never infers host paths from ambient process state.
func newToolDefaults() (toolDefaults, error) {
	workdir, err := hostServices.ResolveCLIWorkDir(flags.NewGlobalFlags())
	if err != nil {
		return toolDefaults{}, fmt.Errorf("resolve tool working directory: %w", err)
	}
	capability, err := runtimeToolsWire.NewService().Resolve(context.Background(), runtimeTools.Request{
		WorkDir:        workdir,
		UseDefaultTool: true,
	})
	if err != nil {
		return toolDefaults{}, fmt.Errorf("resolve default tools: %w", err)
	}
	return toolDefaults{executor: capability.Executor, definitions: capability.Definitions}, nil
}

func compositionValuesWithPorts(definitions []portDefinition, defaults toolDefaults, swaps []PortSwap) (compositionValues, error) {
	if err := validatePortSwaps(definitions, swaps); err != nil {
		return compositionValues{}, err
	}

	values := compositionValues{
		defaultCalls:  make(map[string]int, len(definitions)),
		rtcComponents: defaultSessionRTCComponents(),
	}
	swapped := make(map[string]struct{}, len(swaps))
	for _, swap := range swaps {
		swapped[swap.Name] = struct{}{}
	}
	for _, definition := range definitions {
		if _, replaced := swapped[definition.descriptor.Name]; replaced || definition.defaultValue == nil {
			continue
		}
		values.defaultCalls[definition.descriptor.Name]++
		definition.assign(&values, definition.defaultValue(defaults))
	}
	for _, swap := range swaps {
		definition, _ := findPortDefinitionIn(definitions, swap.Name)
		definition.assign(&values, swap.Value)
	}

	if err := validateDependenciesWithDefinitions(&values, definitions); err != nil {
		return compositionValues{}, err
	}
	return values, nil
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
	transportDialer   transport.Dialer
	deviceRegistry    DeviceRegistry
	audioSource       AudioSource
	audioSink         AudioSink
	clockSource       Clock
	runtimeObserver   SessionRuntimeObserver
	metricSampler     MetricSampler
	logger            Logger
	inferencer        messages.Inferencer
	sessionInferencer messages.SessionInferencer
	rtcComponents     rtcontract.SessionRTCComponents
	defaultCalls      map[string]int
}

func effectiveSessionRTCComponents(options compositionOptions) rtcontract.SessionRTCComponents {
	if options.rtcComponentsSet {
		return options.rtcComponents
	}
	return defaultSessionRTCComponents()
}

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
	default:
		return false
	}
}
