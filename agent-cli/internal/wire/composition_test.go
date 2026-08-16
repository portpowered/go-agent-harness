package wire

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/platform/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

type recordingToolExecutor struct {
	calls int
}

func (e *recordingToolExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.calls++
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: "tool result"}, nil
}

type recordingDeviceRegistry struct {
	lookups int
}

func (r *recordingDeviceRegistry) ListDevices() []string {
	r.lookups++
	return []string{"recording-device"}
}

type recordingAudioSource struct {
	reads int
}

func (s *recordingAudioSource) ReadFrame(context.Context, []int16) error {
	s.reads++
	return io.EOF
}

func (s *recordingAudioSource) Close() error { return nil }

type recordingAudioSink struct {
	writes int
}

func (s *recordingAudioSink) WriteFrame(context.Context, []int16) error {
	s.writes++
	return nil
}

func (s *recordingAudioSink) Close() error { return nil }

type recordingClock struct {
	now time.Time
}

func (c *recordingClock) Now() time.Time { return c.now }

type recordingInferencer struct {
	calls    int
	response string
	results  []messages.InferenceResult
}

func (e *recordingInferencer) Infer(context.Context, messages.InferenceRequest) (messages.InferenceResult, error) {
	e.calls++
	if len(e.results) > 0 {
		result := e.results[0]
		e.results = e.results[1:]
		return result, nil
	}
	return messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, e.response)}, nil
}

func (e *recordingInferencer) InferStream(ctx context.Context, request messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	result, err := e.Infer(ctx, request)
	if err != nil {
		return nil, err
	}
	stream := make(chan messages.StreamMessage, 5+2*len(result.ToolCalls))
	stream <- messages.StreamMessage{Type: messages.StreamTypeTextStart, ActorProvidedIndex: 0, Value: messages.NewTextStartValue()}
	if result.Message.HasText() {
		stream <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, ActorProvidedIndex: 0, Value: messages.NewTextDeltaValue(result.Message.TextContent())}
	}
	stream <- messages.StreamMessage{Type: messages.StreamTypeTextEnd, ActorProvidedIndex: 0, Value: messages.NewTextEndValue()}
	for _, toolCall := range result.ToolCalls {
		stream <- messages.StreamMessage{Type: messages.StreamTypeToolCallStart, ActorProvidedIndex: 0, Value: messages.NewToolCallStartValue(toolCall.ID, toolCall.Name)}
		stream <- messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, ActorProvidedIndex: 0, Value: messages.NewToolCallEndValue(toolCall.ID, toolCall.Name, toolCall.Arguments)}
	}
	stream <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd, ActorProvidedIndex: 0, Value: messages.NewMessageEndValue(result.TokenUsage)}
	close(stream)
	return stream, nil
}

type recordingSessionInferencer struct {
	connects int
}

func (e *recordingSessionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	e.connects++
	done := make(chan struct{})
	close(done)
	return &recordingSession{receive: messages.NewTypedBuffer[messages.StreamMessage](4), done: done}, nil
}

type recordingSession struct {
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    <-chan struct{}
}

func (s *recordingSession) Send(context.Context, messages.StreamMessage) bool { return true }

func (s *recordingSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.receive }

func (s *recordingSession) Done() <-chan struct{} { return s.done }

func (s *recordingSession) Close() error { return nil }

type recordingDialer struct {
	dials atomic.Int64
}

var _ transport.Dialer = (*recordingDialer)(nil)

func (d *recordingDialer) Dial(string, map[string]string) (transport.Conn, error) {
	d.dials.Add(1)
	return nil, errors.New("composition test transport dial")
}

func (d *recordingDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	d.dials.Add(1)
	return nil, errors.New("composition test network dial")
}

func validCompositionValues() compositionValues {
	return compositionValues{
		toolExecutor:    &recordingToolExecutor{},
		transportDialer: &recordingDialer{},
		deviceRegistry:  &recordingDeviceRegistry{},
		audioSource:     &recordingAudioSource{},
		audioSink:       &recordingAudioSink{},
		clockSource:     &recordingClock{now: time.Unix(123, 0)},
	}
}

