package agentruntime_test

import devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	agentruntime "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/grok"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// TestRTCDeviceAudioCaptureReplaysThroughDevices records a real provider
// session implementation at the raw WebSocket seam, then replays that exact
// capture through a fresh virtual microphone/speaker topology. Both passes
// enter and leave through devices (never --audio-in files), so the provider
// rate negotiation and device conversion path remain under test. Device PCM
// observations prove the mock captured both directions as test evidence.
func TestRTCDeviceAudioCaptureReplaysThroughDevices(t *testing.T) {
	const model = "grok-device-record-replay"
	liveConn := newProductionRTCWebSocketConn()
	liveConn.enqueueEvent("session.created", map[string]any{"session_id": "device-record-replay", "model": model})
	recorder := gwtesting.NewRecordingWebSocketDialer(&productionRTCDialer{conn: liveConn}, "grok", model)
	liveInferencer, err := agentruntime.NewGrokSessionInferencerWithOptions(
		config.GrokConfig{APIKey: "test-key", Model: model},
		grok.WithWebSocketDialer(recorder),
	)
	if err != nil {
		t.Fatalf("build recording provider session: %v", err)
	}
	liveRegistry := newRecordedRTCDeviceRoundtripRegistry(t)
	exerciseRTCDeviceAudioPass(t, liveRegistry, liveInferencer, func() {
		liveConn.enqueueEvent("response.done", nil)
	})
	if !liveConn.inputAudioSeen() {
		t.Fatal("record pass did not serialize device microphone PCM")
	}

	capturePath := filepath.Join(t.TempDir(), "device-audio.session.json")
	if err := recorder.FlushToFile(capturePath); err != nil {
		t.Fatalf("flush device audio capture: %v", err)
	}
	capture, err := gwtesting.LoadSessionCapture(capturePath)
	if err != nil {
		t.Fatalf("load recorder-produced device audio capture: %v", err)
	}
	var inputAudio, outputAudio bool
	for _, event := range capture.Records {
		inputAudio = inputAudio || event.Direction == gwtesting.DirectionClientToServer && event.Type == "input_audio_buffer.append"
		outputAudio = outputAudio || event.Direction == gwtesting.DirectionServerToClient && (event.Type == "response.audio.delta" || event.Type == "response.output_audio.delta")
	}
	if !inputAudio || !outputAudio {
		t.Fatalf("capture audio directions: input=%t output=%t", inputAudio, outputAudio)
	}

	replayDialer, err := gwtesting.NewReplayWebSocketDialer(capturePath)
	if err != nil {
		t.Fatalf("open recorder-produced audio capture for replay: %v", err)
	}
	replayInferencer, err := agentruntime.NewGrokSessionInferencerWithOptions(
		config.GrokConfig{APIKey: "replay-never-dials", Model: model},
		grok.WithWebSocketDialer(replayDialer),
	)
	if err != nil {
		t.Fatalf("build replay provider session: %v", err)
	}
	replayRegistry := newRecordedRTCDeviceRoundtripRegistry(t)
	exerciseRTCDeviceAudioPass(t, replayRegistry, replayInferencer, nil)
	if err := replayDialer.Err(); err != nil {
		t.Fatalf("strict device audio replay diverged: %v", err)
	}
}

func newRecordedRTCDeviceRoundtripRegistry(t *testing.T) *devicegw.VirtualRegistry {
	t.Helper()
	registry, err := devicegw.NewVirtualRegistry(devicegw.VirtualBackendConfig{
		RecordPCM: true,
		Devices: []devicegw.VirtualDeviceConfig{
			{ID: "mic-in", Name: "Recorded Microphone", Direction: devicegw.DirectionInput, LoopbackID: "mic-feed"},
			{ID: "mic-feed", Name: "Recorded Microphone Feed", Direction: devicegw.DirectionOutput, LoopbackID: "mic-in"},
			{ID: "speaker-out", Name: "Recorded Speaker", Direction: devicegw.DirectionOutput, LoopbackID: "speaker-observe"},
			{ID: "speaker-observe", Name: "Recorded Speaker Observer", Direction: devicegw.DirectionInput, LoopbackID: "speaker-out"},
		},
		Defaults: map[devicegw.Direction]string{devicegw.DirectionInput: "mic-in", devicegw.DirectionOutput: "speaker-out"},
	})
	if err != nil {
		t.Fatalf("new recording virtual device registry: %v", err)
	}
	return registry
}

