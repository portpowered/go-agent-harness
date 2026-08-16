package wire

import (
	"context"
	"errors"
	"io"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type recordingToolExecutor struct {
	calls int
}

func (e *recordingToolExecutor) Execute(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
	e.calls++
	return messages.ToolCallResponse{}, nil
}

type recordingInferencer struct {
	calls    int
	response string
}

func (e *recordingInferencer) Infer(context.Context, messages.InferenceRequest) (messages.InferenceResult, error) {
	e.calls++
	return messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, e.response)}, nil
}

func (e *recordingInferencer) InferStream(ctx context.Context, request messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	result, err := e.Infer(ctx, request)
	if err != nil {
		return nil, err
	}
	stream := make(chan messages.StreamMessage, 4)
	stream <- messages.StreamMessage{Type: messages.StreamTypeTextStart, ActorProvidedIndex: 0, Value: messages.NewTextStartValue()}
	stream <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, ActorProvidedIndex: 0, Value: messages.NewTextDeltaValue(result.Message.TextContent())}
	stream <- messages.StreamMessage{Type: messages.StreamTypeTextEnd, ActorProvidedIndex: 0, Value: messages.NewTextEndValue()}
	stream <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd, ActorProvidedIndex: 0, Value: messages.NewMessageEndValue(result.TokenUsage)}
	close(stream)
	return stream, nil
}

type recordingSessionInferencer struct {
	connects int
}

func (e *recordingSessionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	e.connects++
	return nil, errors.New("recording session should not be connected during construction")
}

func TestComposeAgentCLI_ValidDependenciesReturnRoot(t *testing.T) {
	root, err := ComposeAgentCLI(&recordingToolExecutor{})
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

func TestValidateDependencies_RejectsEveryRequiredLivePortByName(t *testing.T) {
	requiredCount := 0
	for _, definition := range livePortDefinitions() {
		if !definition.descriptor.Required {
			continue
		}
		requiredCount++

		values := compositionValues{toolExecutor: &recordingToolExecutor{}}
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
	err := validateDependencies(&compositionValues{toolExecutor: typedNil})
	if err == nil || !errors.Is(err, ErrMissingRequiredPort) {
		t.Fatalf("typed nil required port was accepted: %v", err)
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
		replacement := replacementForPortType(t, definition.descriptor.Type)
		values := compositionValues{toolExecutor: &recordingToolExecutor{}}
		if err := applyPortSwap(&values, PortSwap{Name: definition.descriptor.Name, Value: replacement}); err != nil {
			t.Fatalf("applyPortSwap(%q): %v", definition.descriptor.Name, err)
		}
		if got := definition.value(&values); got != replacement {
			t.Fatalf("swap for %q installed %T, want exact replacement %T", definition.descriptor.Name, got, replacement)
		}

		root, err := InitializeMockAgentCLIWithPorts(PortSwap{Name: definition.descriptor.Name, Value: replacement})
		if err != nil {
			t.Fatalf("InitializeMockAgentCLIWithPorts(%q): %v", definition.descriptor.Name, err)
		}
		if root == nil {
			t.Fatalf("InitializeMockAgentCLIWithPorts(%q) returned nil root", definition.descriptor.Name)
		}
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
	default:
		t.Fatalf("no recording replacement for live port type %v", portType)
		return nil
	}
}

func TestPortSwaps_RejectUnknownIncompatibleAndRequiredNil(t *testing.T) {
	unknown := applyPortSwap(&compositionValues{}, PortSwap{Name: "scratch-port", Value: &recordingToolExecutor{}})
	assertPortSwapError(t, unknown, ErrUnknownPort, "scratch-port")

	incompatible := applyPortSwap(&compositionValues{}, PortSwap{Name: PortInferencer, Value: struct{}{}})
	assertPortSwapError(t, incompatible, ErrIncompatiblePort, PortInferencer)

	requiredNil := applyPortSwap(&compositionValues{toolExecutor: &recordingToolExecutor{}}, PortSwap{Name: PortToolExecutor, Value: nil})
	assertPortSwapError(t, requiredNil, ErrInvalidPortSwap, PortToolExecutor)

	values := compositionValues{toolExecutor: &recordingToolExecutor{}, inferencer: &recordingInferencer{}}
	if err := applyPortSwap(&values, PortSwap{Name: PortInferencer, Value: nil}); err != nil {
		t.Fatalf("optional nil swap failed: %v", err)
	}
	if values.inferencer != nil {
		t.Fatal("optional nil swap left inferencer available")
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
}

func TestCompositionOptions_InstallOptionalCapabilities(t *testing.T) {
	without, err := applyCompositionOptions(nil)
	if err != nil {
		t.Fatalf("apply empty options: %v", err)
	}
	if without.inferencer != nil || without.sessionInferencer != nil {
		t.Fatal("optional ports should be unavailable by default")
	}

	inferencer := &recordingInferencer{response: "option"}
	sessionInferencer := &recordingSessionInferencer{}
	with, err := applyCompositionOptions([]CompositionOption{
		WithInferencer(inferencer),
		WithSessionInferencer(sessionInferencer),
		WithRelaxedModelValidation(),
	})
	if err != nil {
		t.Fatalf("apply optional options: %v", err)
	}
	if with.inferencer != inferencer || with.sessionInferencer != sessionInferencer || !with.relaxModelValidation {
		t.Fatal("optional composition options were not installed exactly")
	}

	strict, err := applyCompositionOptions([]CompositionOption{WithRelaxedModelValidation(), WithStrictModelValidation()})
	if err != nil || strict.relaxModelValidation {
		t.Fatalf("strict option did not override relaxed option: %#v, %v", strict, err)
	}
	if _, err := ComposeAgentCLI(&recordingToolExecutor{}, nil); err == nil || !strings.Contains(err.Error(), "option 0") {
		t.Fatalf("nil composition option was not rejected: %v", err)
	}

	root, err := ComposeAgentCLI(&recordingToolExecutor{}, WithInferencer(inferencer), WithSessionInferencer(sessionInferencer))
	if err != nil || root == nil {
		t.Fatalf("ComposeAgentCLI with optional ports failed: root=%v err=%v", root, err)
	}
}

func TestComposeAgentCLI_OptionalInferencerIsObservedAtRuntime(t *testing.T) {
	inferencer := &recordingInferencer{response: "exact replacement"}
	root, err := ComposeAgentCLI(&recordingToolExecutor{}, WithInferencer(inferencer))
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

func TestCompositionConstruction_IsInert(t *testing.T) {
	toolExecutor := &recordingToolExecutor{}
	inferencer := &recordingInferencer{response: "unused"}
	sessionInferencer := &recordingSessionInferencer{}
	before := runtime.NumGoroutine()

	root, err := ComposeAgentCLI(
		toolExecutor,
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