func composeTestAgentCLI(toolExecutor messages.ToolExecutor, options ...CompositionOption) (*cli.AgentCLI, error) {
	return ComposeAgentCLI(
		toolExecutor,
		&recordingDialer{},
		&recordingDeviceRegistry{},
		&recordingAudioSource{},
		&recordingAudioSink{},
		&recordingClock{now: time.Unix(123, 0)},
		options...,
	)
}

func composeTestAgentCLIWithDialer(toolExecutor messages.ToolExecutor, dialer transport.Dialer, options ...CompositionOption) (*cli.AgentCLI, error) {
	return ComposeAgentCLI(
		toolExecutor,
		dialer,
		&recordingDeviceRegistry{},
		&recordingAudioSource{},
		&recordingAudioSink{},
		&recordingClock{now: time.Unix(123, 0)},
		options...,
	)
}

func TestComposeAgentCLI_ValidDependenciesReturnRoot(t *testing.T) {
	root, err := composeTestAgentCLI(&recordingToolExecutor{})
	if err != nil {
		t.Fatalf("ComposeAgentCLI returned error: %v", err)
	}
	if root == nil {
		t.Fatal("ComposeAgentCLI returned a nil root")
	}
	if root.Generate() == nil {
		t.Fatal("ComposeAgentCLI generated a nil cobra root")
	}
}

func TestCompositionClock_DefaultsThroughEnsureAndPreservesSuppliedIdentity(t *testing.T) {
	values := validCompositionValues()
	values.clockSource = nil
	normalizeClock(&values)

	defaultClock, ok := values.clockSource.(clock.Real)
	if !ok {
		t.Fatalf("omitted clock = %T, want clock.Real", values.clockSource)
	}
	if defaultClock.Now().IsZero() {
		t.Fatal("default clock returned a zero timestamp")
	}

	supplied := &recordingClock{now: time.Unix(789, 0)}
	values.clockSource = supplied
	normalizeClock(&values)
	if values.clockSource != supplied {
		t.Fatalf("supplied clock identity changed: got %p want %p", values.clockSource, supplied)
	}

	root, err := ComposeAgentCLI(
		&recordingToolExecutor{},
		&recordingDialer{},
		&recordingDeviceRegistry{},
		&recordingAudioSource{},
		&recordingAudioSink{},
		nil,
	)
	if err != nil || root == nil {
		t.Fatalf("ComposeAgentCLI with omitted clock: root=%v err=%v", root, err)
	}
}

func TestValidateDependencies_RejectsEveryRequiredLivePortByName(t *testing.T) {
	requiredCount := 0
	for _, definition := range livePortDefinitions() {
		if !definition.descriptor.Required {
			continue
		}
		requiredCount++

		values := validCompositionValues()
		definition.assign(&values, nil)
		err := validateDependencies(&values)
		if err == nil {
			t.Fatalf("validateDependencies accepted nil %q", definition.descriptor.Name)
		}

		var missing *MissingPortError
		if !errors.As(err, &missing) {
			t.Fatalf("error for %q is not MissingPortError: %T (%v)", definition.descriptor.Name, err, err)
		}
		if missing.Name != definition.descriptor.Name {
			t.Fatalf("missing error name = %q, want %q", missing.Name, definition.descriptor.Name)
		}
		if !errors.Is(err, ErrMissingRequiredPort) {
			t.Fatalf("error for %q does not preserve ErrMissingRequiredPort: %v", definition.descriptor.Name, err)
		}
		if !strings.Contains(err.Error(), definition.descriptor.Name) {
			t.Fatalf("error %q does not name the missing port: %v", definition.descriptor.Name, err)
		}
	}
	if requiredCount == 0 {
		t.Fatal("live port list has no required ports")
	}

	var typedNil *recordingToolExecutor
	values := validCompositionValues()
	values.toolExecutor = typedNil
	err := validateDependencies(&values)
	if err == nil || !errors.Is(err, ErrMissingRequiredPort) {
		t.Fatalf("typed nil required port was accepted: %v", err)
	}

	var typedNilSource *recordingAudioSource
	values = validCompositionValues()
	values.audioSource = typedNilSource
	err = validateDependencies(&values)
	if err == nil || !errors.Is(err, ErrMissingRequiredPort) || !strings.Contains(err.Error(), PortAudioSource) {
		t.Fatalf("typed nil audio source was accepted or unnamed: %v", err)
	}

	var typedNilDialer *recordingDialer
	values = validCompositionValues()
	values.transportDialer = typedNilDialer
	err = validateDependencies(&values)
	if err == nil || !errors.Is(err, ErrMissingRequiredPort) || !strings.Contains(err.Error(), PortTransportDialer) {
		t.Fatalf("typed nil transport dialer was accepted or unnamed: %v", err)
	}
}

