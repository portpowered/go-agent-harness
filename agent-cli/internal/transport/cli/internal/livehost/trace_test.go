package livehost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	runtimeRecording "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	runtimeSessionTrace "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	runtimeSessionTraceWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/recording"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestLiveTraceRecorderRoundTripsProviderAndPCM(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "bundle")
	provider := filepath.Join(root, "provider.json")
	writeTraceProviderCapture(t, provider)
	inner := &traceTestRecorder{destination: bundle}
	traced, err := newLiveTraceRecorder(inner, bundle, provider, 16_000, clock.Real{})
	if err != nil {
		t.Fatalf("newLiveTraceRecorder: %v", err)
	}
	wantSamples := []int16{1, -2, 3, -5}
	if err := traced.RecordAudio(context.Background(), runtimeSession.LiveAudioRecord{
		Direction: runtimeSession.LiveRecordAgent,
		Admission: runtimeSession.LiveAudioMediaBridged,
		Frame:     audio.PCMFrame{Format: audio.PCM16DeviceFormat(16_000), Samples: wantSamples},
	}); err != nil {
		t.Fatalf("RecordAudio: %v", err)
	}
	if err := traced.Finalize(context.Background(), nil); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	assertTraceReplay(t, filepath.Join(bundle, "audio-trace"), wantSamples)
	if inner.finalized != 1 {
		t.Fatalf("inner finalization count = %d, want 1", inner.finalized)
	}
}

func assertTraceReplay(t *testing.T, path string, wantSamples []int16) {
	t.Helper()
	replay, err := recording.OpenReplay(path)
	if err != nil {
		t.Fatalf("OpenReplay: %v", err)
	}
	var audioFrames int
	var providerWires int
	for {
		event, frame, nextErr := replay.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			t.Fatalf("Replay.Next: %v", nextErr)
		}
		if event.Kind == "audio" {
			audioFrames++
			assertTraceAudioFrame(t, frame, wantSamples)
		}
		if event.Kind == "runtime" && strings.HasPrefix(event.RuntimeKind, "provider_wire_") {
			providerWires++
		}
	}
	if audioFrames != 1 {
		t.Fatalf("audio frame count = %d, want 1", audioFrames)
	}
	if providerWires != 3 {
		t.Fatalf("provider wire count = %d, want 3", providerWires)
	}
}

func assertTraceAudioFrame(t *testing.T, frame *audio.PCMFrame, wantSamples []int16) {
	t.Helper()
	if frame == nil || len(frame.Samples) != len(wantSamples) {
		t.Fatalf("audio frame = %+v, want %d samples", frame, len(wantSamples))
	}
	for i, sample := range frame.Samples {
		if sample != wantSamples[i] {
			t.Fatalf("audio sample[%d] = %d, want %d", i, sample, wantSamples[i])
		}
	}
}

func TestLiveTraceRecorderRetainsTraceWhenProviderEvidenceIsMissing(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "bundle")
	stagedProvider := filepath.Join(root, "missing-provider.json")
	inner := &traceTestRecorder{destination: bundle}
	traced, err := newLiveTraceRecorder(inner, bundle, stagedProvider, 16_000, clock.Real{})
	if err != nil {
		t.Fatalf("newLiveTraceRecorder: %v", err)
	}
	err = traced.Finalize(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "load provider capture for audio trace") {
		t.Fatalf("Finalize error = %v, want provider capture diagnostic", err)
	}
	if _, statErr := os.Stat(filepath.Join(bundle, "audio-trace")); !os.IsNotExist(statErr) {
		t.Fatalf("missing provider evidence attached trace: stat error = %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(traced.tracePath, "timeline.jsonl")); statErr != nil {
		t.Fatalf("retained trace timeline: %v", statErr)
	}
}

func TestLiveTraceRecorderRetainsTraceWhenProviderEvidenceIsCorrupt(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "bundle")
	provider := filepath.Join(root, "provider.json")
	if err := os.WriteFile(provider, []byte("not-json\n"), 0o600); err != nil {
		t.Fatalf("write corrupt provider capture: %v", err)
	}
	inner := &traceTestRecorder{destination: bundle}
	traced, err := newLiveTraceRecorder(inner, bundle, provider, 16_000, clock.Real{})
	if err != nil {
		t.Fatalf("newLiveTraceRecorder: %v", err)
	}
	err = traced.Finalize(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "load provider capture for audio trace") {
		t.Fatalf("Finalize error = %v, want provider capture diagnostic", err)
	}
	if _, statErr := os.Stat(filepath.Join(bundle, "audio-trace")); !os.IsNotExist(statErr) {
		t.Fatalf("corrupt provider evidence attached trace: stat error = %v", statErr)
	}
}

