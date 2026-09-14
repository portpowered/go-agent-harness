package agentruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	sessiontracewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/observability"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func TestResolveSessionRuntimeSelection_DefaultsToWebSocket(t *testing.T) {
	selection, err := resolveSessionRuntimeSelection(SessionRunOptions{ModelCatalog: testModelCatalog()})
	if err != nil {
		t.Fatalf("resolveSessionRuntimeSelection: %v", err)
	}
	if selection != (SessionRuntimeSelection{Transport: SessionTransportWebSocket}) {
		t.Fatalf("selection = %#v, want the WebSocket default", selection)
	}
}

func TestPlanSessionRuntime_RetainsExactWebRTCSelection(t *testing.T) {
	const signaling = " loopback://sentinel/signaling?token=exact "
	const media = "rtsp://fixture:secret@sentinel.example/camera/main"

	plan, err := planSessionRuntimeWithFactory(SessionRunOptions{ModelCatalog: testModelCatalog(),
		ReplayPath:        "synthetic.session.json",
		SessionInferencer: &selectionTestInferencer{},
		Transport:         " WebRTC ",
		SignalingEndpoint: signaling,
		MediaSource:       media,
	}, sessionRuntimeFactory{})
	if err != nil {
		t.Fatalf("planSessionRuntimeWithFactory: %v", err)
	}
	if plan.transport != SessionTransportWebRTC {
		t.Fatalf("plan.transport = %q, want %q", plan.transport, SessionTransportWebRTC)
	}
	if plan.signalingEndpoint != signaling {
		t.Fatalf("plan.signalingEndpoint = %q, want exact %q", plan.signalingEndpoint, signaling)
	}
	if plan.mediaSource != media {
		t.Fatalf("plan.mediaSource = %q, want exact input", plan.mediaSource)
	}
	if plan.selection != (SessionRuntimeSelection{
		Transport:         SessionTransportWebRTC,
		SignalingEndpoint: signaling,
		MediaSource:       media,
	}) {
		t.Fatalf("plan.selection = %#v, want exact selection", plan.selection)
	}
}

func TestPlanSessionRuntime_InvalidSelectionFailsBeforeFactorySideEffects(t *testing.T) {
	cases := []struct {
		name   string
		opts   SessionRunOptions
		fields []string
		cause  error
	}{
		{
			name:   "unknown transport",
			opts:   SessionRunOptions{ModelCatalog: testModelCatalog(), Transport: "quic", RecordPath: filepath.Join(t.TempDir(), "capture.json")},
			fields: []string{"transport"},
			cause:  ErrInvalidSessionTransport,
		},
		{
			name:   "signaling on websocket",
			opts:   SessionRunOptions{ModelCatalog: testModelCatalog(), Signaling: "loopback", RecordPath: filepath.Join(t.TempDir(), "capture.json")},
			fields: []string{"transport", "signaling"},
			cause:  ErrSessionSignalingRequiresWebRTC,
		},
		{
			name:   "media on websocket",
			opts:   SessionRunOptions{ModelCatalog: testModelCatalog(), MediaSource: "fixture", RecordPath: filepath.Join(t.TempDir(), "capture.json")},
			fields: []string{"transport", "media-source"},
			cause:  ErrSessionMediaSourceRequiresWebRTC,
		},
		{
			name:   "webrtc without signaling or media",
			opts:   SessionRunOptions{ModelCatalog: testModelCatalog(), Transport: SessionTransportWebRTC, RecordPath: filepath.Join(t.TempDir(), "capture.json")},
			fields: []string{"transport", "signaling", "media-source"},
			cause:  ErrSessionWebRTCRequiresSignaling,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			factoryCalls := 0
			factory := sessionRuntimeFactory{
				newDefaultLiveDialer: func() transport.Dialer {
					factoryCalls++
					return nil
				},
				newRecordingDialer: func(transport.Dialer, string, string) sessionRecordingDialer {
					factoryCalls++
					return nil
				},
				newReplayDialer: func(string) (sessionReplayDialer, error) {
					factoryCalls++
					return nil, nil
				},
				newReplayInferencer: func(string) messages.SessionInferencer {
					factoryCalls++
					return nil
				},
			}

			_, err := planSessionRuntimeWithFactory(testCase.opts, factory)
			if err == nil {
				t.Fatal("invalid runtime selection returned nil")
			}
			var selectionErr *SessionRuntimeSelectionError
			if !errors.As(err, &selectionErr) {
				t.Fatalf("error type = %T, want *SessionRuntimeSelectionError: %v", err, err)
			}
			for _, field := range testCase.fields {
				found := false
				for _, got := range selectionErr.Fields {
					if got == field {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("selection fields = %v, missing %q", selectionErr.Fields, field)
				}
			}
			if !errors.Is(err, testCase.cause) {
				t.Fatalf("error = %v, want errors.Is(..., %v)", err, testCase.cause)
			}
			if factoryCalls != 0 {
				t.Fatalf("factory calls = %d, want 0 before selection rejection", factoryCalls)
			}
		})
	}
}