func TestLivePorts_ReturnsStableIndependentDescriptors(t *testing.T) {
	first := LivePorts()
	second := LivePorts()
	if len(first) != len(second) || len(first) != len(livePortDefinitions()) {
		t.Fatalf("live port list lengths differ: %d, %d", len(first), len(second))
	}
	if len(first) == 0 {
		t.Fatal("live port list is empty")
	}
	first[0].Name = "mutated"
	if second[0].Name == "mutated" {
		t.Fatal("LivePorts returned shared mutable descriptor storage")
	}
	for index, definition := range livePortDefinitions() {
		if second[index].Name != definition.descriptor.Name || second[index].Required != definition.descriptor.Required || second[index].Type != definition.descriptor.Type {
			t.Fatalf("descriptor %d = %#v, want %#v", index, second[index], definition.descriptor)
		}
	}
}

func TestInitializeMockAgentCLIWithPorts_SwapsEveryLivePort(t *testing.T) {
	for _, definition := range livePortDefinitions() {
		t.Run(definition.descriptor.Name, func(t *testing.T) {
			replacement := replacementForPortType(t, definition.descriptor.Type)
			swaps := []PortSwap{{Name: definition.descriptor.Name, Value: replacement}}
			var fixtureInferencer *recordingInferencer
			if definition.descriptor.Type == reflect.TypeOf((*messages.ToolExecutor)(nil)).Elem() {
				fixtureInferencer = toolCallingInferencer()
				swaps = append([]PortSwap{{Name: PortInferencer, Value: fixtureInferencer}}, swaps...)
			}

			root, err := InitializeMockAgentCLIWithPorts(swaps...)
			if err != nil {
				t.Fatalf("InitializeMockAgentCLIWithPorts(%q): %v", definition.descriptor.Name, err)
			}
			if root == nil {
				t.Fatalf("InitializeMockAgentCLIWithPorts(%q) returned nil root", definition.descriptor.Name)
			}

			switch definition.descriptor.Type {
			case reflect.TypeOf((*messages.ToolExecutor)(nil)).Elem():
				if err := executeAskCommand(t, root); err != nil {
					t.Fatalf("root ask for %q: %v", definition.descriptor.Name, err)
				}
				if got := replacement.(*recordingToolExecutor).calls; got != 1 {
					t.Fatalf("selected %q replacement calls = %d, want exactly 1", definition.descriptor.Name, got)
				}
				if fixtureInferencer.calls != 2 {
					t.Fatalf("fixture inferencer calls = %d, want the tool turn and final turn", fixtureInferencer.calls)
				}
			case reflect.TypeOf((*messages.Inferencer)(nil)).Elem():
				if err := executeAskCommand(t, root); err != nil {
					t.Fatalf("root ask for %q: %v", definition.descriptor.Name, err)
				}
				if got := replacement.(*recordingInferencer).calls; got != 1 {
					t.Fatalf("selected %q replacement calls = %d, want exactly 1", definition.descriptor.Name, got)
				}
			case reflect.TypeOf((*messages.SessionInferencer)(nil)).Elem():
				if err := executeSessionCommand(t, root, true); err != nil {
					t.Fatalf("root session for %q: %v", definition.descriptor.Name, err)
				}
				if got := replacement.(*recordingSessionInferencer).connects; got != 1 {
					t.Fatalf("selected %q replacement connects = %d, want exactly 1", definition.descriptor.Name, got)
				}
			case reflect.TypeOf((*DeviceRegistry)(nil)).Elem(),
				reflect.TypeOf((*AudioSource)(nil)).Elem(),
				reflect.TypeOf((*AudioSink)(nil)).Elem(),
				reflect.TypeOf((*Clock)(nil)).Elem(),
				reflect.TypeOf((*transport.Dialer)(nil)).Elem():
				values := validCompositionValues()
				if err := applyPortSwap(&values, swaps[0]); err != nil {
					t.Fatalf("applyPortSwap(%q): %v", definition.descriptor.Name, err)
				}
				if got := definition.value(&values); got != replacement {
					t.Fatalf("selected %q replacement identity was not retained", definition.descriptor.Name)
				}
				if definition.descriptor.Name == PortClock {
					if _, ok := definition.value(&values).(Clock); !ok {
						t.Fatalf("selected %q replacement lost its clock contract", definition.descriptor.Name)
					}
				}
				if definition.descriptor.Name == PortTransportDialer {
					if got := replacement.(*recordingDialer).dials.Load(); got != 0 {
						t.Fatalf("selected %q replacement was dialed during construction: %d", definition.descriptor.Name, got)
					}
					_, err := InitializeMockAgentCLIWithPorts(NewPortSwap(PortTransportDialer, struct{}{}))
					assertPortSwapError(t, err, ErrIncompatiblePort, PortTransportDialer)
				}
			default:
				t.Fatalf("no root-level observation for live port type %v", definition.descriptor.Type)
			}
		})
	}
}

