package wire

import servicetest "github.com/portpowered/go-agent-harness/agent-cli/internal/services/servicetest"

import sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

// TestGeneratedRootCLI_WebRTCRejectsBeforeProductionSideEffects proves that
// the shipped graph rejects an otherwise valid WebRTC request before any
// customer-unreachable signaling, peer/media setup, provider connection, or
// device/runtime side effect can occur.
func TestGeneratedRootCLI_WebRTCRejectsBeforeProductionSideEffects(t *testing.T) {
	var signalingCalls, dataPlaneCalls, mediaSourceCalls int
	components := servicetest.SessionRTCComponents{
		ResolveSignaling: func(context.Context, string) (rtc.Signaling, error) {
			signalingCalls++
			return nil, errors.New("signaling resolver should not be reached")
		},
		NewDataPlane: func(context.Context, rtc.Signaling) (servicetest.SessionRTCDataPlane, error) {
			dataPlaneCalls++
			return nil, errors.New("RTC data-plane factory should not be reached")
		},
		OpenMediaSource: func(context.Context, string) (sharedaudio.InboundMedia, error) {
			mediaSourceCalls++
			return nil, errors.New("media-source opener should not be reached")
		},
	}
	provider := &recordingSessionInferencer{}
	transportDialer := &recordingDialer{}
	registry := &recordingDeviceRegistry{}
	audioSource := &recordingAudioSource{}
	audioSink := &recordingAudioSink{}
	toolExecutor := &recordingToolExecutor{}
	app, err := ComposeAgentCLI(
		toolExecutor,
		transportDialer,
		registry,
		audioSource,
		audioSink,
		&recordingClock{},
		WithSessionInferencer(provider),
		WithSessionRTCComponents(components),
	)
	if err != nil {
		t.Fatalf("compose generated root CLI: %v", err)
	}

	var stdout, stderr bytes.Buffer
	root := app.Generate()
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SilenceUsage = true
	root.SetArgs([]string{
		"--config-dir", filepath.Join(t.TempDir(), "missing-config"),
		"session",
		"--record", filepath.Join(t.TempDir(), "must-not-be-created.session.json"),
		"--provider", "grok",
		"--model", "grok-customer-boundary",
		"--api-key", "test-provider-key",
		"--transport", "webrtc",
		"--signaling", "loopback://customer-boundary",
		"--media-source", "fixture://customer-boundary",
		"--audio-in-device", "recording:input",
		"--audio-out-device", "recording:output",
		"complete the customer-boundary turn",
	})

	err = root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("generated root WebRTC session unexpectedly succeeded")
	}
	if !errors.Is(err, cli.ErrSessionWebRTCUnavailable) {
		t.Fatalf("generated root WebRTC error = %v, want customer capability error", err)
	}
	for _, want := range []string{
		"customer-reachable network signaling",
		"spoken-audio input",
		"--transport ws",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("generated root WebRTC error %q missing %q", err, want)
		}
	}
	if signalingCalls != 0 || dataPlaneCalls != 0 || mediaSourceCalls != 0 {
		t.Fatalf("RTC component calls = signaling:%d data-plane:%d media:%d, want zero", signalingCalls, dataPlaneCalls, mediaSourceCalls)
	}
	if provider.connects != 0 {
		t.Fatalf("provider session connects = %d, want zero", provider.connects)
	}
	if transportDials := transportDialer.dials.Load(); transportDials != 0 {
		t.Fatalf("transport dials = %d, want zero", transportDials)
	}
	if registry.lookups != 0 {
		t.Fatalf("device registry lookups = %d, want zero", registry.lookups)
	}
	if audioSource.reads != 0 || audioSink.writes != 0 {
		t.Fatalf("audio I/O = reads:%d writes:%d, want zero", audioSource.reads, audioSink.writes)
	}
	if toolExecutor.calls != 0 {
		t.Fatalf("tool executions = %d, want zero", toolExecutor.calls)
	}
	if strings.Contains(strings.ToLower(stdout.String()+stderr.String()), "usage:") {
		t.Fatalf("customer capability rejection emitted help: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func testLivePortSwap(t *testing.T, definition portDefinition) {
	t.Helper()
	replacement := replacementForPortType(t, definition.descriptor.Type)
	swaps := []PortSwap{{Name: definition.descriptor.Name, Value: replacement}}
	expectedSwaps := map[string]any{definition.descriptor.Name: replacement}
	var fixtureInferencer *recordingInferencer
	if definition.descriptor.Type == reflect.TypeOf((*messages.ToolExecutor)(nil)).Elem() {
		fixtureInferencer = toolCallingInferencer()
		swaps = append([]PortSwap{{Name: PortInferencer, Value: fixtureInferencer}}, swaps...)
		expectedSwaps[PortInferencer] = fixtureInferencer
	}

	var observation assemblyObservation
	root, err := initializeAgentCLIWithPorts(true, observation.record, swaps...)
	if err != nil {
		t.Fatalf("InitializeMockAgentCLIWithPorts(%q): %v", definition.descriptor.Name, err)
	}
	if root == nil {
		t.Fatalf("InitializeMockAgentCLIWithPorts(%q) returned nil root", definition.descriptor.Name)
	}
	if observation.calls != 1 {
		t.Fatalf("assembly boundary calls for %q = %d, want exactly 1", definition.descriptor.Name, observation.calls)
	}
	assertLivePortValues(t, observation, expectedSwaps)
	assertSelectedLivePort(t, definition, replacement, fixtureInferencer, root)
}

func assertLivePortValues(t *testing.T, observation assemblyObservation, expected map[string]any) {
	t.Helper()
	for _, definition := range livePortDefinitions() {
		name := definition.descriptor.Name
		got := definition.value(&observation.values)
		if expectedValue, replaced := expected[name]; replaced {
			if got != expectedValue {
				t.Fatalf("assembly boundary value for %q changed identity: got %T/%p want %T/%p", name, got, got, expectedValue, expectedValue)
			}
			if calls := observation.values.defaultCalls[name]; calls != 0 {
				t.Fatalf("displaced %q default constructor calls = %d, want exactly 0", name, calls)
			}
			continue
		}
		if definition.descriptor.Required && isNilPort(got) {
			t.Fatalf("unswapped required port %q has no valid default", name)
		}
		if got != nil && !reflect.TypeOf(got).Implements(definition.descriptor.Type) {
			t.Fatalf("unswapped port %q has type %T, want %v", name, got, definition.descriptor.Type)
		}
		if definition.defaultValue == nil {
			continue
		}
		if calls := observation.values.defaultCalls[name]; calls != 1 {
			t.Fatalf("unswapped %q default constructor calls = %d, want exactly 1", name, calls)
		}
	}
}

func assertSelectedLivePort(t *testing.T, definition portDefinition, replacement any, fixtureInferencer *recordingInferencer, root *cli.AgentCLI) {
	t.Helper()
	switch definition.descriptor.Type {
	case reflect.TypeOf((*messages.ToolExecutor)(nil)).Elem():
		assertToolPort(t, definition, replacement, fixtureInferencer, root)
	case reflect.TypeOf((*messages.Inferencer)(nil)).Elem():
		assertInferencerPort(t, definition, replacement, root)
	case reflect.TypeOf((*serviceTools.Service)(nil)).Elem():
		assertUntouchedPort(t, definition, replacement)
	case reflect.TypeOf((*messages.SessionInferencer)(nil)).Elem():
		assertSessionInferencerPort(t, definition, replacement, root)
	case reflect.TypeOf((*DeviceRegistry)(nil)).Elem(),
		reflect.TypeOf((*AudioSource)(nil)).Elem(),
		reflect.TypeOf((*AudioSink)(nil)).Elem(),
		reflect.TypeOf((*Clock)(nil)).Elem(),
		reflect.TypeOf((*SessionRuntimeObserver)(nil)).Elem(),
		reflect.TypeOf((*MetricSampler)(nil)).Elem(),
		reflect.TypeOf((*Logger)(nil)).Elem(),
		reflect.TypeOf((*transport.Dialer)(nil)).Elem():
		assertUntouchedPort(t, definition, replacement)
	default:
		t.Fatalf("no root-level observation for live port type %v", definition.descriptor.Type)
	}
}

func assertToolPort(t *testing.T, definition portDefinition, replacement any, fixtureInferencer *recordingInferencer, root *cli.AgentCLI) {
	t.Helper()
	if err := executeAskCommand(t, root); err != nil {
		t.Fatalf("root ask for %q: %v", definition.descriptor.Name, err)
	}
	executor, ok := replacement.(*recordingToolExecutor)
	if !ok {
		t.Fatalf("selected %q replacement has type %T, want *recordingToolExecutor", definition.descriptor.Name, replacement)
	}
	if executor.calls != 1 {
		t.Fatalf("selected %q replacement calls = %d, want exactly 1", definition.descriptor.Name, executor.calls)
	}
	if fixtureInferencer.calls != 2 {
		t.Fatalf("fixture inferencer calls = %d, want the tool turn and final turn", fixtureInferencer.calls)
	}
}

func assertInferencerPort(t *testing.T, definition portDefinition, replacement any, root *cli.AgentCLI) {
	t.Helper()
	if err := executeAskCommand(t, root); err != nil {
		t.Fatalf("root ask for %q: %v", definition.descriptor.Name, err)
	}
	inferencer, ok := replacement.(*recordingInferencer)
	if !ok {
		t.Fatalf("selected %q replacement has type %T, want *recordingInferencer", definition.descriptor.Name, replacement)
	}
	if inferencer.calls != 1 {
		t.Fatalf("selected %q replacement calls = %d, want exactly 1", definition.descriptor.Name, inferencer.calls)
	}
}

func assertSessionInferencerPort(t *testing.T, definition portDefinition, replacement any, root *cli.AgentCLI) {
	t.Helper()
	if err := executeSessionCommand(t, root, true); err != nil {
		t.Fatalf("root session for %q: %v", definition.descriptor.Name, err)
	}
	sessionInferencer, ok := replacement.(*recordingSessionInferencer)
	if !ok {
		t.Fatalf("selected %q replacement has type %T, want *recordingSessionInferencer", definition.descriptor.Name, replacement)
	}
	if sessionInferencer.connects != 1 {
		t.Fatalf("selected %q replacement connects = %d, want exactly 1", definition.descriptor.Name, sessionInferencer.connects)
	}
}

func assertUntouchedPort(t *testing.T, definition portDefinition, replacement any) {
	t.Helper()
	if definition.descriptor.Name != PortTransportDialer {
		return
	}
	dialer, ok := replacement.(*recordingDialer)
	if !ok {
		t.Fatalf("selected %q replacement has type %T, want *recordingDialer", definition.descriptor.Name, replacement)
	}
	if got := dialer.dials.Load(); got != 0 {
		t.Fatalf("selected %q replacement was dialed during construction: %d", definition.descriptor.Name, got)
	}
}

func testOptionalCapability(t *testing.T, definition portDefinition) {
	t.Helper()
	switch definition.descriptor.Type {
	case reflect.TypeOf((*messages.Inferencer)(nil)).Elem():
		testOptionalInferencer(t, definition)
	case reflect.TypeOf((*messages.SessionInferencer)(nil)).Elem():
		testOptionalSessionInferencer(t, definition)
	case reflect.TypeOf((*serviceTools.Service)(nil)).Elem():
		testOptionalToolService(t, definition)
	case reflect.TypeOf((*SessionRuntimeObserver)(nil)).Elem():
		testOptionalRuntimeObserver(t, definition)
	default:
		t.Fatalf("no runtime optional-capability observation for %v", definition.descriptor.Type)
	}
}

func testOptionalToolService(t *testing.T, definition portDefinition) {
	t.Helper()
	t.Run("available_with_option", func(t *testing.T) {
		service := &recordingToolService{}
		root, err := composeTestAgentCLI(&recordingToolExecutor{}, WithToolService(service))
		if err != nil || root == nil {
			t.Fatalf("ComposeAgentCLI with %q: root=%v err=%v", definition.descriptor.Name, root, err)
		}
	})
}

func testOptionalInferencer(t *testing.T, definition portDefinition) {
	t.Helper()
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
}

func testOptionalSessionInferencer(t *testing.T, definition portDefinition) {
	t.Helper()
	t.Run("unavailable_without_option", func(t *testing.T) {
		root, err := composeTestAgentCLI(&recordingToolExecutor{})
		if err != nil || root == nil {
			t.Fatalf("ComposeAgentCLI without %q: root=%v err=%v", definition.descriptor.Name, root, err)
		}
		err = executeSessionCommand(t, root, false)
		if err == nil || !strings.Contains(err.Error(), "openai realtime api key is missing") {
			t.Fatalf("session without %q did not report the unavailable OpenAI capability: %v", definition.descriptor.Name, err)
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
}

func testOptionalRuntimeObserver(t *testing.T, definition portDefinition) {
	t.Helper()
	t.Run("available_with_option", func(t *testing.T) {
		observer := recordingSessionRuntimeObserver{}
		root, err := composeTestAgentCLI(&recordingToolExecutor{}, WithSessionRuntimeObserver(observer))
		if err != nil || root == nil {
			t.Fatalf("ComposeAgentCLI with %q: root=%v err=%v", definition.descriptor.Name, root, err)
		}
	})
}

func TestGeneratedRootLiveSessionUsesInjectedTransportPort(t *testing.T) {
	dialer := &recordingDialer{}
	agentCLI, err := InitializeMockAgentCLIWithPorts(NewPortSwap(PortTransportDialer, dialer))
	if err != nil {
		t.Fatal(err)
	}
	root := agentCLI.Generate()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{
		"--config-dir", t.TempDir(), "session", "--provider", "openai",
		"--model", "gpt-realtime", "--api-key", "test-key", "--no-terminal-tools", "hello",
	})
	if calls := dialer.dials.Load(); calls != 0 {
		t.Fatalf("construction dialed transport %d times", calls)
	}
	err = root.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "composition test transport dial") {
		t.Fatalf("live session did not preserve injected transport failure: %v", err)
	}
	if calls := dialer.dials.Load(); calls != 1 {
		t.Fatalf("transport dials = %d, want one invocation", calls)
	}
}

func writeSyntheticRealtimeCapture(t *testing.T) string {
	t.Helper()
	capture, err := gatewaytesting.SealSessionCapture(gatewaytesting.SessionCapture{
		Version:  gatewaytesting.SessionCaptureVersion,
		Provider: gatewaytesting.SessionProviderMetadata{Name: "openai", Model: "gpt-realtime"},
		Records: []gatewaytesting.CapturedSessionEvent{
			{
				Sequence: 1, Direction: gatewaytesting.DirectionClientToServer, Type: "session.update",
				PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage,
				Payload:     json.RawMessage(`{"type":"session.update","session":{"audio":{"input":{"format":{"type":"audio/pcm","rate":16000,"channels":1}},"output":{"format":{"type":"audio/pcm","rate":16000,"channels":1}}}}}`),
			},
			{
				Sequence: 2, Direction: gatewaytesting.DirectionServerToClient, Type: "session.created",
				PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage, Payload: json.RawMessage(`{"type":"session.created"}`),
			},
		},
	})
	if err != nil {
		t.Fatalf("seal synthetic realtime capture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "synthetic.json")
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal synthetic realtime capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write synthetic realtime capture: %v", err)
	}
	return path
}

type trackingDeviceRegistry struct {
	inner  *devicegw.VirtualRegistry
	opened []devicegw.DeviceID
}

func (r *trackingDeviceRegistry) List() ([]devicegw.Device, error) { return r.inner.List() }

func (r *trackingDeviceRegistry) Default(direction devicegw.Direction) (devicegw.Device, error) {
	return r.inner.Default(direction)
}

func (r *trackingDeviceRegistry) Open(id devicegw.DeviceID) (devicegw.OpenedDevice, error) {
	r.opened = append(r.opened, id)
	return r.inner.Open(id)
}

var _ DeviceRegistry = (*trackingDeviceRegistry)(nil)