func TestRunSession_InvalidRTCSelectionDoesNotMutateCapturePath(t *testing.T) {
	recordPath := filepath.Join(t.TempDir(), "rejected.session.json")
	err := RunSession(context.Background(), os.Stdout, SessionRunOptions{ModelCatalog: testModelCatalog(),
		RecordPath: recordPath,
		Transport:  SessionTransportWebRTC,
		Signaling:  "loopback",
	})
	if err == nil {
		t.Fatal("invalid WebRTC selection returned nil")
	}
	if _, statErr := os.Stat(recordPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("rejected selection touched capture path: stat error = %v", statErr)
	}
}

type selectionTestInferencer struct{}

func (*selectionTestInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return nil, errors.New("selection test inferencer must not connect")
}

func TestSessionTraceObserverContractThroughServiceWire(t *testing.T) {
	t.Run("stream", testSessionTraceObserverStream)
	t.Run("tool continuation", testSessionTraceObserverToolContinuation)
	t.Run("scheduled audio", testSessionTraceObserverScheduledAudio)
	t.Run("failure and cancellation", testSessionTraceObserverFailureAndCancellation)
}

func testSessionTraceObserverStream(t *testing.T) {
	sink := &diagnosticRecordSink{}
	observer := sessiontracewire.NewObserver(sessiontrace.NewObserverOptions{
		Sink: sink, Provider: "caller-provider", Model: "caller-model", TerminalService: wire.NewService(),
	})
	observer.NoteUserTextInput("hello")
	observer.Observe(messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("caller-session", "audio")})
	observeCallerAssistantText(observer, "response-1", "world")
	end := messages.NewMessageEndValue(messages.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5})
	end.Status = "completed"
	end.TerminalReason = messages.TerminalReasonProviderAuthoredCompletion
	end.TerminalProvenance = messages.TerminalProvenanceProvider
	end.OutputState = messages.TerminalOutputComplete
	observer.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "response-1", Value: end})
	state := observer.State()
	if state.Provider != "caller-provider" || state.Model != "caller-model" || state.TurnsCompleted != 1 || state.InputTextBytes != 5 || state.OutputTextBytes != 5 {
		t.Fatalf("service-owned observer state = %+v", state)
	}
	if err := observer.Finish(nil); err != nil {
		t.Fatalf("observer Finish: %v", err)
	}
	if got := len(sink.events(SessionDiagnosticEventTurn)); got != 1 {
		t.Fatalf("turn records = %d, want one", got)
	}
	if got := len(sink.events(SessionDiagnosticEventMetrics)); got != 1 {
		t.Fatalf("metrics records = %d, want one", got)
	}
}

func observeCallerAssistantText(observer sessiontrace.Observer, responseID, text string) {
	observer.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageStartValue()})
	observer.Observe(messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewTextStartValue()})
	observer.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewTextDeltaValue(text)})
	observer.Observe(messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewTextEndValue()})
}

func testSessionTraceObserverToolContinuation(t *testing.T) {
	observer := sessiontracewire.NewObserver(sessiontrace.NewObserverOptions{TerminalService: wire.NewService()})
	observer.SetToolResultsEnabled(true)
	observer.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "tool-response", Value: messages.NewMessageStartValue()})
	observer.Observe(messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ResponseID: "tool-response", Value: messages.NewToolCallEndValue("call-1", "lookup", `{}`)})
	observer.NoteToolResultAccepted("call-1")
	observer.NoteToolContinuationRequestedFor("call-1")
	if !observer.HasPendingToolContinuations() {
		t.Fatal("accepted tool result did not retain its continuation obligation")
	}
	observer.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "tool-response", Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	observeCallerAssistantText(observer, "tool-continuation", "ok")
	observer.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "tool-continuation", Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	if observer.HasPendingToolContinuations() || observer.HasUnresolvedToolCalls() {
		t.Fatal("tool continuation obligation was not retired by its response")
	}
}

