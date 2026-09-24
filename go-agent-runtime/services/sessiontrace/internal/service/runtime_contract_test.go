package service

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

type runtimeContractObserver struct {
	mu           sync.Mutex
	observations []sessiontrace.SessionRuntimeObservation
}

func (o *runtimeContractObserver) ObserveSessionRuntime(observation sessiontrace.SessionRuntimeObservation) {
	o.mu.Lock()
	o.observations = append(o.observations, observation)
	o.mu.Unlock()
}

func (o *runtimeContractObserver) snapshot() []sessiontrace.SessionRuntimeObservation {
	o.mu.Lock()
	defer o.mu.Unlock()
	observations := make([]sessiontrace.SessionRuntimeObservation, len(o.observations))
	copy(observations, o.observations)
	return observations
}

func TestRuntimeRecorderPublicContractCopiesPayloadsAndPublishesEachBoundary(t *testing.T) {
	observer := &runtimeContractObserver{}
	recorder := NewRuntimeRecorder(observer, clock.Real{})
	if recorder == nil || recorder.ObservesProviderBoundaries() {
		t.Fatalf("recorder = %v, provider boundaries = %v", recorder, recorder.ObservesProviderBoundaries())
	}
	recorder.EnableProviderBoundaryObservations()
	if !recorder.ObservesProviderBoundaries() {
		t.Fatal("provider boundary observations were not enabled")
	}

	payload := []byte("payload")
	recorder.Observe(sessiontrace.SessionRuntimeObservationAudioInput, payload, 1, true, context.Canceled)
	payload[0] = 'X'
	recorder.ObserveWithInputCommit(sessiontrace.SessionRuntimeObservationInputCommit, []byte("commit"), 1, 2, true, errors.New("commit failed"))
	recorder.ObserveFinal(sessiontrace.SessionRuntimeObservationTerminal, nil, 2, 2, false, errors.New("terminal failed"), &sessiontrace.SessionFinalAccounting{PromptTokens: 1})

	message := messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, ResponseID: "response-1", ActorStreamID: "stream-1", LoopPassID: 4}
	recorder.AudioOutputMessage([]byte("audio"), message)
	recorder.AudioPlaybackReceipt(audio.PlaybackReceipt{CommandID: 7, Epoch: 3, Applied: false, Err: errors.New("stale")})
	recorder.AudioInput([]byte("input"))
	recorder.ProviderAudioSent([]byte("provider input"))
	recorder.InputCommit()
	recorder.ProviderInputCommit()
	recorder.ResponseCreate(message)
	recorder.TurnCompleted(2)
	recorder.TerminalWithAccounting(2, errors.New("run failed"), nil)
	recorder.TerminalWithAccounting(3, nil, nil)
	recorder.ObserveToolCall(messages.ToolCall{ID: "call-1", Name: "lookup", Arguments: `{}`})
	recorder.ObserveToolResult(messages.ToolCall{ID: "call-1", Name: "lookup"}, messages.ToolCallResponse{ToolCallID: "call-1", Name: "lookup", Content: "ok"}, false)

	observations := observer.snapshot()
	if len(observations) < 13 {
		t.Fatalf("observations = %d, want all recorder boundaries", len(observations))
	}
	if string(observations[0].Payload) != "payload" || observations[0].Error != "" {
		t.Fatalf("first observation = %+v, want copied payload and suppressed context error", observations[0])
	}
	if observations[0].Tick == 0 || observations[1].InputCommit != 2 || observations[1].Error != "commit failed" {
		t.Fatalf("observation metadata = %+v/%+v", observations[0], observations[1])
	}
	terminalCount := 0
	for _, observation := range observations {
		if observation.Kind == sessiontrace.SessionRuntimeObservationTerminal {
			terminalCount++
		}
	}
	if terminalCount != 2 {
		t.Fatalf("terminal observations = %d, want explicit and final boundary", terminalCount)
	}
}

type liveRecorderInner struct {
	messageErr error
	audioErr   error
	eventErr   error
	finalErr   error
}

func (i *liveRecorderInner) RecordMessage(context.Context, session.LiveRecord) error {
	return i.messageErr
}
func (i *liveRecorderInner) RecordAudio(context.Context, session.LiveAudioRecord) error {
	return i.audioErr
}
func (i *liveRecorderInner) RecordEvent(context.Context, session.LiveEvent) error { return i.eventErr }
func (i *liveRecorderInner) Finalize(context.Context, error) error                { return i.finalErr }