func TestCompositionValuesWithPorts_SkipsDisplacedDefaultConstructors(t *testing.T) {
	for _, selected := range livePortDefinitions() {
		selected := selected
		t.Run(selected.descriptor.Name, func(t *testing.T) {
			definitions := livePortDefinitions()
			defaultCalls := make(map[string]int, len(definitions))
			for index := range definitions {
				name := definitions[index].descriptor.Name
				factory := definitions[index].defaultValue
				definitions[index].defaultValue = func(registry *tools.ToolRegistry) any {
					defaultCalls[name]++
					return factory(registry)
				}
			}

			replacement := replacementForPortType(t, selected.descriptor.Type)
			values, err := compositionValuesWithPorts(
				definitions,
				nil,
				[]PortSwap{NewPortSwap(selected.descriptor.Name, replacement)},
			)
			if err != nil {
				t.Fatalf("compositionValuesWithPorts: %v", err)
			}
			definition, ok := findPortDefinitionIn(definitions, selected.descriptor.Name)
			if !ok {
				t.Fatalf("selected port %q disappeared from live definitions", selected.descriptor.Name)
			}
			if got := definition.value(&values); got != replacement {
				t.Fatalf("selected %q replacement identity changed: got %T/%p want %T/%p", selected.descriptor.Name, got, got, replacement, replacement)
			}

			for _, definition := range definitions {
				calls := defaultCalls[definition.descriptor.Name]
				if definition.descriptor.Name == selected.descriptor.Name {
					if calls != 0 {
						t.Fatalf("displaced %q default constructor calls = %d, want exactly 0", definition.descriptor.Name, calls)
					}
					continue
				}
				if calls != 1 {
					t.Fatalf("unswapped %q default constructor calls = %d, want exactly 1", definition.descriptor.Name, calls)
				}
			}
		})
	}
}

func toolCallingInferencer() *recordingInferencer {
	return &recordingInferencer{
		results: []messages.InferenceResult{
			{
				Message:   messages.NewTextMessage(messages.RoleAssistant, "use tool"),
				ToolCalls: []messages.ToolCall{{ID: "composition-swap", Name: "sleep", Arguments: `{"duration":"0s"}`}},
			},
			{Message: messages.NewTextMessage(messages.RoleAssistant, "tool complete")},
		},
	}
}