func testSessionTraceObserverScheduledAudio(t *testing.T) {
	observer := sessiontracewire.NewObserver(sessiontrace.NewObserverOptions{TerminalService: wire.NewService()})
	observer.ScheduleAudioInputs([]sessiontrace.ScheduledAudioInput{{AfterCompletedTurns: 0, PCM: []byte{1}, EndOfTurn: true}})
	if !observer.ScheduledAudioReady() || observer.ScheduledAudioComplete() {
		t.Fatal("scheduled observer reported an invalid initial readiness state")
	}
	if err := observer.DispatchScheduledInputs(context.Background(), serviceContractScheduledSender{}); err != nil {
		t.Fatalf("scheduled DispatchScheduledInputs: %v", err)
	}
	observer.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "scheduled-response", Value: messages.NewMessageStartValue()})
	observer.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: "scheduled-response", Value: messages.NewTextDeltaValue("scheduled")})
	observer.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "scheduled-response", Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	if !observer.ScheduledAudioComplete() || observer.ScheduledAudioIncomplete() {
		t.Fatal("scheduled observer did not retire its dispatched response")
	}
}

func testSessionTraceObserverFailureAndCancellation(t *testing.T) {
	sink := &diagnosticRecordSink{}
	observer := sessiontracewire.NewObserver(sessiontrace.NewObserverOptions{Sink: sink, TerminalService: wire.NewService()})
	observer.Observe(messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("failure-session", "audio")})
	observer.Observe(messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValueWithTerminal("provider failed", "provider_error", messages.TerminalReasonTerminalFailure, messages.TerminalProvenanceProvider, messages.TerminalOutputNone)})
	if observer.State().Failure == nil {
		t.Fatal("service-owned failure was not retained")
	}
	if err := observer.Finish(errors.New("provider failed")); err == nil || len(sink.events(SessionDiagnosticEventFailure)) != 1 {
		t.Fatalf("failure Finish = %v, records = %d", err, len(sink.events(SessionDiagnosticEventFailure)))
	}
	cancelSink := &diagnosticRecordSink{}
	cancelIntent := sessiontracewire.NewCancellationIntent()
	cancelObserver := sessiontracewire.NewObserver(sessiontrace.NewObserverOptions{Sink: cancelSink, TerminalService: wire.NewService(), CancellationIntent: cancelIntent})
	cancelIntent.MarkSIGINT()
	if !cancelObserver.CancellationClean(context.Canceled) {
		t.Fatal("SIGINT cancellation was not clean")
	}
	if err := cancelObserver.Finish(context.Canceled); err != nil || len(cancelSink.events(SessionDiagnosticEventTerminal)) != 1 {
		t.Fatalf("cancellation Finish = %v, terminal records = %d", err, len(cancelSink.events(SessionDiagnosticEventTerminal)))
	}
}

func TestSessionTraceRuntimeAdaptersThroughServiceWire(t *testing.T) {
	t.Run("runtime recorder", testSessionTraceRuntimeRecorder)
	t.Run("live recorder", testSessionTraceLiveRecorder)
	t.Run("playback and trace service", testSessionTracePlaybackAndService)
	t.Run("metrics and provider wire", testSessionTraceMetricsAndProviderWire)
}