func TestLiveRecorderPublicContractPreservesInnerErrorsAndAddsTypedObservations(t *testing.T) {
	messageErr := errors.New("message recorder failed")
	audioErr := errors.New("audio recorder failed")
	eventErr := errors.New("event recorder failed")
	finalErr := errors.New("final recorder failed")
	observer := &runtimeContractObserver{}
	recorder := NewLiveRecorder(sessiontrace.LiveRecorderOptions{
		Inner:      &liveRecorderInner{messageErr: messageErr, audioErr: audioErr, eventErr: eventErr, finalErr: finalErr},
		Observer:   observer,
		InputRate:  16000,
		OutputRate: 24000,
	})
	message := messages.StreamMessage{Type: messages.StreamTypeResponseCreate, Role: messages.RoleAssistant, ResponseID: "response-2"}
	if err := recorder.RecordMessage(context.Background(), session.LiveRecord{Direction: session.LiveRecordClient, Timestamp: time.Now(), Message: message}); !errors.Is(err, messageErr) {
		t.Fatalf("RecordMessage error = %v, want inner error", err)
	}
	if err := recorder.RecordAudio(context.Background(), session.LiveAudioRecord{Direction: session.LiveRecordClient, Admission: session.LiveAudioQueueAdmitted, Frame: audio.PCMFrame{Samples: []int16{1, 2, 3}, StreamID: "stream-2", Epoch: 4}}); !errors.Is(err, audioErr) {
		t.Fatalf("RecordAudio error = %v, want inner error", err)
	}
	if err := recorder.RecordAudio(context.Background(), session.LiveAudioRecord{Direction: session.LiveRecordAgent, Admission: session.LiveAudioMediaBridged, Frame: audio.PCMFrame{Samples: []int16{4}, Format: audio.DeviceFormat{SampleRate: 24000}, Epoch: 5}}); !errors.Is(err, audioErr) {
		t.Fatalf("second RecordAudio error = %v, want inner error", err)
	}
	if err := recorder.RecordEvent(context.Background(), session.LiveEvent{Kind: "provider_event", Timestamp: time.Now(), ResponseID: "response-2", Error: errors.New("event payload")}); !errors.Is(err, eventErr) {
		t.Fatalf("RecordEvent error = %v, want inner error", err)
	}
	if err := recorder.RecordEvent(context.Background(), session.LiveEvent{Kind: string(session.LiveEventTerminal)}); !errors.Is(err, eventErr) {
		t.Fatalf("terminal RecordEvent error = %v, want inner error", err)
	}
	if err := recorder.Finalize(context.Background(), errors.New("run failed")); !errors.Is(err, finalErr) {
		t.Fatalf("Finalize error = %v, want inner error", err)
	}
	if len(observer.snapshot()) < 5 {
		t.Fatal("live recorder did not publish message, audio, event, and terminal observations")
	}
}

type playbackContractSink struct {
	records []sessiontrace.DiagnosticRecord
}

func (s *playbackContractSink) RecordSessionDiagnostic(record sessiontrace.DiagnosticRecord) {
	s.records = append(s.records, record)
}

