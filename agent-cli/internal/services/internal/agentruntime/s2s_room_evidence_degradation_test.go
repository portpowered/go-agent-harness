package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
)

func TestRunRoom_EvidenceFailureDegradesWithoutStoppingParticipants(t *testing.T) {
	ids := []string{"a", "b"}
	inferencers := map[string]*roomTestInferencer{
		"a": {events: []messages.StreamMessage{roomTestSessionOpen("a")}},
		"b": {events: []messages.StreamMessage{roomTestSessionOpen("b")}},
	}
	outputDir := filepath.Join(t.TempDir(), "room-run")
	opts, _ := newRoomTestRunOptions(ids, inferencers)
	opts.OutputDir = outputDir
	failure := &roomEvidenceOperationFailure{err: errors.New("injected room evidence delta write failure")}
	opts.evidenceService = roomEvidenceFailureService{Service: opts.evidenceService, failure: failure}
	opened := make(chan string, len(ids))
	streamedText := make(chan string, len(ids))
	opts.onParticipantSessionOpen = func(participantID string) {
		opened <- participantID
	}
	opts.onParticipantStream = func(participantID string, msg messages.StreamMessage) {
		if msg.Type == messages.StreamTypeTextDelta {
			streamedText <- participantID
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outcome := make(chan roomTestRunOutcome, 1)
	go func() {
		result, err := RunRoomWithResult(ctx, io.Discard, opts)
		outcome <- roomTestRunOutcome{result: result, err: err}
	}()

	waitForRoomParticipants(t, opened, ids, "both participants did not become live")
	aSession := inferencers["a"].sessionsSnapshot()[0]
	bSession := inferencers["b"].sessionsSnapshot()[0]
	textDelta := messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextDeltaValue("survivor remains live"),
	}
	aSession.publish(textDelta)
	bSession.publish(textDelta)
	waitForRoomParticipants(t, streamedText, ids, "evidence failure interrupted participant stream processing")
	if !failure.triggered.Load() {
		t.Fatal("room evidence recorder did not exercise the injected public write failure")
	}
	assertRoomParticipantsStillLive(t, aSession, bSession)

	cancel()
	got := waitForRoomOutcome(t, outcome)
	assertDegradedRoomResult(t, got)
	assertDegradedRoomManifest(t, filepath.Join(outputDir, RoomEvidenceManifestPath))
}

type roomEvidenceOperationFailure struct {
	once      sync.Once
	triggered atomic.Bool
	err       error
}

type roomEvidenceFailureService struct {
	roomevidence.Service
	failure *roomEvidenceOperationFailure
}

func (s roomEvidenceFailureService) Open(request roomevidence.RecordingRequest) (roomevidence.Recorder, error) {
	recorder, err := s.Service.Open(request)
	if err != nil {
		return nil, err
	}
	return roomEvidenceFailureRecorder{Recorder: recorder, failure: s.failure}, nil
}

type roomEvidenceFailureRecorder struct {
	roomevidence.Recorder
	failure *roomEvidenceOperationFailure
}

func (r roomEvidenceFailureRecorder) Observe(observation roomevidence.Observation) error {
	if observation.Kind == roomevidence.ObservationDelta &&
		observation.ParticipantID == "a" &&
		observation.StreamMessage.Type == messages.StreamTypeTextDelta {
		injected := false
		r.failure.once.Do(func() {
			r.failure.triggered.Store(true)
			injected = true
		})
		if injected {
			return r.failure.err
		}
	}
	return r.Recorder.Observe(observation)
}

func waitForRoomParticipants(t *testing.T, events <-chan string, ids []string, failure string) {
	t.Helper()
	seen := make(map[string]bool, len(ids))
	for len(seen) < len(ids) {
		select {
		case id := <-events:
			seen[id] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("%s; observed participants: %v", failure, seen)
		}
	}
}

func assertRoomParticipantsStillLive(t *testing.T, a, b *roomTestSession) {
	t.Helper()
	if a.doneSnapshot() || b.doneSnapshot() {
		t.Fatalf("participant sessions stopped after evidence failure: a_done=%v b_done=%v", a.doneSnapshot(), b.doneSnapshot())
	}
	if a.closeCallsSnapshot() != 0 || b.closeCallsSnapshot() != 0 {
		t.Fatalf("participant sessions were cleaned up before room stop: a_close=%d b_close=%d", a.closeCallsSnapshot(), b.closeCallsSnapshot())
	}
}

func waitForRoomOutcome(t *testing.T, outcome <-chan roomTestRunOutcome) roomTestRunOutcome {
	t.Helper()
	select {
	case got := <-outcome:
		return got
	case <-time.After(3 * time.Second):
		t.Fatal("room did not finish after explicit stop")
		return roomTestRunOutcome{}
	}
}

func assertDegradedRoomResult(t *testing.T, got roomTestRunOutcome) {
	t.Helper()
	if got.err != nil {
		t.Fatalf("evidence failure became a room error: %v", got.err)
	}
	if got.result.TerminationReason != RoomTerminationStopped || got.result.Error != "" {
		t.Fatalf("room result = %+v, want clean stopped runtime result", got.result)
	}
	if got.result.RecordingStatus == nil || got.result.RecordingStatus.State != transcript.RecordingStatusPartial || got.result.RecordingStatus.Reason == "" {
		t.Fatalf("room recording status = %+v, want partial degraded status", got.result.RecordingStatus)
	}
	if got.result.Participants["a"].RecordingStatus == nil || got.result.Participants["a"].RecordingStatus.State != transcript.RecordingStatusPartial {
		t.Fatalf("participant a recording status = %+v, want partial", got.result.Participants["a"].RecordingStatus)
	}
	if got.result.Participants["b"].RecordingStatus != nil {
		t.Fatalf("participant b recording status = %+v, want healthy", got.result.Participants["b"].RecordingStatus)
	}
	if _, ok := got.result.DegradedArtifacts["agent-a.deltas.jsonl"]; !ok {
		t.Fatalf("room degraded artifacts = %v, want a's delta artifact", got.result.DegradedArtifacts)
	}
}

func assertDegradedRoomManifest(t *testing.T, path string) {
	t.Helper()
	manifestData := readRoomEvidenceFile(t, path)
	var manifest roomEvidenceManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("decode room manifest: %v", err)
	}
	if !manifest.Finalized || manifest.Error != "" {
		t.Fatalf("degraded room manifest finalized=%v error=%q, want finalized without runtime error", manifest.Finalized, manifest.Error)
	}
	if manifest.RecordingStatus == nil || manifest.RecordingStatus.State != transcript.RecordingStatusPartial {
		t.Fatalf("manifest recording status = %+v, want partial", manifest.RecordingStatus)
	}
	if manifest.Participants["a"].RecordingStatus == nil || manifest.Participants["a"].RecordingStatus.State != transcript.RecordingStatusPartial {
		t.Fatalf("manifest participant a recording status = %+v, want partial", manifest.Participants["a"].RecordingStatus)
	}
	if manifest.Participants["b"].RecordingStatus != nil {
		t.Fatalf("manifest participant b recording status = %+v, want healthy", manifest.Participants["b"].RecordingStatus)
	}
	if _, ok := manifest.DegradedArtifacts["agent-a.deltas.jsonl"]; !ok {
		t.Fatalf("manifest degraded artifacts = %v, want a's delta artifact", manifest.DegradedArtifacts)
	}
}