func replacementForPortType(t *testing.T, portType reflect.Type) any {
	t.Helper()
	switch {
	case portType == reflect.TypeOf((*messages.ToolExecutor)(nil)).Elem():
		return &recordingToolExecutor{}
	case portType == reflect.TypeOf((*messages.Inferencer)(nil)).Elem():
		return &recordingInferencer{response: "swapped"}
	case portType == reflect.TypeOf((*messages.SessionInferencer)(nil)).Elem():
		return &recordingSessionInferencer{}
	case portType == reflect.TypeOf((*DeviceRegistry)(nil)).Elem():
		return &recordingDeviceRegistry{}
	case portType == reflect.TypeOf((*AudioSource)(nil)).Elem():
		return &recordingAudioSource{}
	case portType == reflect.TypeOf((*AudioSink)(nil)).Elem():
		return &recordingAudioSink{}
	case portType == reflect.TypeOf((*Clock)(nil)).Elem():
		return &recordingClock{now: time.Unix(456, 0)}
	case portType == reflect.TypeOf((*transport.Dialer)(nil)).Elem():
		return &recordingDialer{}
	default:
		t.Fatalf("no recording replacement for live port type %v", portType)
		return nil
	}
}

func TestPortSwaps_RejectUnknownIncompatibleAndRequiredNil(t *testing.T) {
	unknownRoot, unknownErr := InitializeMockAgentCLIWithPorts(PortSwap{Name: "unknown-port", Value: &recordingToolExecutor{}})
	if unknownRoot != nil {
		t.Fatal("unknown swap unexpectedly returned a root")
	}
	assertPortSwapError(t, unknownErr, ErrUnknownPort, "unknown-port")

	incompatibleRoot, incompatibleErr := InitializeMockAgentCLIWithPorts(NewPortSwap(PortInferencer, struct{}{}))
	if incompatibleRoot != nil {
		t.Fatal("incompatible swap unexpectedly returned a root")
	}
	assertPortSwapError(t, incompatibleErr, ErrIncompatiblePort, PortInferencer)

	for _, definition := range livePortDefinitions() {
		definition := definition
		if definition.descriptor.Required {
			t.Run(definition.descriptor.Name+"/required-nil", func(t *testing.T) {
				_, err := InitializeMockAgentCLIWithPorts(NewPortSwap(definition.descriptor.Name, nil))
				assertPortSwapError(t, err, ErrInvalidPortSwap, definition.descriptor.Name)
			})
		}
		t.Run(definition.descriptor.Name+"/incompatible", func(t *testing.T) {
			_, err := InitializeMockAgentCLIWithPorts(NewPortSwap(definition.descriptor.Name, struct{}{}))
			assertPortSwapError(t, err, ErrIncompatiblePort, definition.descriptor.Name)
		})
		if !definition.descriptor.Required {
			t.Run(definition.descriptor.Name+"/optional-nil", func(t *testing.T) {
				root, err := InitializeMockAgentCLIWithPorts(NewPortSwap(definition.descriptor.Name, nil))
				if err != nil || root == nil {
					t.Fatalf("optional nil swap returned root=%v err=%v", root, err)
				}
			})
		}
	}
}

func assertPortSwapError(t *testing.T, err error, sentinel error, name string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s error for %q", sentinel, name)
	}
	var swapErr *PortSwapError
	if !errors.As(err, &swapErr) {
		t.Fatalf("%T is not PortSwapError: %v", err, err)
	}
	if swapErr.Name != name || !strings.Contains(err.Error(), name) {
		t.Fatalf("swap error name = %q / message %q, want %q", swapErr.Name, err.Error(), name)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("swap error %v does not preserve %v", err, sentinel)
	}
	if sentinel == ErrIncompatiblePort && (swapErr.Expected == nil || swapErr.Actual == nil) {
		t.Fatalf("incompatible swap %q omitted expected/actual type details: %#v", name, swapErr)
	}
}