type traceTestRecorder struct {
	destination string
	finalized   int
}

func (r *traceTestRecorder) RecordMessage(context.Context, runtimeSession.LiveRecord) error {
	return nil
}

func (r *traceTestRecorder) RecordAudio(context.Context, runtimeSession.LiveAudioRecord) error {
	return nil
}

func (r *traceTestRecorder) RecordEvent(context.Context, runtimeSession.LiveEvent) error {
	return nil
}

func (r *traceTestRecorder) Finalize(context.Context, error) error {
	r.finalized++
	return os.MkdirAll(r.destination, 0o755)
}

func writeTraceProviderCapture(t *testing.T, path string) {
	t.Helper()
	websocket := func(sequence int, direction gatewaytesting.SessionEventDirection, event string) gatewaytesting.CapturedSessionEvent {
		return gatewaytesting.CapturedSessionEvent{
			Sequence: sequence, Direction: direction, TimestampMs: int64(sequence),
			Type: eventType(event), PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage,
			Payload: json.RawMessage(event),
		}
	}
	capture, err := gatewaytesting.SealSessionCapture(gatewaytesting.SessionCapture{
		Version:  gatewaytesting.SessionCaptureVersion,
		Provider: gatewaytesting.SessionProviderMetadata{Name: "openai", Model: "gpt-test"},
		Records: []gatewaytesting.CapturedSessionEvent{
			websocket(1, gatewaytesting.DirectionClientToServer, `{"type":"session.update","session":{"model":"gpt-test"}}`),
			websocket(2, gatewaytesting.DirectionServerToClient, `{"type":"session.created","session":{"model":"gpt-test"}}`),
			websocket(3, gatewaytesting.DirectionServerToClient, `{"type":"response.done"}`),
		},
	})
	if err != nil {
		t.Fatalf("SealSessionCapture: %v", err)
	}
	data, err := json.Marshal(capture)
	if err != nil {
		t.Fatalf("marshal provider capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write provider capture: %v", err)
	}
}

func eventType(payload string) string {
	var value struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(payload), &value); err != nil {
		return ""
	}
	return value.Type
}

var _ runtimeSession.LiveRecorder = (*traceTestRecorder)(nil)

func TestTraceRequiredFailsClosedWithoutPublicService(t *testing.T) {
	for _, test := range []struct {
		name       string
		traceAudio bool
	}{
		{name: "trace audio", traceAudio: true},
		{name: "record directory", traceAudio: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			probe := &traceHostProbe{}
			recording := &traceRecordingProbe{}
			request := serviceSession.Request{TraceAudio: test.traceAudio, RecordDirectory: filepath.Join(root, "bundle")}

			err := Run(context.Background(), nil, request, Dependencies{
				LiveService: probe,
				BuildRequest: func(context.Context, serviceSession.Request, *runtimeReplay.CaptureInspection) (runtimeSession.LiveRequest, error) {
					probe.built.Add(1)
					return runtimeSession.LiveRequest{SessionID: "trace-required", OutputAudioSampleRate: 16_000}, nil
				},
				RecordingService: recording,
				CredentialValues: func(serviceSession.Request) ([]string, error) {
					recording.credentials.Add(1)
					return nil, nil
				},
			})
			if err == nil || !strings.Contains(err.Error(), "live session trace service is unavailable") {
				t.Fatalf("Run error = %v, want fail-closed public trace-service diagnostic", err)
			}
			if got := probe.built.Load(); got != 0 {
				t.Fatalf("request builder calls = %d, want 0 before trace admission", got)
			}
			if got := probe.runs.Load(); got != 0 {
				t.Fatalf("live runner calls = %d, want 0 before trace admission", got)
			}
			if got := recording.opened.Load(); got != 0 {
				t.Fatalf("recording opens = %d, want 0 before trace admission", got)
			}
			if got := recording.credentials.Load(); got != 0 {
				t.Fatalf("credential resolutions = %d, want 0 before trace admission", got)
			}
			if _, statErr := os.Stat(request.RecordDirectory); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("record directory side effect: stat error = %v", statErr)
			}
		})
	}
}