func exerciseRTCDeviceAudioPass(t *testing.T, registry *devicegw.VirtualRegistry, inferencer messages.SessionInferencer, afterOutput func()) {
	t.Helper()
	feed, err := devicegw.NewDeviceSink(registry, rtcRoundtripMicFeedID)
	if err != nil {
		t.Fatalf("open device microphone feeder: %v", err)
	}
	observe, err := devicegw.NewDeviceSource(registry, rtcRoundtripSpeakerID)
	if err != nil {
		_ = feed.Close()
		t.Fatalf("open device speaker observer: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() {
		runErr <- agentruntime.RunSession(ctx, io.Discard, agentruntime.SessionRunOptions{
			ReplayPath: "device-record-replay.json", SessionInferencer: inferencer,
			RTCDeviceBinding: agentruntime.RTCDeviceBindingRequest{
				Registry: registry, InputDevice: rtcRoundtripInputID, OutputDevice: rtcRoundtripOutputID,
				InputPresent: true, OutputPresent: true,
			},
		})
	}()
	want := rtcRoundtripPCMFrame(0)
	readResult := make(chan []int16, 1)
	readError := make(chan error, 1)
	go func() {
		frame := make([]int16, audio.FrameSize)
		if readErr := observe.ReadFrame(ctx, frame); readErr != nil {
			readError <- readErr
			return
		}
		readResult <- frame
	}()
	if err := feed.WriteFrame(ctx, want); err != nil {
		t.Fatalf("write device microphone frame: %v", err)
	}
	select {
	case err := <-readError:
		t.Fatalf("read replayed device speaker frame: %v", err)
	case got := <-readResult:
		if len(got) != len(want) {
			t.Fatalf("replayed speaker samples = %d, want %d", len(got), len(want))
		}
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("replayed speaker sample %d = %d, want %d", index, got[index], want[index])
			}
		}
	}
	if afterOutput != nil {
		afterOutput()
	}
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("device audio session pass: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("device audio session did not terminate: %v", ctx.Err())
	}
	if err := feed.Close(); err != nil {
		t.Fatalf("close device microphone feeder: %v", err)
	}
	if err := observe.Close(); err != nil {
		t.Fatalf("close device speaker observer: %v", err)
	}
	observations := registry.PCMObservations()
	var microphoneRead, speakerWrite bool
	for _, observation := range observations {
		microphoneRead = microphoneRead || observation.DeviceID == rtcRoundtripInputID && observation.Operation == "read"
		speakerWrite = speakerWrite || observation.DeviceID == rtcRoundtripOutputID && observation.Operation == "write"
	}
	if !microphoneRead || !speakerWrite {
		t.Fatalf("recorded mock device PCM: microphone_read=%t speaker_write=%t observations=%d", microphoneRead, speakerWrite, len(observations))
	}
}

