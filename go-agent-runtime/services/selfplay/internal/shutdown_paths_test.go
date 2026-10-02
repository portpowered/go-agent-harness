package internal

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
)

// These tests pin the shutdown and failure races of a self-play run one at a
// time. In the end-to-end service tests which side observes the stop first
// is decided by the scheduler, so each of these paths runs only in some of
// those runs.

type memoryEvidenceFile struct{ bytes.Buffer }

func (*memoryEvidenceFile) Name() string                   { return "memory" }
func (*memoryEvidenceFile) Sync() error                    { return nil }
func (*memoryEvidenceFile) Close() error                   { return nil }
func (*memoryEvidenceFile) Seek(int64, int) (int64, error) { return 0, nil }

func newMemoryEvidence() *evidence {
	result := &evidence{}
	for index, role := range []selfplay.SideRole{selfplay.RoleCustomer, selfplay.RoleAssistant} {
		result.sides[index] = &sideEvidence{
			role:        role,
			wav:         &wavRecorder{file: &memoryEvidenceFile{}, path: "side.wav", limit: 1 << 20},
			diagnostics: &jsonlRecorder{file: &memoryEvidenceFile{}, path: "diagnostics.jsonl", limit: 1 << 20},
			stream:      &jsonlRecorder{file: &memoryEvidenceFile{}, path: "stream.jsonl", limit: 1 << 20},
		}
	}
	return result
}

func newTestSideLoop(t *testing.T) *agentloop.AgentLoop {
	t.Helper()
	loop, err := newSideLoop(injectedInferencer{})
	if err != nil {
		t.Fatalf("new side loop: %v", err)
	}
	return loop
}

func cancelledContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	return ctx
}

func sessionOpenMessage() messages.StreamMessage {
	return messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("test", "audio")}
}

// The customer's opening seed is the first thing a run sends. When the run
// is already being cancelled, the cancellation is the reported cause, not the
// rejected enqueue it causes; otherwise the enqueue failure is reported.
func TestCustomerOpeningReportsCancellationOverRejectedSend(t *testing.T) {
	service := &Service{}
	stop := newStopState(1, nil)
	sent := false
	err := service.handleSideMessage(cancelledContext(t), newTestSideLoop(t), 0, selfplay.RoleCustomer, sessionOpenMessage(), &sent, newPCMBridge(), stop, newMemoryEvidence())
	if !errors.Is(err, context.Canceled) || !sent {
		t.Fatalf("opening during cancellation = %v (sent %t), want context.Canceled", err, sent)
	}

	full := newTestSideLoop(t)
	filler := []messages.Message{messages.NewTextMessage(messages.RoleUser, "filler")}
	for range sideLoopBufferCapacity {
		if err := full.Send(t.Context(), filler); err != nil {
			break
		}
	}
	sent = false
	if err := sendCustomerOpening(t.Context(), full, 0, sessionOpenMessage(), &sent); err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("opening into a full queue = %v, want the enqueue failure", err)
	}
	if err := sendCustomerOpening(t.Context(), full, 0, sessionOpenMessage(), &sent); err != nil {
		t.Fatalf("second opening = %v, want it skipped", err)
	}
}

// Audio arriving after cancellation is not written to the WAV evidence, and
// the failure names the side.
func TestSideAudioAfterCancellationIsNotRecorded(t *testing.T) {
	service := &Service{}
	evidence := newMemoryEvidence()
	audio := messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: &messages.AudioDeltaValue{Content: []byte{1, 0, 2, 0}}}
	sent := false
	err := service.handleSideMessage(cancelledContext(t), newTestSideLoop(t), 1, selfplay.RoleAssistant, audio, &sent, newPCMBridge(), newStopState(1, nil), evidence)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "assistant WAV evidence") {
		t.Fatalf("audio after cancellation = %v, want a cancelled assistant WAV write", err)
	}
	if written := evidence.sides[1].wav.dataBytes; written != 0 {
		t.Fatalf("WAV evidence recorded %d bytes after cancellation", written)
	}
}

// Once the run has stopped, the PCM bridge is closed: audio still arriving
// from a side is recorded as evidence but its failed bridge write is not a
// failure of the run.
func TestSideAudioAfterStopIgnoresClosedBridge(t *testing.T) {
	service := &Service{}
	stop := newStopState(1, nil)
	stop.commit(selfplay.StopTurnTarget, nil)
	bridge := newPCMBridge()
	bridge.close()
	audio := messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: &messages.AudioDeltaValue{Content: []byte{1, 0}}}
	if err := service.handleSideAudio(t.Context(), 0, selfplay.RoleCustomer, audio, bridge, stop, newMemoryEvidence()); err != nil {
		t.Fatalf("audio after stop = %v, want ignored bridge failure", err)
	}
	if err := bridge.write([]byte{1, 0}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("closed bridge write = %v, want io.ErrClosedPipe", err)
	}
}

// A turn completed after the run stopped is not counted, and a side failing
// after another side already failed does not replace the first cause.
func TestLateTurnsAndFailuresAfterStopAreDropped(t *testing.T) {
	stop := newStopState(1, nil)
	firstCause := errors.New("customer failed first")
	if result := failSide(stop, 0, firstCause, true); !errors.Is(result.err, firstCause) {
		t.Fatalf("first failure result = %+v, want its cause", result)
	}
	if result := failSide(stop, 1, errors.New("assistant failed second"), true); result.err != nil || result.index != 1 || !result.started {
		t.Fatalf("second failure result = %+v, want no cause of its own", result)
	}
	end := messages.StreamMessage{Type: messages.StreamTypeMessageEnd, ResponseID: "late", Value: messages.NewMessageEndValue(messages.TokenUsage{})}
	evidence := newMemoryEvidence()
	if err := recordCompletedTurn(1, end, stop, evidence); err != nil {
		t.Fatalf("late turn = %v", err)
	}
	if snapshot := stop.snapshot(); snapshot.turns != [2]int{} || !errors.Is(snapshot.err, firstCause) {
		t.Fatalf("terminal = %+v, want no counted turns and the first cause", snapshot)
	}
	if evidence.sides[1].diagnostics.bytes != 0 {
		t.Fatal("a turn after the stop was recorded as evidence")
	}
}

// The PCM pump that forwards one side's audio to the other ends quietly when
// the run is cancelled before the target loop exists, and reports a target
// loop that rejects the audio.
func TestPCMPumpStopsOnCancellationAndRejectedAudio(t *testing.T) {
	if err := pumpPCM(cancelledContext(t), newPCMBridge(), make(chan *agentloop.AgentLoop)); err != nil {
		t.Fatalf("pump cancelled before its target = %v, want a quiet stop", err)
	}
	err := forwardBridgePCM(cancelledContext(t), newTestSideLoop(t), bytes.NewReader([]byte{1, 0, 2, 0}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("forward into a cancelled loop = %v, want context.Canceled", err)
	}
}