func TestPlaybackDiagnosticsPublicContractFansOutQueueAndReceiptObservations(t *testing.T) {
	sink := &playbackContractSink{}
	runtimeObserver := &runtimeContractObserver{}
	diagnostics := NewPlaybackDiagnostics(sessiontrace.PlaybackDiagnosticsOptions{
		Sink:    sink,
		Runtime: NewRuntimeRecorder(runtimeObserver, clock.Real{}),
	})
	playbackCalls := 0
	playbackObserver := diagnostics.PlaybackObserver(func(devicegw.DeviceID, audio.PlaybackQueueStats) { playbackCalls++ })
	playbackObserver(devicegw.DeviceID("virtual:output"), audio.PlaybackQueueStats{
		Format: audio.DeviceFormat{SampleRate: 16000, Channels: 1}, DroppedSamples: 2, OverflowEvents: 1,
	})
	if playbackCalls != 1 || len(sink.records) != 1 {
		t.Fatalf("playback fanout = callbacks %d, diagnostics %d; want one each", playbackCalls, len(sink.records))
	}
	receiptObserver := diagnostics.PlaybackReceiptObserver(nil)
	receiptObserver(audio.PlaybackReceipt{CommandID: 7, Epoch: 2, Applied: true})
	existingReceiptCalls := 0
	withoutRuntime := NewPlaybackDiagnostics(sessiontrace.PlaybackDiagnosticsOptions{}).PlaybackReceiptObserver(func(audio.PlaybackReceipt) {
		existingReceiptCalls++
	})
	withoutRuntime(audio.PlaybackReceipt{CommandID: 8, Epoch: 3, Applied: false})
	if existingReceiptCalls != 1 {
		t.Fatalf("optional runtime changed the existing receipt observer: calls=%d", existingReceiptCalls)
	}
	captureObserver := diagnostics.CaptureObserver(nil)
	captureObserver(devicegw.DeviceID("virtual:input"), audio.CaptureQueueStats{CapturedSamples: 4, DroppedSamples: 1, DropPolicy: "drop_oldest"})
	diagnostics.RecordParticipantPlaybackOverflow("participant-1", nil)
	if len(runtimeObserver.snapshot()) != 1 {
		t.Fatalf("receipt observations = %d, want one", len(runtimeObserver.snapshot()))
	}
}

func TestPlaybackDiagnosticsPublicContractReportsParticipantQueueOverflow(t *testing.T) {
	const rate = 24000
	capability := devicegw.VirtualCapability{SampleRate: rate, Channels: 1, BitDepth: 16, Format: audio.DeviceEncodingPCM16}
	registry, err := devicegw.NewVirtualRegistry(devicegw.VirtualBackendConfig{
		Devices: []devicegw.VirtualDeviceConfig{
			{ID: "input", Name: "Input", Direction: devicegw.DirectionInput, Capabilities: []devicegw.VirtualCapability{capability}, LoopbackID: "output"},
			{ID: "output", Name: "Output", Direction: devicegw.DirectionOutput, Capabilities: []devicegw.VirtualCapability{capability}, LoopbackID: "input"},
		},
		Defaults: map[devicegw.Direction]string{devicegw.DirectionInput: "input", devicegw.DirectionOutput: "output"},
	})
	if err != nil {
		t.Fatalf("NewVirtualRegistry: %v", err)
	}
	sinkDevice, err := devicegw.NewDeviceSinkAtRate(registry, "virtual:output", rate)
	if err != nil {
		t.Fatalf("NewDeviceSinkAtRate: %v", err)
	}
	t.Cleanup(func() { _ = sinkDevice.Close() })
	capacity := sinkDevice.PlaybackStats().CapacitySamples
	if err := sinkDevice.WriteSamples(context.Background(), make([]int16, capacity+audio.FrameSize)); err != nil {
		t.Fatalf("WriteSamples: %v", err)
	}

	sink := &playbackContractSink{}
	diagnostics := NewPlaybackDiagnostics(sessiontrace.PlaybackDiagnosticsOptions{Sink: sink})
	diagnostics.RecordParticipantPlaybackOverflow("participant-7", sinkDevice)
	if len(sink.records) != 1 || sink.records[0].Event != SessionDiagnosticEventPlaybackOverflow {
		t.Fatalf("participant overflow diagnostics = %+v, want one playback overflow", sink.records)
	}
	fields := sink.records[0].Fields
	if fields[SessionDiagnosticFieldPlaybackParticipantID] != "participant-7" || fields[SessionDiagnosticFieldPlaybackDroppedSamples] == "0" || fields[SessionDiagnosticFieldPlaybackOverflowEvents] == "0" {
		t.Fatalf("participant overflow fields = %+v", fields)
	}
}

func TestLivenessClockAndTraceObserverPoliciesUseServiceContracts(t *testing.T) {
	if liveness := LivenessClockFromSource(clock.Real{}); liveness == nil || liveness.NewTimer(time.Hour) == nil {
		t.Fatal("real clock did not provide a liveness timer")
	}
	if LivenessClockFromSource(sourceOnlyClock{}) != nil {
		t.Fatal("source-only clock unexpectedly provided a liveness timer")
	}
	observer := traceObserver{}
	if !observer.ObserveProviderBoundaries() || observer.RetainCommitPayload() {
		t.Fatal("trace observer policy contract changed")
	}
}

