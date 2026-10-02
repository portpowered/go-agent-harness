package wire

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/observability"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// TestComposedProviderServiceLogsRealtimeDiagnostics proves the host logger
// composed into the provider service receives realtime provider diagnostics.
func TestComposedProviderServiceLogsRealtimeDiagnostics(t *testing.T) {
	var (
		mu      sync.Mutex
		records []observability.LogRecord
	)
	logger := observability.LoggerFunc(func(_ context.Context, record observability.LogRecord) error {
		mu.Lock()
		defer mu.Unlock()
		records = append(records, record)
		return nil
	})
	service, err := provideProviderService(clock.Real{}, nil, nil, nil, provideProviderLogger(logger))
	if err != nil {
		t.Fatal(err)
	}
	inferencer, err := service.BuildSession(t.Context(), providers.SessionConfig{
		Provider: "openai", Model: providers.OpenAIRealtimeLegacyModel, APIKey: "test-key", WebSocketDialer: failingConnDialer{},
	})
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	if session, err := inferencer.ConnectSession(t.Context()); err == nil {
		if closeErr := session.Close(); closeErr != nil {
			t.Logf("close session: %v", closeErr)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, record := range records {
		if record.Level == "info" && record.Message == "openai realtime: websocket connected" && record.Fields["endpoint"] != "" {
			return
		}
	}
	t.Fatalf("host log records = %+v, want provider websocket diagnostic", records)
}

type failingConnDialer struct{}

func (failingConnDialer) Dial(string, map[string]string) (transport.Conn, error) {
	return failingConn{}, nil
}

type failingConn struct{}

func (failingConn) ReadMessage() (int, []byte, error) { return 0, nil, errors.New("closed") }
func (failingConn) WriteMessage(int, []byte) error    { return errors.New("closed") }
func (failingConn) Close() error                      { return nil }

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
	ctx context.Context, toolExecutor messages.ToolExecutor,
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
		toolService:       compositionOptions.toolService,
	}
	normalizeClock(&values)
	if err := validateDependencies(&values); err != nil {
		return nil, err
	}

	toolDefaults, err := newToolDefaults(ctx)
	if err != nil {
		return nil, err
	}
	return assembleAgentCLI(ctx,
		markToolExecutorReplacement(values.toolExecutor),
		values.transportDialer,
		values.deviceRegistry,
		values.audioSource,
		values.audioSink,
		values.clockSource,
		values.runtimeObserver,
		values.metricSampler,
		values.logger,
		toolDefaults.definitions,
		toolServiceOverride{service: values.toolService},
		values.inferencer,
		values.sessionInferencer,
		compositionOptions.relaxModelValidation,
		nil,
	)
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