func TestCompositionOptions_InstallOptionalCapabilities(t *testing.T) {
	strict, err := applyCompositionOptions([]CompositionOption{WithRelaxedModelValidation(), WithStrictModelValidation()})
	if err != nil || strict.relaxModelValidation {
		t.Fatalf("strict option did not override relaxed option: %#v, %v", strict, err)
	}
	if _, err := composeTestAgentCLI(&recordingToolExecutor{}, nil); err == nil || !strings.Contains(err.Error(), "option 0") {
		t.Fatalf("nil composition option was not rejected: %v", err)
	}

	for _, definition := range livePortDefinitions() {
		if definition.descriptor.Required {
			continue
		}
		t.Run(definition.descriptor.Name, func(t *testing.T) {
			switch definition.descriptor.Type {
			case reflect.TypeOf((*messages.Inferencer)(nil)).Elem():
				t.Run("unavailable_without_option", func(t *testing.T) {
					root, err := composeTestAgentCLI(&recordingToolExecutor{})
					if err != nil || root == nil {
						t.Fatalf("ComposeAgentCLI without %q: root=%v err=%v", definition.descriptor.Name, root, err)
					}
					err = executeAskCommand(t, root)
					if err == nil || !strings.Contains(err.Error(), "API key") {
						t.Fatalf("ask without %q did not report the unavailable capability: %v", definition.descriptor.Name, err)
					}
				})
				t.Run("available_with_option", func(t *testing.T) {
					inferencer := &recordingInferencer{response: "option"}
					root, err := composeTestAgentCLI(&recordingToolExecutor{}, WithInferencer(inferencer))
					if err != nil || root == nil {
						t.Fatalf("ComposeAgentCLI with %q: root=%v err=%v", definition.descriptor.Name, root, err)
					}
					if err := executeAskCommand(t, root); err != nil {
						t.Fatalf("ask with %q: %v", definition.descriptor.Name, err)
					}
					if inferencer.calls != 1 {
						t.Fatalf("supplied %q calls = %d, want exactly 1", definition.descriptor.Name, inferencer.calls)
					}
				})
			case reflect.TypeOf((*messages.SessionInferencer)(nil)).Elem():
				t.Run("unavailable_without_option", func(t *testing.T) {
					root, err := composeTestAgentCLI(&recordingToolExecutor{})
					if err != nil || root == nil {
						t.Fatalf("ComposeAgentCLI without %q: root=%v err=%v", definition.descriptor.Name, root, err)
					}
					err = executeSessionCommand(t, root, false)
					if err == nil || !strings.Contains(err.Error(), "requires --provider grok") {
						t.Fatalf("session without %q did not report the unavailable capability: %v", definition.descriptor.Name, err)
					}
				})
				t.Run("available_with_option", func(t *testing.T) {
					sessionInferencer := &recordingSessionInferencer{}
					root, err := composeTestAgentCLI(&recordingToolExecutor{}, WithSessionInferencer(sessionInferencer))
					if err != nil || root == nil {
						t.Fatalf("ComposeAgentCLI with %q: root=%v err=%v", definition.descriptor.Name, root, err)
					}
					if err := executeSessionCommand(t, root, true); err != nil {
						t.Fatalf("session with %q: %v", definition.descriptor.Name, err)
					}
					if sessionInferencer.connects != 1 {
						t.Fatalf("supplied %q connects = %d, want exactly 1", definition.descriptor.Name, sessionInferencer.connects)
					}
				})
			default:
				t.Fatalf("no runtime optional-capability observation for %v", definition.descriptor.Type)
			}
		})
	}
}

func TestComposeAgentCLI_OptionalInferencerIsObservedAtRuntime(t *testing.T) {
	inferencer := &recordingInferencer{response: "exact replacement"}
	root, err := composeTestAgentCLI(&recordingToolExecutor{}, WithInferencer(inferencer))
	if err != nil {
		t.Fatalf("ComposeAgentCLI: %v", err)
	}

	command := root.Generate()
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"--config-dir", t.TempDir(), "ask", "--no-system-information", "hello"})
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute ask: %v", err)
	}
	if inferencer.calls != 1 {
		t.Fatalf("injected inferencer calls = %d, want exactly 1", inferencer.calls)
	}
}

func executeAskCommand(t *testing.T, root *cli.AgentCLI) error {
	t.Helper()
	command := root.Generate()
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"--config-dir", t.TempDir(), "ask", "--no-system-information", "hello"})
	return command.ExecuteContext(context.Background())
}

func executeSessionCommand(t *testing.T, root *cli.AgentCLI, provider bool) error {
	t.Helper()
	configDir := t.TempDir()
	args := []string{
		"--config-dir", configDir,
		"session", "--record", "capture.json",
	}
	if provider {
		args = append(args, "--provider", "grok", "--model", "test-model", "--api-key", "test-key")
	}
	command := root.Generate()
	command.SetOut(&bytes.Buffer{})
	command.SetErr(io.Discard)
	command.SetArgs(args)
	return command.ExecuteContext(context.Background())
}