type sourceOnlyClock struct{}

func (sourceOnlyClock) Now() time.Time { return time.Unix(0, 0) }

func TestTraceDeviceServiceBindsCaptureAndPlaybackObservers(t *testing.T) {
	var preGate, uploaded, rendered []int16
	playbackSamples := []int16(nil)
	capture := &traceContractCapture{}
	playback := &traceContractPlayback{renderedSupported: true}
	innerHandle := &traceContractHandle{ports: runtimeDevices.MediaPorts{Capture: capture, Playback: playback}}
	inner := &traceContractDeviceService{handle: innerHandle}
	service := wrapDeviceService(inner, sessiontrace.DeviceBinding{
		PreGateSamplesObserver: func(_ int, samples []int16) { preGate = append(preGate, samples...) },
		UploadedSamplesObserver: func(_ int, samples []int16) {
			uploaded = append(uploaded, samples...)
		},
		PlaybackSamplesObserver: func(_ context.Context, _ int, samples []int16) error {
			playbackSamples = append(playbackSamples, samples...)
			return nil
		},
		RenderedSamplesObserver: func(_ int, samples []int16) { rendered = append(rendered, samples...) },
	})

	handle, err := service.Open(context.Background(), runtimeDevices.Request{CaptureEnabled: true, PlaybackEnabled: true, SampleRate: 24_000})
	if err != nil {
		t.Fatal(err)
	}
	ports := handle.Media()
	if ports.Capture == capture || capture.preGate == nil || playback.playbackObserver == nil || playback.renderedObserver == nil {
		t.Fatalf("trace bindings were not installed: capture=%T pre-gate=%v playback=%v rendered=%v", ports.Capture, capture.preGate != nil, playback.playbackObserver != nil, playback.renderedObserver != nil)
	}
	capture.preGate(16_000, []int16{1, 2})
	outbound := &traceContractOutbound{}
	if err := ports.Capture.Pump(context.Background(), outbound); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(preGate, []int16{1, 2}) || !reflect.DeepEqual(uploaded, []int16{3, 4}) ||
		len(outbound.frames) != 1 || !reflect.DeepEqual(outbound.frames[0].Samples, []int16{3, 4}) {
		t.Fatalf("capture=%v uploaded=%v outbound=%#v", preGate, uploaded, outbound.frames)
	}
	playbackErr := playback.playbackObserver(context.Background(), 24_000, []int16{5, 6})
	playback.renderedObserver(16_000, []int16{7, 8})
	if playbackErr != nil || !reflect.DeepEqual(playbackSamples, []int16{5, 6}) || !reflect.DeepEqual(rendered, []int16{7, 8}) {
		t.Fatalf("playback observations = enqueued %v rendered %v", playbackSamples, rendered)
	}
	if err := handle.Close(); err != nil || innerHandle.closeCount != 1 {
		t.Fatalf("handle close = %v count = %d", err, innerHandle.closeCount)
	}
}

func TestTraceDeviceServiceUsesUploadedCaptureAndReportsUnavailablePlayback(t *testing.T) {
	var unavailable, uploaded []int16
	capture := &traceContractUploadedCapture{}
	playback := &traceContractBarePlayback{}
	innerHandle := &traceContractHandle{ports: runtimeDevices.MediaPorts{Capture: capture, Playback: playback}}
	inner := &traceContractDeviceService{handle: innerHandle}
	service := wrapDeviceService(inner, sessiontrace.DeviceBinding{
		UploadedSamplesObserver:    func(_ int, samples []int16) { uploaded = append(uploaded, samples...) },
		RenderedSamplesUnavailable: func() { unavailable = append(unavailable, 1) },
	})
	handle, err := service.Open(context.Background(), runtimeDevices.Request{CaptureEnabled: true, PlaybackEnabled: true, RemoteEndpoint: "not-an-endpoint"})
	if err != nil {
		t.Fatal(err)
	}
	if handle != innerHandle || handle.Media().Capture != capture || capture.uploadedObserver == nil {
		t.Fatal("uploaded-capture path did not preserve the inner handle")
	}
	capture.uploadedObserver(24_000, []int16{9, 10})
	if !reflect.DeepEqual(uploaded, []int16{9, 10}) || len(unavailable) != 1 {
		t.Fatalf("uploaded=%v unavailable=%v", uploaded, unavailable)
	}
}