func testSessionTraceRuntimeRecorder(t *testing.T) {
	observer := &recordingSessionRuntimeObserver{}
	recorder := sessiontracewire.NewRuntimeRecorder(observer, clock.Real{})
	if recorder == nil {
		t.Fatal("NewRuntimeRecorder returned nil")
	}
	recorder.EnableProviderBoundaryObservations()
	recorder.Observe(sessiontrace.SessionRuntimeObservationAudioInput, []byte("input"), 0, true, context.Canceled)
	recorder.ObserveWithInputCommit(sessiontrace.SessionRuntimeObservationInputCommit, []byte("commit"), 0, 1, true, errors.New("commit failed"))
	recorder.ObserveFinal(sessiontrace.SessionRuntimeObservationTerminal, []byte("final"), 1, 1, false, errors.New("final failed"), &sessiontrace.SessionFinalAccounting{
		PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3,
		Metrics: metrics.Snapshot{HistogramBounds: []int64{1, 2}, Series: []metrics.SeriesSnapshot{{Histogram: metrics.HistogramSnapshot{Bounds: []int64{1}, BucketCounts: []uint64{2}, SampleCount: 1}}}},
	})
	recorder.AudioOutputMessage([]byte("audio"), messages.StreamMessage{Type: messages.StreamTypeAudioDelta, ResponseID: "response-2", ActorStreamID: "stream-2", LoopPassID: 3})
	recorder.AudioPlaybackReceipt(audio.PlaybackReceipt{CommandID: 9, Epoch: 2, Applied: false, Err: errors.New("stale")})
	recorder.AudioInput([]byte("mic"))
	recorder.ProviderAudioSent([]byte("provider-mic"))
	recorder.InputCommit()
	recorder.ProviderInputCommit()
	recorder.ResponseCreate(messages.StreamMessage{Type: messages.StreamTypeResponseCreate, ResponseID: "response-2"})
	recorder.TurnCompleted(1)
	recorder.TerminalWithAccounting(1, errors.New("terminal"), nil)
	recorder.ObserveToolCall(messages.ToolCall{ID: "call-2", Name: "lookup", Arguments: `{}`})
	recorder.ObserveToolResult(messages.ToolCall{ID: "call-2", Name: "lookup"}, messages.ToolCallResponse{ToolCallID: "call-2", Content: "ok"}, false)
	if len(observer.observations) < 10 {
		t.Fatalf("runtime observations = %d, want each boundary", len(observer.observations))
	}
}

func testSessionTraceLiveRecorder(t *testing.T) {
	observer := &recordingSessionRuntimeObserver{}
	live := sessiontracewire.NewLiveRecorder(sessiontrace.LiveRecorderOptions{Observer: observer, InputRate: 16000, OutputRate: 24000})
	if err := live.RecordMessage(context.Background(), session.LiveRecord{Direction: session.LiveRecordAgent, Message: messages.StreamMessage{Type: messages.StreamTypeResponseCreate}}); err != nil {
		t.Fatalf("live RecordMessage: %v", err)
	}
	for _, messageType := range []messages.StreamMessageType{messages.StreamTypeInputItemAdded, messages.StreamTypeMessageEnd, messages.StreamTypeToolCallEnd, messages.StreamTypeAudioDelta, messages.StreamTypeSessionClose} {
		message := messages.StreamMessage{Type: messageType, Role: messages.RoleAssistant, ToolCallId: "call-3", Value: messages.NewMessageEndValue(messages.TokenUsage{})}
		if messageType == messages.StreamTypeToolCallEnd {
			message.Value = messages.NewToolCallEndValue("call-3", "lookup", `{}`)
		}
		if messageType == messages.StreamTypeAudioDelta {
			message.Value = messages.NewAudioDeltaValue([]byte{1})
		}
		if messageType == messages.StreamTypeInputItemAdded {
			message.Value = messages.NewInputItemAddedValue("item-1")
		}
		if err := live.RecordMessage(context.Background(), session.LiveRecord{Direction: session.LiveRecordAgent, Message: message}); err != nil {
			t.Fatalf("live RecordMessage(%s): %v", messageType, err)
		}
	}
	if err := live.RecordMessage(context.Background(), session.LiveRecord{Direction: session.LiveRecordAgent, Message: messages.StreamMessage{Role: messages.RoleTool, Type: messages.StreamMessageType("tool.result")}}); err != nil {
		t.Fatalf("live tool-role RecordMessage: %v", err)
	}
	if err := live.RecordAudio(context.Background(), session.LiveAudioRecord{Direction: session.LiveRecordClient, Admission: session.LiveAudioQueueAdmitted, Frame: audio.PCMFrame{Samples: []int16{1, 2}}}); err != nil {
		t.Fatalf("live RecordAudio: %v", err)
	}
	if err := live.RecordAudio(context.Background(), session.LiveAudioRecord{Direction: session.LiveRecordAgent, Admission: session.LiveAudioMediaBridged, Frame: audio.PCMFrame{Samples: []int16{3, 4}}}); err != nil {
		t.Fatalf("live output-rate RecordAudio: %v", err)
	}
	if err := live.RecordEvent(context.Background(), session.LiveEvent{Kind: "provider_event", Timestamp: time.Now()}); err != nil {
		t.Fatalf("live RecordEvent: %v", err)
	}
	if err := live.RecordEvent(context.Background(), session.LiveEvent{Kind: "provider_error", Timestamp: time.Now(), Error: errors.New("provider event failed")}); err != nil {
		t.Fatalf("live error RecordEvent: %v", err)
	}
	if err := live.RecordEvent(context.Background(), session.LiveEvent{Kind: string(session.LiveEventTerminal)}); err != nil {
		t.Fatalf("live terminal RecordEvent: %v", err)
	}
	if err := live.Finalize(context.Background(), errors.New("run failed")); err != nil {
		t.Fatalf("live Finalize: %v", err)
	}
}