func TestCompositionConstruction_IsInert(t *testing.T) {
	toolExecutor := &recordingToolExecutor{}
	inferencer := &recordingInferencer{response: "unused"}
	sessionInferencer := &recordingSessionInferencer{}
	fileSentinel := t.TempDir()
	// Route every platform temp-dir lookup through the sentinel. The removed
	// construction path used os.MkdirTemp("", ...), so this makes the test
	// fail if that filesystem side effect is reintroduced.
	t.Setenv("TMPDIR", fileSentinel)
	t.Setenv("TMP", fileSentinel)
	t.Setenv("TEMP", fileSentinel)
	beforeFiles := directoryEntries(t, fileSentinel)
	dialer := &recordingDialer{}
	// Composition has no file or network side effects. The sentinel catches the
	// old construction-time config-file path, while the default transport hook
	// catches an accidental HTTP client dial without making a real connection.
	previousTransport := http.DefaultTransport
	http.DefaultTransport = &http.Transport{DialContext: dialer.DialContext}
	defer func() {
		http.DefaultTransport = previousTransport
	}()
	before := runtime.NumGoroutine()

	root, err := composeTestAgentCLIWithDialer(
		toolExecutor,
		dialer,
		WithInferencer(inferencer),
		WithSessionInferencer(sessionInferencer),
	)
	if err != nil || root == nil {
		t.Fatalf("inert construction failed: root=%v err=%v", root, err)
	}

	// Allow unrelated runtime work to settle. A tolerance of two goroutines
	// covers test/runtime noise; construction itself starts none.
	time.Sleep(20 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > before+2 {
		t.Fatalf("construction left unexpected goroutines: before=%d after=%d", before, after)
	}
	if toolExecutor.calls != 0 || inferencer.calls != 0 || sessionInferencer.connects != 0 {
		t.Fatalf("construction called a dependency: tool=%d inferencer=%d session=%d", toolExecutor.calls, inferencer.calls, sessionInferencer.connects)
	}
	if got := directoryEntries(t, fileSentinel); !reflect.DeepEqual(got, beforeFiles) {
		t.Fatalf("construction changed the sentinel filesystem: before=%v after=%v", beforeFiles, got)
	}
	if got := dialer.dials.Load(); got != 0 {
		t.Fatalf("construction performed %d recorded network dials", got)
	}
}

func directoryEntries(t *testing.T, directory string) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read sentinel directory: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

func TestLegacyInitializersForwardToExplicitComposition(t *testing.T) {
	tests := []struct {
		name string
		init func(*recordingToolExecutor, *recordingInferencer, *recordingSessionInferencer) (*cli.AgentCLI, error)
	}{
		{
			name: "mock",
			init: func(tool *recordingToolExecutor, inferencer *recordingInferencer, _ *recordingSessionInferencer) (*cli.AgentCLI, error) {
				return InitializeMockAgentCLI(tool, inferencer)
			},
		},
		{
			name: "mock-session",
			init: func(tool *recordingToolExecutor, inferencer *recordingInferencer, session *recordingSessionInferencer) (*cli.AgentCLI, error) {
				return InitializeMockAgentCLIWithSessionInferencer(tool, inferencer, session)
			},
		},
		{
			name: "strict-override",
			init: func(tool *recordingToolExecutor, inferencer *recordingInferencer, _ *recordingSessionInferencer) (*cli.AgentCLI, error) {
				return InitializeAgentCLIWithInferencerOverride(tool, inferencer)
			},
		},
		{
			name: "production",
			init: func(_ *recordingToolExecutor, _ *recordingInferencer, _ *recordingSessionInferencer) (*cli.AgentCLI, error) {
				return InitializeAgentCLI()
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, err := test.init(&recordingToolExecutor{}, &recordingInferencer{}, &recordingSessionInferencer{})
			if err != nil {
				t.Fatalf("initializer: %v", err)
			}
			if root == nil {
				t.Fatal("initializer returned nil root")
			}
		})
	}
}