func TestTraceDeviceServiceForwardsRTCBinding(t *testing.T) {
	wantErr := errors.New("rtc binding failed")
	inner := &traceContractDeviceService{bindErr: wantErr}
	wrapper := traceDeviceService{inner: inner}
	_, forwardedErr := wrapper.BindRTC(context.Background(), runtimeDevices.RTCBindingRequest{})
	_, unavailableErr := (traceDeviceService{}).BindRTC(context.Background(), runtimeDevices.RTCBindingRequest{})
	if !errors.Is(forwardedErr, wantErr) || !errors.Is(unavailableErr, runtimeDevices.ErrUnavailable) {
		t.Fatalf("RTC binding errors = forwarded:%v unavailable:%v", forwardedErr, unavailableErr)
	}
}

func TestTraceDeviceAdaptersRejectInvalidInputs(t *testing.T) {
	wantErr := errors.New("open failed")
	if _, err := (traceDeviceService{}).Open(context.Background(), runtimeDevices.Request{}); !errors.Is(err, runtimeDevices.ErrUnavailable) {
		t.Fatalf("nil inner Open error = %v", err)
	}
	if _, err := (traceDeviceService{inner: &traceContractDeviceService{openErr: wantErr}}).Open(context.Background(), runtimeDevices.Request{}); !errors.Is(err, wantErr) {
		t.Fatalf("inner Open error = %v", err)
	}
	if err := (&traceCapture{}).Pump(context.Background(), &traceContractOutbound{}); !errors.Is(err, runtimeDevices.ErrUnavailable) {
		t.Fatalf("nil capture error = %v", err)
	}
	if err := (&traceCapture{inner: &traceContractCapture{}}).Pump(context.Background(), nil); !errors.Is(err, runtimeDevices.ErrInvalidRequest) {
		t.Fatalf("nil outbound error = %v", err)
	}
	if err := (&traceCaptureOutbound{}).WriteFrame(context.Background(), audio.PCMFrame{}); !errors.Is(err, runtimeDevices.ErrInvalidRequest) {
		t.Fatalf("nil outbound target error = %v", err)
	}
}

func TestTraceSourceAdaptersPreserveSamples(t *testing.T) {
	var observed []struct {
		rate    int
		samples []int16
	}
	observe := func(rate int, samples []int16) {
		observed = append(observed, struct {
			rate    int
			samples []int16
		}{rate, append([]int16(nil), samples...)})
	}
	source := &traceContractSource{samples: []int16{11, 12}}
	wrapped := wrapAudioSource(source, 0, observe)
	buf := make([]int16, 2)
	if err := wrapped.ReadFrame(context.Background(), buf); err != nil {
		t.Fatal(err)
	}
	if err := wrapped.Close(); err != nil {
		t.Fatal(err)
	}
	sampleSource := &traceContractSampleSource{traceContractSource: traceContractSource{samples: []int16{13, 14}}}
	wrappedSample := wrapAudioSource(sampleSource, 22_050, observe)
	sampleWrapper, ok := wrappedSample.(audio.SampleSource)
	if !ok {
		t.Fatal("sample source wrapper lost SampleSource contract")
	}
	if err := sampleWrapper.ReadFrame(context.Background(), make([]int16, 2)); err != nil {
		t.Fatal(err)
	}
	count, err := sampleWrapper.ReadSamples(context.Background(), make([]int16, 2))
	if err != nil || count != 2 {
		t.Fatalf("ReadSamples = %d, %v", count, err)
	}
	if err := wrappedSample.Close(); err != nil {
		t.Fatal(err)
	}
	if len(observed) != 3 || observed[0].rate != audio.SampleRate || observed[1].rate != 22_050 || observed[2].rate != 22_050 {
		t.Fatalf("source observations = %#v", observed)
	}
	if wrapAudioSource(nil, 0, observe) != nil || wrapAudioSource(source, 0, nil) != source {
		t.Fatal("nil source wrapper policy changed")
	}
	if err := (&traceAudioSource{}).ReadFrame(context.Background(), buf); !errors.Is(err, io.EOF) {
		t.Fatalf("nil audio source error = %v", err)
	}
	if err := (&traceSampleSource{}).ReadFrame(context.Background(), buf); !errors.Is(err, io.EOF) {
		t.Fatalf("nil sample source frame error = %v", err)
	}
	if _, err := (&traceSampleSource{}).ReadSamples(context.Background(), buf); !errors.Is(err, io.EOF) {
		t.Fatalf("nil sample source samples error = %v", err)
	}
}