func TestTraceFailClosedKeepsTraceDisabledBehavior(t *testing.T) {
	wantErr := errors.New("trace-disabled runner result")
	probe := &traceHostProbe{runErr: wantErr}
	err := Run(context.Background(), nil, serviceSession.Request{}, Dependencies{
		LiveService: probe,
		BuildRequest: func(context.Context, serviceSession.Request, *runtimeReplay.CaptureInspection) (runtimeSession.LiveRequest, error) {
			probe.built.Add(1)
			return runtimeSession.LiveRequest{SessionID: "trace-disabled", OutputAudioSampleRate: 16_000}, nil
		},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}
	if got := probe.built.Load(); got != 1 {
		t.Fatalf("request builder calls = %d, want 1", got)
	}
	if got := probe.runs.Load(); got != 1 {
		t.Fatalf("live runner calls = %d, want 1", got)
	}
}

func TestPublicSessionTracePublishesRedactedRuntimeAndAudioAfterRecording(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "bundle")
	secret := "trace-secret-value"
	capture := &traceCaptureProbe{}
	playback := &tracePlaybackProbe{renderSupported: true}
	devices := &traceDeviceProbe{handle: &traceDeviceHandle{ports: runtimeDevices.MediaPorts{Capture: capture, Playback: playback}}}
	probe := &traceHostProbe{run: func(options runtimeSession.LiveRunOptions) error {
		if options.Recorder == nil {
			t.Fatal("live options recorder is nil")
		}
		handle, err := options.Devices.Open(context.Background(), options.DeviceRequest)
		if err != nil {
			return err
		}
		defer handle.Close()
		capture.emitPreGate(16_000, []int16{11, -12, 13})
		capture.emitUploaded(16_000, []int16{14, -15, 16})
		if err := playback.emitEnqueued(context.Background(), 16_000, []int16{17, -18, 19}); err != nil {
			return err
		}
		playback.emitRendered(16_000, []int16{20, -21, 22})
		message := messages.StreamMessage{
			Type:  messages.StreamTypeResponseCreate,
			Value: &messages.ResponseCreateValue{Type: "response_create", Instructions: secret},
		}
		if err := options.Recorder.RecordMessage(context.Background(), runtimeSession.LiveRecord{Direction: runtimeSession.LiveRecordAgent, Timestamp: time.Unix(1, 0), Message: message}); err != nil {
			return err
		}
		if err := options.Recorder.RecordAudio(context.Background(), runtimeSession.LiveAudioRecord{
			Direction: runtimeSession.LiveRecordClient, Admission: runtimeSession.LiveAudioQueueAdmitted,
			Frame: audio.PCMFrame{Format: audio.PCM16DeviceFormat(16_000), Samples: []int16{1, -2, 3}},
		}); err != nil {
			return err
		}
		if err := options.Recorder.RecordAudio(context.Background(), runtimeSession.LiveAudioRecord{
			Direction: runtimeSession.LiveRecordClient, Admission: runtimeSession.LiveAudioMediaBridged,
			Frame: audio.PCMFrame{Format: audio.PCM16DeviceFormat(16_000), Samples: []int16{4, -5, 6}},
		}); err != nil {
			return err
		}
		if err := options.Recorder.RecordAudio(context.Background(), runtimeSession.LiveAudioRecord{
			Direction: runtimeSession.LiveRecordAgent, Admission: runtimeSession.LiveAudioMediaBridged,
			Frame: audio.PCMFrame{Format: audio.PCM16DeviceFormat(16_000), Samples: []int16{7, -8, 9}},
		}); err != nil {
			return err
		}
		if err := options.Recorder.RecordEvent(context.Background(), runtimeSession.LiveEvent{Kind: string(runtimeSession.LiveEventTerminal)}); err != nil {
			return err
		}
		return options.Recorder.Finalize(context.Background(), nil)
	}}
	recording := &traceRecordingProbe{}
	err := Run(context.Background(), nil, serviceSession.Request{TraceAudio: true, RecordDirectory: bundle, AudioInputDevicePresent: true, AudioOutputDevicePresent: true}, Dependencies{
		LiveService:   probe,
		DeviceService: devices,
		TraceService:  runtimeSessionTraceWire.NewService(),
		BuildRequest: func(context.Context, serviceSession.Request, *runtimeReplay.CaptureInspection) (runtimeSession.LiveRequest, error) {
			return runtimeSession.LiveRequest{SessionID: "trace-public", InputAudioSampleRate: 16_000, OutputAudioSampleRate: 16_000}, nil
		},
		RecordingService: recording,
		CredentialValues: func(serviceSession.Request) ([]string, error) { return []string{secret}, nil },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	timelinePath := filepath.Join(bundle, "audio-trace", "timeline.jsonl")
	timeline, err := os.ReadFile(timelinePath)
	if err != nil {
		t.Fatalf("read published timeline: %v", err)
	}
	if strings.Contains(string(timeline), secret) {
		t.Fatalf("timeline leaked credential %q: %s", secret, timeline)
	}
	for _, kind := range []string{"provider_wire_receive", "response_create", "audio_input", "audio_output", "terminal"} {
		if !strings.Contains(string(timeline), `"runtime_kind":"`+kind+`"`) {
			t.Fatalf("timeline missing runtime kind %q: %s", kind, timeline)
		}
	}
	for _, name := range []string{"microphone-pre-gate.wav", "microphone-uploaded.wav", "speaker-enqueued.wav", "speaker-rendered.wav"} {
		if _, err := os.Stat(filepath.Join(bundle, "audio-trace", name)); err != nil {
			t.Fatalf("trace audio %s: %v", name, err)
		}
	}
	if strings.Contains(string(timeline), `"runtime_kind":"audio_render_tap_unavailable"`) {
		t.Fatalf("callback-capable renderer was marked unavailable: %s", timeline)
	}
	if got := capture.preGateSamples(); !equalTraceSamples(got, []int16{11, -12, 13}) {
		t.Fatalf("pre-gate callback samples = %v, want callback payload", got)
	}
	if got := capture.uploadedSamples(); !equalTraceSamples(got, []int16{14, -15, 16}) {
		t.Fatalf("uploaded callback samples = %v, want callback payload", got)
	}
	if got := playback.enqueuedSamples(); !equalTraceSamples(got, []int16{17, -18, 19}) {
		t.Fatalf("enqueued callback samples = %v, want callback payload", got)
	}
	if got := playback.renderedSamples(); !equalTraceSamples(got, []int16{20, -21, 22}) {
		t.Fatalf("rendered callback samples = %v, want callback payload", got)
	}
}

func TestPublicSessionTraceMarksUnsupportedRenderBoundary(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "bundle")
	playback := &tracePlaybackProbe{}
	devices := &traceDeviceProbe{handle: &traceDeviceHandle{ports: runtimeDevices.MediaPorts{Playback: playback}}}
	probe := &traceHostProbe{run: func(options runtimeSession.LiveRunOptions) error {
		handle, err := options.Devices.Open(context.Background(), options.DeviceRequest)
		if err != nil {
			return err
		}
		defer handle.Close()
		return options.Recorder.Finalize(context.Background(), nil)
	}}
	err := Run(context.Background(), nil, serviceSession.Request{TraceAudio: true, RecordDirectory: bundle, AudioOutputDevicePresent: true}, Dependencies{
		LiveService:   probe,
		DeviceService: devices,
		TraceService:  runtimeSessionTraceWire.NewService(),
		BuildRequest: func(context.Context, serviceSession.Request, *runtimeReplay.CaptureInspection) (runtimeSession.LiveRequest, error) {
			return runtimeSession.LiveRequest{SessionID: "trace-render-unavailable", OutputAudioSampleRate: 16_000}, nil
		},
		RecordingService: &traceRecordingProbe{},
		CredentialValues: func(serviceSession.Request) ([]string, error) { return nil, nil },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	timeline, err := os.ReadFile(filepath.Join(bundle, "audio-trace", "timeline.jsonl"))
	if err != nil {
		t.Fatalf("read published timeline: %v", err)
	}
	if !strings.Contains(string(timeline), `"runtime_kind":"audio_render_tap_unavailable"`) {
		t.Fatalf("timeline missing render-unavailable observation: %s", timeline)
	}
	if _, err := os.Stat(filepath.Join(bundle, "audio-trace", "speaker-rendered.wav")); !os.IsNotExist(err) {
		t.Fatalf("unsupported render unexpectedly published speaker-rendered.wav: %v", err)
	}
}

func TestPublicSessionTraceDoesNotOverwriteExistingDestination(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "bundle")
	recording := &traceRecordingProbe{existingTrace: true}
	probe := &traceHostProbe{run: func(options runtimeSession.LiveRunOptions) error {
		return options.Recorder.Finalize(context.Background(), nil)
	}}
	err := Run(context.Background(), nil, serviceSession.Request{TraceAudio: true, RecordDirectory: bundle}, Dependencies{
		LiveService:  probe,
		TraceService: runtimeSessionTraceWire.NewService(),
		BuildRequest: func(context.Context, serviceSession.Request, *runtimeReplay.CaptureInspection) (runtimeSession.LiveRequest, error) {
			return runtimeSession.LiveRequest{SessionID: "trace-duplicate", OutputAudioSampleRate: 16_000}, nil
		},
		RecordingService: recording,
		CredentialValues: func(serviceSession.Request) ([]string, error) { return nil, nil },
	})
	if !errors.Is(err, runtimeSessionTrace.ErrDestinationExists) {
		t.Fatalf("Run error = %v, want destination conflict", err)
	}
	sentinel, readErr := os.ReadFile(filepath.Join(bundle, "audio-trace", "timeline.jsonl"))
	if readErr != nil {
		t.Fatalf("read existing destination: %v", readErr)
	}
	if string(sentinel) != "existing-trace" {
		t.Fatalf("existing destination changed to %q", sentinel)
	}
}

type traceHostProbe struct {
	built  atomic.Int32
	runs   atomic.Int32
	runErr error
	run    func(runtimeSession.LiveRunOptions) error
}

func (p *traceHostProbe) OpenLive(context.Context, runtimeSession.LiveRequest) (runtimeSession.LiveHandle, error) {
	return nil, errors.New("trace host probe does not open handles")
}

func (p *traceHostProbe) RunLive(_ context.Context, options runtimeSession.LiveRunOptions) error {
	p.runs.Add(1)
	if p.run != nil {
		return p.run(options)
	}
	return p.runErr
}

type traceRecordingProbe struct {
	opened        atomic.Int32
	credentials   atomic.Int32
	existingTrace bool
}

func (p *traceRecordingProbe) TrackSession(messages.SessionInferencer, runtimeRecording.Writer, string) (runtimeRecording.SessionCapture, error) {
	return nil, nil
}

func (p *traceRecordingProbe) OpenLiveEvidence(options runtimeRecording.LiveEvidenceOptions) (runtimeSession.LiveRecorder, error) {
	p.opened.Add(1)
	if err := os.MkdirAll(options.Destination, 0o755); err != nil {
		return nil, err
	}
	if p.existingTrace {
		path := filepath.Join(options.Destination, "audio-trace")
		if err := os.MkdirAll(path, 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(path, "timeline.jsonl"), []byte("existing-trace"), 0o600); err != nil {
			return nil, err
		}
	}
	return &traceRecorderProbe{destination: options.Destination}, nil
}

func (p *traceRecordingProbe) OpenLiveSemanticEvidence(string) (runtimeSession.LiveRecorder, error) {
	p.opened.Add(1)
	return &traceRecorderProbe{}, nil
}

type traceRecorderProbe struct{ destination string }

func (*traceRecorderProbe) RecordMessage(context.Context, runtimeSession.LiveRecord) error {
	return nil
}

func (*traceRecorderProbe) RecordAudio(context.Context, runtimeSession.LiveAudioRecord) error {
	return nil
}

func (*traceRecorderProbe) RecordEvent(context.Context, runtimeSession.LiveEvent) error {
	return nil
}

func (r *traceRecorderProbe) Finalize(context.Context, error) error {
	if r == nil || r.destination == "" {
		return nil
	}
	return os.MkdirAll(r.destination, 0o755)
}

type traceDeviceProbe struct {
	handle *traceDeviceHandle
	opened atomic.Int32
}

func (p *traceDeviceProbe) Open(context.Context, runtimeDevices.Request) (runtimeDevices.Handle, error) {
	p.opened.Add(1)
	return p.handle, nil
}

type traceDeviceHandle struct {
	ports runtimeDevices.MediaPorts
}

func (h *traceDeviceHandle) Media() runtimeDevices.MediaPorts {
	if h == nil {
		return runtimeDevices.MediaPorts{}
	}
	return h.ports
}

func (*traceDeviceHandle) Close() error { return nil }

type traceCaptureProbe struct {
	preGate      func(int, []int16)
	uploaded     func(int, []int16)
	preGateSeen  []int16
	uploadedSeen []int16
}

func (*traceCaptureProbe) Pump(context.Context, audio.OutboundMedia) error { return nil }
func (*traceCaptureProbe) Close() error                                    { return nil }

func (p *traceCaptureProbe) SetPreGateSamplesObserver(observer func(int, []int16)) {
	p.preGate = observer
}

func (p *traceCaptureProbe) SetUploadedSamplesObserver(observer func(int, []int16)) {
	p.uploaded = observer
}

func (p *traceCaptureProbe) emitPreGate(rate int, samples []int16) {
	p.preGateSeen = append([]int16(nil), samples...)
	if p.preGate != nil {
		p.preGate(rate, append([]int16(nil), samples...))
	}
}

func (p *traceCaptureProbe) emitUploaded(rate int, samples []int16) {
	p.uploadedSeen = append([]int16(nil), samples...)
	if p.uploaded != nil {
		p.uploaded(rate, append([]int16(nil), samples...))
	}
}

func (p *traceCaptureProbe) preGateSamples() []int16 {
	return append([]int16(nil), p.preGateSeen...)
}

func (p *traceCaptureProbe) uploadedSamples() []int16 {
	return append([]int16(nil), p.uploadedSeen...)
}

type tracePlaybackProbe struct {
	enqueued        func(context.Context, int, []int16) error
	rendered        func(int, []int16)
	renderSupported bool
	enqueuedSeen    []int16
	renderedSeen    []int16
}

func (*tracePlaybackProbe) Pump(context.Context, audio.InboundMedia) error { return nil }
func (*tracePlaybackProbe) Close() error                                   { return nil }

func (p *tracePlaybackProbe) SetPlaybackSamplesObserver(observer func(context.Context, int, []int16) error) {
	p.enqueued = observer
}

func (p *tracePlaybackProbe) SetRenderedSamplesObserver(observer func(int, []int16)) bool {
	p.rendered = observer
	return p.renderSupported
}

func (p *tracePlaybackProbe) emitEnqueued(ctx context.Context, rate int, samples []int16) error {
	p.enqueuedSeen = append([]int16(nil), samples...)
	if p.enqueued == nil {
		return nil
	}
	return p.enqueued(ctx, rate, append([]int16(nil), samples...))
}

func (p *tracePlaybackProbe) emitRendered(rate int, samples []int16) {
	p.renderedSeen = append([]int16(nil), samples...)
	if p.rendered != nil {
		p.rendered(rate, append([]int16(nil), samples...))
	}
}

func (p *tracePlaybackProbe) enqueuedSamples() []int16 {
	return append([]int16(nil), p.enqueuedSeen...)
}

func (p *tracePlaybackProbe) renderedSamples() []int16 {
	return append([]int16(nil), p.renderedSeen...)
}

func equalTraceSamples(got, want []int16) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

var _ runtimeSession.LiveService = (*traceHostProbe)(nil)
var _ runtimeSession.LiveRunner = (*traceHostProbe)(nil)
var _ runtimeRecording.Service = (*traceRecordingProbe)(nil)
var _ runtimeSession.LiveRecorder = (*traceRecorderProbe)(nil)
var _ runtimeDevices.Service = (*traceDeviceProbe)(nil)
var _ runtimeDevices.Handle = (*traceDeviceHandle)(nil)
var _ runtimeDevices.Capture = (*traceCaptureProbe)(nil)
var _ runtimeDevices.Playback = (*tracePlaybackProbe)(nil)
