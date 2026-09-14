package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
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
	captureObserver := diagnostics.CaptureObserver(nil)
	captureObserver(devicegw.DeviceID("virtual:input"), audio.CaptureQueueStats{CapturedSamples: 4, DroppedSamples: 1, DropPolicy: "drop_oldest"})
	diagnostics.RecordParticipantPlaybackOverflow("participant-1", nil)
	if len(runtimeObserver.snapshot()) != 1 {
		t.Fatalf("receipt observations = %d, want one", len(runtimeObserver.snapshot()))
	}
}

func TestReplayMetricsCollectorPublicContractReportsWireDeltas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.session.json")
	capture := gatewaytesting.SessionCapture{
		Version:  gatewaytesting.SessionCaptureVersion,
		Provider: gatewaytesting.SessionProviderMetadata{Name: "fixture-provider", Model: "fixture-model"},
		Records: []gatewaytesting.CapturedSessionEvent{
			{Sequence: 1, Direction: gatewaytesting.DirectionServerToClient, TimestampMs: 1, Type: "response.output_text.delta", PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage, Payload: json.RawMessage(`{"type":"response.output_text.delta","delta":"hello"}`)},
			{Sequence: 2, Direction: gatewaytesting.DirectionClientToServer, TimestampMs: 2, Type: "input_audio_buffer.append", PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage, Payload: json.RawMessage(`{"type":"input_audio_buffer.append","audio":"AQID"}`)},
		},
	}
	data, err := json.Marshal(capture)
	if err != nil {
		t.Fatalf("marshal capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write capture: %v", err)
	}
	collector := NewReplayMetricsCollector(sessiontrace.MetricsCollectorOptions{
		Clock: clock.Real{},
		Runner: func(context.Context, string, string) (metrics.Snapshot, error) {
			return metrics.Snapshot{Series: []metrics.SeriesSnapshot{{Direction: metrics.DirectionOutput, Modality: metrics.ModalityText, TotalBytes: 5}}}, nil
		},
	})
	series, err := collector.Collect(context.Background(), path, "fixture prompt")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	var outputText, inputAudio bool
	for _, entry := range series {
		switch {
		case entry.Direction == string(metrics.DirectionOutput) && entry.Modality == string(metrics.ModalityText):
			outputText = entry.ObservedDeltas == 5 && entry.ReportedTotal == 5
		case entry.Direction == string(metrics.DirectionInput) && entry.Modality == string(metrics.ModalityAudio):
			inputAudio = entry.ObservedDeltas == 3
		}
	}
	if !outputText || !inputAudio {
		t.Fatalf("metrics series = %+v, want text=5 and audio=3", series)
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