func TestTracePreparedPublicAdaptersDelegateToServiceOwnedState(t *testing.T) {
	prepared, err := New().Prepare(sessiontrace.Request{
		TraceAudio:      true,
		RecordDirectory: filepath.Join(t.TempDir(), "requested"),
		Clock:           clock.Real{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.WrapLiveRecorder(&liveRecorderInner{}, session.LiveRequest{InputAudioSampleRate: 24_000, OutputAudioSampleRate: 16_000}) == nil ||
		prepared.WrapDeviceService(&traceContractDeviceService{handle: &traceContractHandle{}}) == nil ||
		prepared.WrapAudioSource(&traceContractSource{samples: []int16{1}}, 0) == nil {
		t.Fatal("prepared adapter returned nil")
	}
}

func TestTraceDeviceServiceCoversOptionalCapabilitiesAndHandleLifecycle(t *testing.T) {
	legacyPlayback := &traceContractLegacyPlayback{}
	innerHandle := &traceContractHandle{ports: runtimeDevices.MediaPorts{Playback: legacyPlayback}}
	inner := &traceContractDeviceService{handle: innerHandle}
	var rendered []int16
	service := wrapDeviceService(inner, sessiontrace.DeviceBinding{
		RenderedSamplesObserver: func(_ int, samples []int16) { rendered = append(rendered, samples...) },
	})
	handle, err := service.Open(context.Background(), runtimeDevices.Request{PlaybackEnabled: true, SampleRate: 16_000})
	if err != nil {
		t.Fatal(err)
	}
	if legacyPlayback.renderedObserver == nil {
		t.Fatal("legacy rendered observer was not installed")
	}
	legacyPlayback.renderedObserver(16_000, []int16{21, 22})
	if !reflect.DeepEqual(rendered, []int16{21, 22}) {
		t.Fatalf("legacy rendered samples = %v", rendered)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}

	monitor := &remoteRenderMonitor{endpoint: "not-an-endpoint", observer: func(int, []int16) {}, done: make(chan struct{})}
	monitorHandle := &traceDeviceHandle{inner: &traceContractHandle{}, monitor: monitor}
	monitor.Start()
	if err := monitorHandle.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteRenderMonitorCapturesRenderedDeviceSamples(t *testing.T) {
	registry, err := devicegw.NewSimulatedDuplexRegistry(devicegw.DuplexScenario{
		Render:  devicegw.ClockSpec{NominalRate: 16_000, Quanta: []int{audio.FrameSize}},
		Capture: devicegw.ClockSpec{NominalRate: 16_000, Quanta: []int{audio.FrameSize}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := devicegw.NewDeviceServer(registry)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := server.Close(); closeErr != nil {
			t.Errorf("close device server: %v", closeErr)
		}
	}()
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	endpoint := strings.TrimPrefix(httpServer.URL, "http://")

	var observed []struct {
		rate    int
		samples []int16
	}
	monitor, err := newRemoteRenderMonitor(context.Background(), runtimeDevices.Request{RemoteEndpoint: endpoint, SampleRate: 16_000}, &traceContractBarePlayback{}, func(rate int, samples []int16) {
		observed = append(observed, struct {
			rate    int
			samples []int16
		}{rate, append([]int16(nil), samples...)})
	})
	if err != nil {
		t.Fatal(err)
	}
	remote, err := devicegw.NewRemoteDeviceRegistry(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	sink, err := devicegw.NewDeviceSinkAtRate(remote, "simulated-duplex:output", 16_000)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := sink.Close(); closeErr != nil {
			t.Errorf("close device sink: %v", closeErr)
		}
	}()
	frame := make([]int16, audio.FrameSize)
	copy(frame, []int16{31, 32, 33})
	if err := sink.WriteFrame(context.Background(), frame); err != nil {
		t.Fatal(err)
	}
	if err := devicegw.AdvanceRemoteDeviceServer(context.Background(), endpoint, 1); err != nil {
		t.Fatal(err)
	}
	if err := sink.WaitForPlayback(context.Background()); err != nil {
		t.Fatal(err)
	}
	monitor.poll(context.Background())
	if len(observed) != 1 || observed[0].rate != 24_000 || len(observed[0].samples) < 3 || !reflect.DeepEqual(observed[0].samples[:3], []int16{31, 32, 33}) {
		t.Fatalf("render observations = %#v", observed)
	}
}

type traceContractDeviceService struct {
	handle  runtimeDevices.Handle
	openErr error
	bindErr error
}

func (s *traceContractDeviceService) Open(_ context.Context, _ runtimeDevices.Request) (runtimeDevices.Handle, error) {
	return s.handle, s.openErr
}
func (s *traceContractDeviceService) BindRTC(context.Context, runtimeDevices.RTCBindingRequest) (runtimeDevices.RTCBinding, error) {
	if s.bindErr != nil {
		return nil, s.bindErr
	}
	return nil, runtimeDevices.ErrUnavailable
}

type traceContractHandle struct {
	ports      runtimeDevices.MediaPorts
	closeCount int
}

func (h *traceContractHandle) Media() runtimeDevices.MediaPorts { return h.ports }
func (h *traceContractHandle) Close() error                     { h.closeCount++; return nil }

type traceContractCapture struct {
	preGate    func(int, []int16)
	closeCount int
}

func (c *traceContractCapture) Pump(ctx context.Context, outbound audio.OutboundMedia) error {
	return outbound.WriteFrame(ctx, audio.PCMFrame{Samples: []int16{3, 4}, Format: audio.PCM16DeviceFormat(24_000)})
}
func (c *traceContractCapture) Close() error { c.closeCount++; return nil }
func (c *traceContractCapture) SetPreGateSamplesObserver(observer func(int, []int16)) {
	c.preGate = observer
}

type traceContractUploadedCapture struct {
	traceContractCapture
	uploadedObserver func(int, []int16)
}

func (c *traceContractUploadedCapture) SetUploadedSamplesObserver(observer func(int, []int16)) {
	c.uploadedObserver = observer
}

type traceContractOutbound struct{ frames []audio.PCMFrame }

func (o *traceContractOutbound) WriteFrame(_ context.Context, frame audio.PCMFrame) error {
	frame.Samples = append([]int16(nil), frame.Samples...)
	o.frames = append(o.frames, frame)
	return nil
}
func (*traceContractOutbound) Close() error { return nil }

type traceContractPlayback struct {
	renderedSupported bool
	playbackObserver  func(context.Context, int, []int16) error
	renderedObserver  func(int, []int16)
}

func (*traceContractPlayback) Pump(context.Context, audio.InboundMedia) error { return nil }
func (*traceContractPlayback) Close() error                                   { return nil }
func (p *traceContractPlayback) SetPlaybackSamplesObserver(observer func(context.Context, int, []int16) error) {
	p.playbackObserver = observer
}
func (p *traceContractPlayback) SetRenderedSamplesObserver(observer func(int, []int16)) bool {
	p.renderedObserver = observer
	return p.renderedSupported
}

type traceContractLegacyPlayback struct{ renderedObserver func(int, []int16) }

func (traceContractLegacyPlayback) Pump(context.Context, audio.InboundMedia) error { return nil }
func (traceContractLegacyPlayback) Close() error                                   { return nil }
func (p *traceContractLegacyPlayback) SetPlaybackRenderObserver(observer audio.PlaybackRenderObserver) {
	p.renderedObserver = func(rate int, samples []int16) { observer(rate, samples) }
}

type traceContractBarePlayback struct{}

func (traceContractBarePlayback) Pump(context.Context, audio.InboundMedia) error { return nil }
func (traceContractBarePlayback) Close() error                                   { return nil }
func (traceContractBarePlayback) DeviceSampleRate() int                          { return 24_000 }

type traceContractSource struct{ samples []int16 }

func (s *traceContractSource) ReadFrame(_ context.Context, buf []int16) error {
	copy(buf, s.samples)
	return nil
}
func (*traceContractSource) Close() error { return nil }

type traceContractSampleSource struct{ traceContractSource }

func (s *traceContractSampleSource) ReadSamples(_ context.Context, buf []int16) (int, error) {
	return copy(buf, s.samples), nil
}
