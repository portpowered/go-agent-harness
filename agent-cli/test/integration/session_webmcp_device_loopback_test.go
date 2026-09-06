package integration

import sessionclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"

import sessionservicewire "github.com/portpowered/go-agent-harness/agent-cli/internal/services/wire"

import devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	webmcpTools "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

// TestSessionWebMCPDeviceLoopbackRecordsAndReplaysAudio proves the customer
// path that file-input tests cannot: microphone device -> provider media ->
// WebMCP page tool -> provider audio -> speaker device. The virtual backend's
// recorder is asserted on both device directions so future tests can retain
// and diagnose the exact loop without replacing either device with a file.
func TestSessionWebMCPDeviceLoopbackRecordsAndReplaysAudio(t *testing.T) {
	registry := newWebMCPDeviceRegistry(t)
	feed := openWebMCPVirtualStream(t, registry, "mic-feed")

	broker, page := newWebMCPCubeBroker(t)
	toolSet := webmcpTools.NewBrokerToolSet(broker)
	provider := newWebMCPDeviceProvider()
	capabilityFactory := func(*config.Config) (cli.SessionToolCapabilities, error) {
		return cli.SessionToolCapabilities{
			Executor: toolSet.Executor(), Definitions: toolSet.Definitions(),
			BrowserWatch: broker.Watch, Close: broker.Close,
		}, nil
	}
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	globalFlags.WorkDirPath = t.TempDir()
	owner := cli.NewSessionCommand(flags.NewAskFlags(), globalFlags, newTestSessionService(sessionservicewire.SessionDependencies{Clock: sessionclock.Real{}, SessionInferencer: provider, ToolService: cli.SessionToolCapabilitiesFactory(capabilityFactory), DeviceRegistry: registry}), nil)
	command := owner.Generate()
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{
		"--browser-tools", "webmcp",
		"--provider", "openai", "--model", "gpt-realtime-2.1-mini", "--api-key", "test-key",
		"--audio-in-device", "default", "--audio-out-device", "default",
		"--record", filepath.Join(t.TempDir(), "device-loop.session.json"),
		"--wait-for-close",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- command.ExecuteContext(ctx) }()
	select {
	case <-provider.opened:
	case <-ctx.Done():
		t.Fatal("device-backed WebMCP session did not open")
	}

	// Only a device write triggers the provider script. There is no audio file
	// or direct provider event injection in this integration path.
	for frame := 0; frame < 12; frame++ {
		before := registry.PCMObservations()
		if err := feed.WriteFrame(ctx, webMCPDeviceSignal(audio.FrameSize, 4100+frame)); err != nil {
			t.Fatalf("write customer turn frame %d to microphone device: %v", frame, err)
		}
		if _, err := registry.WaitForPCMObservations(ctx, len(before)+2); err != nil {
			t.Fatalf("wait for microphone callback after frame %d: %v", frame, err)
		}
	}
	select {
	case result := <-provider.toolResult:
		if result.Name != "queue_cube_moves" || !bytes.Contains([]byte(result.Arguments), []byte("ok")) {
			t.Fatalf("provider tool result = %+v", result)
		}
	case <-ctx.Done():
		t.Fatalf("device turn did not complete the WebMCP cube action: provider_frames=%d device_observations=%d page_invocations=%#v", provider.frames.Load(), len(registry.PCMObservations()), page.Invocations())
	}

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("device-backed WebMCP session: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("device-backed WebMCP session did not close")
	}
	var played []int16
	for _, observation := range registry.PCMObservations() {
		if observation.DeviceID == "virtual:speaker" && observation.Operation == "write" {
			played = append(played, observation.Samples...)
		}
	}
	if len(played) == 0 || webMCPAllZero(played) {
		t.Fatalf("recorded assistant replay on the speaker device was missing or silent (observations=%d)", len(registry.PCMObservations()))
	}

	invocations := page.Invocations()
	if len(invocations) != 1 || invocations[0].ToolName != "queue_cube_moves" || string(invocations[0].Input) != `{"moves":["R","U"]}` {
		t.Fatalf("cube page invocations = %#v", invocations)
	}
	observations := registry.PCMObservations()
	var reads, writes int
	for _, observation := range observations {
		switch observation.Operation {
		case "read":
			reads++
		case "write":
			writes++
		}
	}
	if reads == 0 || writes < 2 { // customer feed + assistant playback
		t.Fatalf("recorded device evidence reads=%d writes=%d, want both capture and render", reads, writes)
	}
}