func testSessionTracePlaybackAndService(t *testing.T) {
	observer := &recordingSessionRuntimeObserver{}
	recorder := sessiontracewire.NewRuntimeRecorder(observer, clock.Real{})
	sink := &diagnosticRecordSink{}
	playback := sessiontracewire.NewPlaybackDiagnostics(sessiontrace.PlaybackDiagnosticsOptions{Sink: sink, Runtime: recorder})
	var existingPlayback, existingReceipt, existingCapture int
	playbackObserver := playback.PlaybackObserver(func(devicegw.DeviceID, audio.PlaybackQueueStats) { existingPlayback++ })
	playbackObserver(devicegw.DeviceID("virtual:output"), audio.PlaybackQueueStats{Format: audio.DeviceFormat{SampleRate: 16000, Channels: 1}, DroppedSamples: 4, OverflowEvents: 1})
	receiptObserver := playback.PlaybackReceiptObserver(func(audio.PlaybackReceipt) { existingReceipt++ })
	receiptObserver(audio.PlaybackReceipt{CommandID: 10, Applied: true})
	captureObserver := playback.CaptureObserver(func(devicegw.DeviceID, audio.CaptureQueueStats) { existingCapture++ })
	captureObserver(devicegw.DeviceID("virtual:input"), audio.CaptureQueueStats{CapturedSamples: 2, DroppedSamples: 1})
	if existingPlayback != 1 || existingReceipt != 1 || existingCapture != 1 || len(sink.events(SessionDiagnosticEventPlaybackOverflow)) != 1 {
		t.Fatalf("playback callbacks = %d/%d/%d, diagnostics = %d", existingPlayback, existingReceipt, existingCapture, len(sink.events(SessionDiagnosticEventPlaybackOverflow)))
	}
	playback.RecordParticipantPlaybackOverflow("participant-1", nil)
	zeroPlayback := sessiontracewire.NewPlaybackDiagnostics(sessiontrace.PlaybackDiagnosticsOptions{
		MetricSampler: observability.MetricSamplerFunc(func(context.Context, observability.MetricSample) error { return errors.New("metric unavailable") }),
		Logger:        observability.LoggerFunc(func(context.Context, observability.LogRecord) error { return errors.New("logger unavailable") }),
	})
	zeroPlayback.PlaybackObserver(nil)(devicegw.DeviceID("virtual:output"), audio.PlaybackQueueStats{})
	zeroPlayback.CaptureObserver(nil)(devicegw.DeviceID("virtual:input"), audio.CaptureQueueStats{})
	trace, err := sessiontracewire.NewService().Prepare(sessiontrace.Request{TraceAudio: true, RecordDirectory: filepath.Join(t.TempDir(), "requested"), Clock: clock.Real{}, Credentials: []string{"secret"}, RuntimeObserver: observer})
	if err != nil {
		t.Fatalf("trace Prepare: %v", err)
	}
	provider, providerOK := trace.RuntimeObserver().(sessiontrace.ProviderBoundaryObserver)
	commit, commitOK := trace.RuntimeObserver().(sessiontrace.CommitPayloadObserver)
	if !providerOK || !provider.ObserveProviderBoundaries() || !commitOK || !commit.RetainCommitPayload() {
		t.Fatal("trace observer policy preferences were not preserved")
	}
	if err := trace.DeviceBinding().PlaybackSamplesObserver(context.Background(), 16000, []int16{1, 2}); err != nil {
		t.Fatalf("trace playback callback: %v", err)
	}
	trace.RuntimeObserver().ObserveSessionRuntime(sessiontrace.SessionRuntimeObservation{Kind: "provider_wire_send", Payload: []byte("secret"), Error: "secret"})
	if err := trace.Finish(context.Background(), "", false); err == nil {
		t.Fatal("unpublished trace Finish returned nil")
	}
	if _, err := sessiontracewire.NewService().Prepare(sessiontrace.Request{TraceAudio: true}); !errors.Is(err, sessiontrace.ErrClockRequired) {
		t.Fatalf("missing clock error = %v", err)
	}
	if err := trace.Finish(context.Background(), "", true); err != nil {
		t.Fatalf("second trace Finish: %v", err)
	}
}

