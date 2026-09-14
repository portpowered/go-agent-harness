package agentruntime

import (
	"context"
	"errors"
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

func TestSessionTraceObserverContractThroughServiceWire(t *testing.T) {
	sink := &diagnosticRecordSink{}
	observer := sessiontracewire.NewObserver(sessiontrace.NewObserverOptions{
		Sink:            sink,
		Provider:        "caller-provider",
		Model:           "caller-model",
		TerminalService: wire.NewService(),
	})
	observer.NoteUserTextInput("hello")
	observer.Observe(messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("caller-session", "audio")})
	observer.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "response-1", Value: messages.NewMessageStartValue()})
	observer.Observe(messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, ResponseID: "response-1", Value: messages.NewTextStartValue()})
	observer.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: "response-1", Value: messages.NewTextDeltaValue("world")})
	observer.Observe(messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, ResponseID: "response-1", Value: messages.NewTextEndValue()})
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

	toolObserver := sessiontracewire.NewObserver(sessiontrace.NewObserverOptions{TerminalService: wire.NewService()})
	toolObserver.SetToolResultsEnabled(true)
	toolObserver.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "tool-response", Value: messages.NewMessageStartValue()})
	toolObserver.Observe(messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ResponseID: "tool-response", Value: messages.NewToolCallEndValue("call-1", "lookup", `{}`)})
	toolObserver.NoteToolResultAccepted("call-1")
	toolObserver.NoteToolContinuationRequestedFor("call-1")
	if !toolObserver.HasPendingToolContinuations() {
		t.Fatal("accepted tool result did not retain its continuation obligation")
	}
	toolObserver.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "tool-response", Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	toolObserver.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "tool-continuation", Value: messages.NewMessageStartValue()})
	toolObserver.Observe(messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, ResponseID: "tool-continuation", Value: messages.NewTextStartValue()})
	toolObserver.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: "tool-continuation", Value: messages.NewTextDeltaValue("ok")})
	toolObserver.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "tool-continuation", Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	if toolObserver.HasPendingToolContinuations() || toolObserver.HasUnresolvedToolCalls() {
		t.Fatal("tool continuation obligation was not retired by its response")
	}

	unexecutableSink := &diagnosticRecordSink{}
	unexecutable := sessiontracewire.NewObserver(sessiontrace.NewObserverOptions{Sink: unexecutableSink, TerminalService: wire.NewService()})
	unexecutable.SetToolResultsEnabledForObservation(false)
	unexecutable.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "unexecutable-response", Value: messages.NewMessageStartValue()})
	unexecutable.Observe(messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ResponseID: "unexecutable-response", Value: messages.NewToolCallEndValue("call-unexecutable", "lookup", `{}`)})
	if got := len(unexecutableSink.events(SessionDiagnosticEventToolCall)); got != 1 {
		t.Fatalf("unexecutable tool diagnostics = %d, want one", got)
	}

	scheduled := sessiontracewire.NewObserver(sessiontrace.NewObserverOptions{TerminalService: wire.NewService()})
	scheduled.ScheduleAudioInputs([]sessiontrace.ScheduledAudioInput{{AfterCompletedTurns: 0, PCM: []byte{1}, EndOfTurn: true}})
	if !scheduled.ScheduledAudioReady() || scheduled.ScheduledAudioComplete() {
		t.Fatal("scheduled observer reported an invalid initial readiness state")
	}
	if err := scheduled.DispatchScheduledInputs(context.Background(), serviceContractScheduledSender{}); err != nil {
		t.Fatalf("scheduled DispatchScheduledInputs: %v", err)
	}
	scheduled.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "scheduled-response", Value: messages.NewMessageStartValue()})
	scheduled.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: "scheduled-response", Value: messages.NewTextDeltaValue("scheduled")})
	scheduled.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "scheduled-response", Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	if !scheduled.ScheduledAudioComplete() || scheduled.ScheduledAudioIncomplete() {
		t.Fatal("scheduled observer did not retire its dispatched response")
	}

	failureSink := &diagnosticRecordSink{}
	failureObserver := sessiontracewire.NewObserver(sessiontrace.NewObserverOptions{Sink: failureSink, TerminalService: wire.NewService()})
	failureObserver.Observe(messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("failure-session", "audio")})
	failureObserver.Observe(messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValueWithTerminal("provider failed", "provider_error", messages.TerminalReasonTerminalFailure, messages.TerminalProvenanceProvider, messages.TerminalOutputNone)})
	if failureObserver.State().Failure == nil {
		t.Fatal("service-owned failure was not retained")
	}
	if err := failureObserver.Finish(errors.New("provider failed")); err == nil || len(failureSink.events(SessionDiagnosticEventFailure)) != 1 {
		t.Fatalf("failure Finish = %v, records = %d", err, len(failureSink.events(SessionDiagnosticEventFailure)))
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
	runtimeObserver := &recordingSessionRuntimeObserver{}
	recorder := sessiontracewire.NewRuntimeRecorder(runtimeObserver, clock.Real{})
	if recorder == nil {
		t.Fatal("NewRuntimeRecorder returned nil")
	}
	recorder.EnableProviderBoundaryObservations()
	recorder.Observe(sessiontrace.SessionRuntimeObservationAudioInput, []byte("input"), 0, true, context.Canceled)
	recorder.ObserveWithInputCommit(sessiontrace.SessionRuntimeObservationInputCommit, []byte("commit"), 0, 1, true, errors.New("commit failed"))
	recorder.ObserveFinal(sessiontrace.SessionRuntimeObservationTerminal, []byte("final"), 1, 1, false, errors.New("final failed"), &sessiontrace.SessionFinalAccounting{
		PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3,
		Metrics: metrics.Snapshot{
			HistogramBounds: []int64{1, 2},
			Series:          []metrics.SeriesSnapshot{{Histogram: metrics.HistogramSnapshot{Bounds: []int64{1}, BucketCounts: []uint64{2}, SampleCount: 1}}},
		},
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
	if len(runtimeObserver.observations) < 10 {
		t.Fatalf("runtime observations = %d, want each boundary", len(runtimeObserver.observations))
	}

	live := sessiontracewire.NewLiveRecorder(sessiontrace.LiveRecorderOptions{Observer: runtimeObserver, InputRate: 16000, OutputRate: 24000})
	if err := live.RecordMessage(context.Background(), session.LiveRecord{Direction: session.LiveRecordAgent, Message: messages.StreamMessage{Type: messages.StreamTypeResponseCreate}}); err != nil {
		t.Fatalf("live RecordMessage: %v", err)
	}
	for _, messageType := range []messages.StreamMessageType{
		messages.StreamTypeInputItemAdded,
		messages.StreamTypeMessageEnd,
		messages.StreamTypeToolCallEnd,
		messages.StreamTypeAudioDelta,
		messages.StreamTypeSessionClose,
	} {
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

	preGate, uploaded, playbackSamples, rendered, unavailable := 0, 0, 0, 0, 0
	prepared, err := sessiontracewire.NewService().Prepare(sessiontrace.Request{
		TraceAudio:      true,
		RecordDirectory: filepath.Join(t.TempDir(), "requested"),
		Clock:           clock.Real{},
		Credentials:     []string{"secret"},
		Device: sessiontrace.DeviceBinding{
			PreGateSamplesObserver:     func(int, []int16) { preGate++ },
			UploadedSamplesObserver:    func(int, []int16) { uploaded++ },
			PlaybackSamplesObserver:    func(context.Context, int, []int16) error { playbackSamples++; return nil },
			RenderedSamplesObserver:    func(int, []int16) { rendered++ },
			RenderedSamplesUnavailable: func() { unavailable++ },
		},
	})
	if err != nil {
		t.Fatalf("trace Prepare: %v", err)
	}
	if prepared.StagedPath() == "" || prepared.DeviceBinding().PreGateSamplesObserver == nil || prepared.RuntimeObserver() == nil {
		t.Fatal("trace Prepare did not expose its public prepared contract")
	}
	providerBoundary, providerOK := prepared.RuntimeObserver().(sessiontrace.ProviderBoundaryObserver)
	commitPayload, commitOK := prepared.RuntimeObserver().(sessiontrace.CommitPayloadObserver)
	if !providerOK || !providerBoundary.ObserveProviderBoundaries() || !commitOK || commitPayload.RetainCommitPayload() {
		t.Fatal("trace observer policy preferences were not published")
	}
	binding := prepared.DeviceBinding()
	binding.PreGateSamplesObserver(16000, []int16{1, 2})
	binding.UploadedSamplesObserver(16000, []int16{3, 4})
	if err := binding.PlaybackSamplesObserver(context.Background(), 16000, []int16{5, 6}); err != nil {
		t.Fatalf("trace playback callback: %v", err)
	}
	binding.RenderedSamplesObserver(16000, []int16{7, 8})
	binding.RenderedSamplesUnavailable()
	prepared.RuntimeObserver().ObserveSessionRuntime(sessiontrace.SessionRuntimeObservation{Kind: "provider_wire_send", Payload: []byte("secret"), Error: "secret"})
	if preGate != 1 || uploaded != 1 || playbackSamples != 1 || rendered != 1 || unavailable != 1 {
		t.Fatalf("trace binding callbacks = %d/%d/%d/%d/%d", preGate, uploaded, playbackSamples, rendered, unavailable)
	}
	if err := prepared.Finish(context.Background(), "", false); err == nil {
		t.Fatal("unpublished trace Finish returned nil; staged evidence should be retained")
	}
	if _, err := sessiontracewire.NewService().Prepare(sessiontrace.Request{TraceAudio: true}); !errors.Is(err, sessiontrace.ErrClockRequired) {
		t.Fatalf("missing clock error = %v", err)
	}
	if disabled, err := sessiontracewire.NewService().Prepare(sessiontrace.Request{}); err != nil || disabled != nil {
		t.Fatalf("disabled trace Prepare = %v, %v", disabled, err)
	}
	if err := prepared.Finish(context.Background(), "", true); err != nil {
		t.Fatalf("second trace Finish: %v", err)
	}

	chained, err := sessiontracewire.NewService().Prepare(sessiontrace.Request{
		TraceAudio:      true,
		RecordDirectory: filepath.Join(t.TempDir(), "chained"),
		Clock:           clock.Real{},
		RuntimeObserver: runtimeObserver,
	})
	if err != nil {
		t.Fatalf("chained trace Prepare: %v", err)
	}
	chainedProvider, providerOK := chained.RuntimeObserver().(sessiontrace.ProviderBoundaryObserver)
	chainedCommit, commitOK := chained.RuntimeObserver().(sessiontrace.CommitPayloadObserver)
	if !providerOK || !chainedProvider.ObserveProviderBoundaries() || !commitOK || !chainedCommit.RetainCommitPayload() {
		t.Fatal("chained observer policy preferences were not preserved")
	}
	payload := []byte("chained payload")
	chained.RuntimeObserver().ObserveSessionRuntime(sessiontrace.SessionRuntimeObservation{Payload: payload, FinalAccounting: &sessiontrace.SessionFinalAccounting{PromptTokens: 1}})
	payload[0] = 'X'

	bindingError := errors.New("prior playback observer failed")
	withPriorError, err := sessiontracewire.NewService().Prepare(sessiontrace.Request{
		TraceAudio:      true,
		RecordDirectory: filepath.Join(t.TempDir(), "prior-error"),
		Clock:           clock.Real{},
		Device:          sessiontrace.DeviceBinding{PlaybackSamplesObserver: func(context.Context, int, []int16) error { return bindingError }},
	})
	if err != nil {
		t.Fatalf("prior-error trace Prepare: %v", err)
	}
	if err := withPriorError.DeviceBinding().PlaybackSamplesObserver(context.Background(), 16000, []int16{1}); !errors.Is(err, bindingError) {
		t.Fatalf("prior playback error = %v, want %v", err, bindingError)
	}
	if err := withPriorError.Finish(context.Background(), "", true); err != nil {
		t.Fatalf("prior-error trace Finish: %v", err)
	}

	retainingRecorder := sessiontracewire.NewRuntimeRecorder(chained.RuntimeObserver(), clock.Real{})
	retainingRecorder.ProviderAudioSent([]byte("retained provider audio"))
	retainingRecorder.InputCommit()
	if err := chained.Finish(context.Background(), "", true); err != nil {
		t.Fatalf("chained trace Finish: %v", err)
	}

	zeroPlayback := sessiontracewire.NewPlaybackDiagnostics(sessiontrace.PlaybackDiagnosticsOptions{
		MetricSampler: observability.MetricSamplerFunc(func(context.Context, observability.MetricSample) error { return errors.New("metric unavailable") }),
		Logger:        observability.LoggerFunc(func(context.Context, observability.LogRecord) error { return errors.New("logger unavailable") }),
	})
	zeroPlayback.PlaybackObserver(nil)(devicegw.DeviceID("virtual:output"), audio.PlaybackQueueStats{})
	zeroPlayback.CaptureObserver(nil)(devicegw.DeviceID("virtual:input"), audio.CaptureQueueStats{})
	collector := sessiontracewire.NewReplayMetricsCollector(sessiontrace.MetricsCollectorOptions{
		Clock: clock.Real{},
		Runner: func(context.Context, string, string) (metrics.Snapshot, error) {
			return metrics.Snapshot{}, errors.New("replay failed")
		},
	})
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

	inner := testTransportDialer{conn: &testTransportConn{}}
	decorated := sessiontracewire.NewProviderWireDialer(inner, runtimeObserver, clock.Real{})
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
	if err := conn.WriteMessage(2, []byte{0, 1, 2}); err != nil {
		t.Fatalf("provider wire binary WriteMessage: %v", err)
	}
	if got := sessiontracewire.NewProviderWireDialer(inner, nil, clock.Real{}); got == nil {
		t.Fatal("provider wire nil observer unexpectedly removed the inner dialer")
	}
	if got := sessiontracewire.NewProviderWireDialer(nil, runtimeObserver, clock.Real{}); got != nil {
		t.Fatal("provider wire nil inner unexpectedly returned a dialer")
	}
	dialErr := errors.New("dial failed")
	if _, err := sessiontracewire.NewProviderWireDialer(testTransportDialer{err: dialErr}, runtimeObserver, clock.Real{}).Dial("test-endpoint", nil); !errors.Is(err, dialErr) {
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