func newWebMCPDeviceRegistry(t *testing.T) *devicegw.VirtualRegistry {
	t.Helper()
	registry, err := devicegw.NewVirtualRegistry(devicegw.VirtualBackendConfig{
		RecordPCM: true,
		Devices: []devicegw.VirtualDeviceConfig{
			{ID: "mic", Name: "WebMCP Mic", Direction: devicegw.DirectionInput, LoopbackID: "mic-feed"},
			{ID: "mic-feed", Name: "WebMCP Mic Feed", Direction: devicegw.DirectionOutput, LoopbackID: "mic"},
			{ID: "speaker", Name: "WebMCP Speaker", Direction: devicegw.DirectionOutput, LoopbackID: "speaker-tap"},
			{ID: "speaker-tap", Name: "WebMCP Speaker Tap", Direction: devicegw.DirectionInput, LoopbackID: "speaker"},
		},
		Defaults: map[devicegw.Direction]string{devicegw.DirectionInput: "mic", devicegw.DirectionOutput: "speaker"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func openWebMCPVirtualStream(t *testing.T, registry *devicegw.VirtualRegistry, nativeID string) *devicegw.VirtualStream {
	t.Helper()
	id, err := devicegw.NewDeviceID(devicegw.VirtualBackendName, nativeID)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := registry.Open(id)
	if err != nil {
		t.Fatal(err)
	}
	stream := opened.(*devicegw.VirtualStream)
	t.Cleanup(func() { _ = stream.Close() })
	return stream
}

type webMCPDeviceDiscoverer struct{ candidate webmcp.BrowserCandidate }

func (d webMCPDeviceDiscoverer) Discover(ctx context.Context, _ webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []webmcp.BrowserCandidate{d.candidate}, nil
}

func newWebMCPCubeBroker(t *testing.T) (*webmcp.StatefulBroker, *testkit.ScriptedTargetSession) {
	t.Helper()
	candidate := webmcp.BrowserCandidate{ID: "device-browser", Product: "fixture", Loopback: true}
	target := webmcp.Target{BrowserID: candidate.ID, ID: "cube-tab", Type: "page", Title: "Cubecade", URL: "https://cube.test/", Origin: "https://cube.test"}
	write := false
	runtime := testkit.NewScriptedBrowserRuntimeWithOptions(testkit.RuntimeOptions{IDs: testkit.NewDeterministicIDs()}, testkit.BrowserConfig{
		Candidate: candidate,
		Targets: []testkit.TargetConfig{testkit.NewTargetConfig(target,
			testkit.WithInitialCatalog(webmcp.ToolDescriptor{
				Name: "queue_cube_moves", Description: "Queue Rubik's cube moves.", FrameID: "cube-frame",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"moves":{"type":"array","items":{"type":"string"}}},"required":["moves"],"additionalProperties":false}`),
				Annotations: webmcp.ToolAnnotations{ReadOnly: &write},
			}),
			testkit.WithAutoResponse(json.RawMessage(`{"ok":true,"queued":["R","U"]}`)),
		)},
	})
	broker := webmcp.NewBroker(webmcp.BrokerOptions{Runtime: runtime, Discoverer: webMCPDeviceDiscoverer{candidate}, IDs: testkit.NewDeterministicIDs()})
	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: target.ID}); err != nil {
		t.Fatal(err)
	}
	handle, err := runtime.Open(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	page := handle.(*testkit.ScriptedBrowserHandle).TargetSession(target.ID)
	if page == nil {
		t.Fatal("cube target session is nil")
	}
	return broker, page
}

type webMCPDeviceInbound struct {
	frames chan audio.PCMFrame
	done   chan struct{}
	read   chan struct{}
	once   sync.Once
}

func (m *webMCPDeviceInbound) ReadFrame(ctx context.Context) (audio.PCMFrame, error) {
	select {
	case frame := <-m.frames:
		select {
		case m.read <- struct{}{}:
		default:
		}
		return frame, nil
	case <-m.done:
		select {
		case frame := <-m.frames:
			return frame, nil
		default:
			return audio.PCMFrame{}, rtc.ErrPeerClosed
		}
	case <-ctx.Done():
		return audio.PCMFrame{}, ctx.Err()
	}
}
func (m *webMCPDeviceInbound) Close() error { m.once.Do(func() { close(m.done) }); return nil }

type webMCPDeviceOutbound struct {
	once    sync.Once
	onAudio func()
}

func (m *webMCPDeviceOutbound) WriteFrame(ctx context.Context, _ audio.PCMFrame) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.once.Do(m.onAudio)
	return nil
}
func (*webMCPDeviceOutbound) Close() error { return nil }

type webMCPDeviceSession struct {
	recv         *messages.TypedBuffer[messages.StreamMessage]
	done         chan struct{}
	inbound      *webMCPDeviceInbound
	outbound     *webMCPDeviceOutbound
	result       chan messages.ToolCallEndValue
	closeOnce    sync.Once
	resultOnce   sync.Once
	continueOnce sync.Once
}

func (s *webMCPDeviceSession) RTCMedia() audio.MediaEndpoints {
	return audio.MediaEndpoints{Inbound: s.inbound, Outbound: s.outbound}
}
func (s *webMCPDeviceSession) Send(ctx context.Context, message messages.StreamMessage) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	if message.Type == messages.StreamTypeToolCallEnd {
		if value, ok := message.Value.(*messages.ToolCallEndValue); ok && value != nil && value.ToolCallID == "cube-call" {
			s.resultOnce.Do(func() {
				s.result <- *value
			})
		}
	}
	if message.Type == messages.StreamTypeResponseCreate {
		s.continueOnce.Do(func() {
			go func() {
				// Provider events are asynchronous to the client send boundary.
				// Preserve that ordering so the session observer registers the
				// continuation request before its response begins.
				time.Sleep(25 * time.Millisecond)
				s.recv.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "cube-continuation", Value: messages.NewMessageStartValue()})
				s.recv.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, ResponseID: "cube-continuation", Value: messages.NewAudioStartValue()})
				s.inbound.frames <- audio.PCMFrame{Samples: webMCPDeviceSignal(720, 9300), EndOfResponse: true}
				s.recv.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, ResponseID: "cube-continuation", Value: messages.NewTextStartValue()})
				s.recv.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: "cube-continuation", Value: messages.NewTextDeltaValue("Cube moves queued.")})
				s.recv.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, ResponseID: "cube-continuation", Value: messages.NewTextEndValue()})
				s.recv.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, ResponseID: "cube-continuation", Value: messages.NewAudioEndValue()})
				s.recv.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "cube-continuation", Value: messages.NewMessageEndValue(messages.TokenUsage{})})
				select {
				case <-s.inbound.read:
				case <-time.After(time.Second):
				}
				time.Sleep(100 * time.Millisecond)
				s.recv.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("webmcp-device", "fixture complete")})
			}()
		})
	}
	return true
}
func (s *webMCPDeviceSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.recv }
func (s *webMCPDeviceSession) Done() <-chan struct{}                                  { return s.done }
func (s *webMCPDeviceSession) Close() error {
	s.closeOnce.Do(func() { close(s.done); _ = s.inbound.Close() })
	return nil
}
func (s *webMCPDeviceSession) emitCubeCall() {
	ctx := context.Background()
	s.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "cube-response", Value: messages.NewMessageStartValue()})
	s.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, ResponseID: "cube-response", Value: messages.NewAudioStartValue()})
	s.inbound.frames <- audio.PCMFrame{Samples: webMCPDeviceSignal(720, 7300), EndOfResponse: true}
	s.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, ResponseID: "cube-response", Value: messages.NewAudioEndValue()})
	s.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, ResponseID: "cube-response", Value: messages.NewToolCallStartValue("cube-call", "queue_cube_moves")})
	s.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ResponseID: "cube-response", Value: messages.NewToolCallEndValue("cube-call", "queue_cube_moves", `{"moves":["R","U"]}`)})
	s.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "cube-response", Value: messages.NewMessageEndValue(messages.TokenUsage{})})
}

type webMCPDeviceProvider struct {
	opened     chan struct{}
	toolResult chan messages.ToolCallEndValue
	once       sync.Once
	frames     atomic.Int64
}

func newWebMCPDeviceProvider() *webMCPDeviceProvider {
	return &webMCPDeviceProvider{opened: make(chan struct{}), toolResult: make(chan messages.ToolCallEndValue, 1)}
}
func (p *webMCPDeviceProvider) ConnectSession(context.Context) (messages.Session, error) {
	session := &webMCPDeviceSession{
		recv: messages.NewTypedBuffer[messages.StreamMessage](64), done: make(chan struct{}),
		inbound: &webMCPDeviceInbound{frames: make(chan audio.PCMFrame, 4), done: make(chan struct{}), read: make(chan struct{}, 1)}, result: p.toolResult,
	}
	session.outbound = &webMCPDeviceOutbound{onAudio: func() {
		p.frames.Add(1)
		session.emitCubeCall()
	}}
	session.recv.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("webmcp-device", "fixture")})
	session.recv.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeSessionUpdated, Value: messages.NewSessionUpdatedValue("webmcp-device")})
	p.once.Do(func() { close(p.opened) })
	return session, nil
}

func webMCPDeviceSignal(count, seed int) []int16 {
	result := make([]int16, count)
	state := uint32(seed)
	for index := range result {
		state = state*1664525 + 1013904223
		result[index] = int16(int32(state>>16)%18000 - 9000) //nolint:gosec // deterministic PCM fixture
	}
	return result
}
func webMCPAllZero(samples []int16) bool {
	for _, sample := range samples {
		if sample != 0 {
			return false
		}
	}
	return true
}

var _ messages.SessionInferencer = (*webMCPDeviceProvider)(nil)
var _ messages.Session = (*webMCPDeviceSession)(nil)
var _ audio.MediaSession = (*webMCPDeviceSession)(nil)
var _ audio.InboundMedia = (*webMCPDeviceInbound)(nil)
var _ audio.OutboundMedia = (*webMCPDeviceOutbound)(nil)