// TestRunSessionRTCDeviceBindingUsesProductionProviderMediaOwner exercises
// the CLI service runtime with the real Grok provider session implementation.
// The transport is only a deterministic WebSocket seam: input device PCM is
// serialized by grokSession, echoed as a provider audio delta, then framed by
// the same provider-owned media endpoints for the output device.
func TestRunSessionRTCDeviceBindingUsesProductionProviderMediaOwner(t *testing.T) {
	registry := newRTCDeviceRoundtripRegistry(t)
	feed, err := devicegw.NewDeviceSink(registry, rtcRoundtripMicFeedID)
	if err != nil {
		t.Fatalf("open virtual microphone feeder: %v", err)
	}
	observe, err := devicegw.NewDeviceSource(registry, rtcRoundtripSpeakerID)
	if err != nil {
		_ = feed.Close()
		t.Fatalf("open virtual speaker observer: %v", err)
	}
	conn := newProductionRTCWebSocketConn()
	conn.enqueueEvent("session.created", map[string]any{
		"session_id": "production-rtc-session",
		"model":      "grok-production-test",
	})
	dialer := &productionRTCDialer{conn: conn}
	inferencer, err := agentruntime.NewGrokSessionInferencerWithOptions(
		config.GrokConfig{APIKey: "test-key", Model: "grok-production-test"},
		grok.WithWebSocketDialer(dialer),
	)
	if err != nil {
		_ = feed.Close()
		_ = observe.Close()
		t.Fatalf("build production Grok session inferencer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.Cleanup(func() {
		_ = conn.Close()
		_ = feed.Close()
		_ = observe.Close()
	})

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- agentruntime.RunSession(ctx, io.Discard, agentruntime.SessionRunOptions{
			ReplayPath:        "synthetic.json",
			SessionInferencer: inferencer,
			RTCDeviceBinding: agentruntime.RTCDeviceBindingRequest{
				Registry:      registry,
				InputDevice:   rtcRoundtripInputID,
				OutputDevice:  rtcRoundtripOutputID,
				InputPresent:  true,
				OutputPresent: true,
			},
		})
	}()

	want := rtcRoundtripPCMFrame(0)
	readCtx, readCancel := context.WithTimeout(ctx, 3*time.Second)
	defer readCancel()
	gotCh := make(chan []int16, 1)
	readErrCh := make(chan error, 1)
	go func() {
		frame := make([]int16, audio.FrameSize)
		if err := observe.ReadFrame(readCtx, frame); err != nil {
			readErrCh <- err
			return
		}
		gotCh <- frame
	}()
	if err := feed.WriteFrame(ctx, want); err != nil {
		t.Fatalf("feed virtual microphone frame: %v", err)
	}

	var got []int16
	select {
	case err := <-readErrCh:
		t.Fatalf("read virtual speaker frame: %v", err)
	case got = <-gotCh:
	}
	if len(got) != len(want) {
		t.Fatalf("speaker frame length = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("speaker frame sample %d = %d, want %d", index, got[index], want[index])
		}
	}
	if !conn.inputAudioSeen() {
		t.Fatal("production provider session did not receive input_audio_buffer.append")
	}

	// Let the runtime finish only after the output device has observed the
	// echoed frame, proving the media pump was live for the provider response.
	conn.enqueueEvent("response.done", nil)
	select {
	case err := <-runErrCh:
		if err != nil {
			t.Fatalf("RunSession with production provider media: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("RunSession did not finish after response.done: %v", ctx.Err())
	}

	if err := feed.Close(); err != nil {
		t.Fatalf("close virtual microphone feeder: %v", err)
	}
	if err := observe.Close(); err != nil {
		t.Fatalf("close virtual speaker observer: %v", err)
	}
	if got := registry.Observations(); got.OpenCount != 4 || got.ReleaseCount != 4 {
		t.Fatalf("production runtime registry observations = %+v, want four opens and releases", got)
	}
}

type productionRTCDialer struct {
	conn *productionRTCWebSocketConn
}

func (d *productionRTCDialer) Dial(string, map[string]string) (transport.Conn, error) {
	return d.conn, nil
}

type productionRTCWebSocketConn struct {
	mu sync.Mutex

	serverMessages [][]byte
	readIndex      int
	clientMessages [][]byte
	inputAudio     bool
	responseSent   bool

	readWake  chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
}

func newProductionRTCWebSocketConn() *productionRTCWebSocketConn {
	return &productionRTCWebSocketConn{
		readWake: make(chan struct{}, 1),
		closed:   make(chan struct{}),
	}
}

func (c *productionRTCWebSocketConn) enqueueEvent(eventType string, fields map[string]any) {
	payload := make(map[string]any, len(fields)+1)
	payload["type"] = eventType
	for key, value := range fields {
		payload[key] = value
	}
	data, _ := json.Marshal(payload)
	c.mu.Lock()
	c.serverMessages = append(c.serverMessages, data)
	c.mu.Unlock()
	c.notifyReader()
}

func (c *productionRTCWebSocketConn) ReadMessage() (int, []byte, error) {
	for {
		c.mu.Lock()
		if c.readIndex < len(c.serverMessages) {
			data := append([]byte(nil), c.serverMessages[c.readIndex]...)
			c.readIndex++
			c.mu.Unlock()
			return 1, data, nil
		}
		c.mu.Unlock()

		select {
		case <-c.readWake:
		case <-c.closed:
			return 0, nil, errors.New("production RTC test connection closed")
		}
	}
}

func (c *productionRTCWebSocketConn) WriteMessage(_ int, data []byte) error {
	c.mu.Lock()
	if c.isClosedLocked() {
		c.mu.Unlock()
		return errors.New("production RTC test connection closed")
	}
	c.clientMessages = append(c.clientMessages, append([]byte(nil), data...))
	c.mu.Unlock()

	var event struct {
		Type  string `json:"type"`
		Audio string `json:"audio"`
	}
	if err := json.Unmarshal(data, &event); err != nil || event.Type != "input_audio_buffer.append" {
		return nil
	}

	c.mu.Lock()
	firstAudio := !c.responseSent
	if firstAudio {
		c.responseSent = true
		c.inputAudio = true
	}
	c.mu.Unlock()
	if firstAudio {
		c.enqueueEvent("response.created", nil)
		c.enqueueEvent("response.audio.delta", map[string]any{"delta": event.Audio})
		c.enqueueEvent("response.audio.done", nil)
	}
	return nil
}

func (c *productionRTCWebSocketConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.notifyReader()
	})
	return nil
}

func (c *productionRTCWebSocketConn) inputAudioSeen() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inputAudio
}

func (c *productionRTCWebSocketConn) isClosedLocked() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func (c *productionRTCWebSocketConn) notifyReader() {
	select {
	case c.readWake <- struct{}{}:
	default:
	}
}