func testSessionTraceMetricsAndProviderWire(t *testing.T) {
	collector := sessiontracewire.NewReplayMetricsCollector(sessiontrace.MetricsCollectorOptions{Clock: clock.Real{}, Runner: func(context.Context, string, string) (metrics.Snapshot, error) {
		return metrics.Snapshot{}, errors.New("replay failed")
	}})
	if _, err := collector.Collect(context.Background(), "fixture", "prompt"); err == nil {
		t.Fatal("metrics collector accepted a failed replay")
	}
	for name, options := range map[string]sessiontrace.MetricsCollectorOptions{
		"missing clock":     {Runner: func(context.Context, string, string) (metrics.Snapshot, error) { return metrics.Snapshot{}, nil }},
		"factory not ready": {Clock: clock.Real{}, FactoryReady: func() bool { return false }},
		"missing runner":    {Clock: clock.Real{}},
	} {
		if _, err := sessiontracewire.NewReplayMetricsCollector(options).Collect(context.Background(), "fixture", "prompt"); err == nil {
			t.Fatalf("metrics collector %s returned nil error", name)
		}
	}
	observer := &recordingSessionRuntimeObserver{}
	inner := testTransportDialer{conn: &testTransportConn{}}
	decorated := sessiontracewire.NewProviderWireDialer(inner, observer, clock.Real{})
	conn, err := decorated.Dial("test-endpoint", map[string]string{"Authorization": "redacted"})
	if err != nil || conn == nil {
		t.Fatalf("provider wire Dial = %v, %v", conn, err)
	}
	if err := conn.WriteMessage(1, []byte("{}")); err != nil {
		t.Fatalf("provider wire WriteMessage: %v", err)
	}
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("provider wire ReadMessage: %v", err)
	}
	if got := sessiontracewire.NewProviderWireDialer(inner, nil, clock.Real{}); got == nil {
		t.Fatal("provider wire nil observer removed the inner dialer")
	}
	if got := sessiontracewire.NewProviderWireDialer(nil, observer, clock.Real{}); got != nil {
		t.Fatal("provider wire nil inner returned a dialer")
	}
	dialErr := errors.New("dial failed")
	if _, err := sessiontracewire.NewProviderWireDialer(testTransportDialer{err: dialErr}, observer, clock.Real{}).Dial("test-endpoint", nil); !errors.Is(err, dialErr) {
		t.Fatalf("provider wire dial error = %v, want %v", err, dialErr)
	}
	firstErr := errors.New("first merged error")
	first := make(chan error, 1)
	second := make(chan error)
	first <- firstErr
	close(first)
	close(second)
	if err := <-sessiontracewire.MergeErrorChannels(context.Background(), first, second); !errors.Is(err, firstErr) {
		t.Fatalf("merged provider error = %v, want %v", err, firstErr)
	}
}

type serviceContractScheduledSender struct{}

func (serviceContractScheduledSender) SendAudioInput(context.Context, []byte) error { return nil }
func (serviceContractScheduledSender) SendSessionEvent(context.Context, messages.StreamMessage) error {
	return nil
}

type testTransportDialer struct {
	conn transport.Conn
	err  error
}

func (d testTransportDialer) Dial(string, map[string]string) (transport.Conn, error) {
	if d.err != nil {
		return nil, d.err
	}
	return d.conn, nil
}

type testTransportConn struct{}

func (*testTransportConn) ReadMessage() (int, []byte, error) { return 1, []byte("{}"), nil }
func (*testTransportConn) WriteMessage(int, []byte) error    { return nil }
func (*testTransportConn) Close() error                      { return nil }
