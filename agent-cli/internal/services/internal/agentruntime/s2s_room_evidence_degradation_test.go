package agentruntime

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

func TestRunRoom_EvidenceWriteFailureDegradesWithoutStoppingParticipants(t *testing.T) {
	ids := []string{"a", "b"}
	inferencers := map[string]*roomTestInferencer{
		"a": {events: []messages.StreamMessage{roomTestSessionOpen("a")}},
		"b": {events: []messages.StreamMessage{roomTestSessionOpen("b")}},
	}
	outputDir := filepath.Join(t.TempDir(), "room-run")
	opts, _ := newRoomTestRunOptions(ids, inferencers)
	opts.OutputDir = outputDir

	var injectionErr error
	opts.onRoomEvidenceReady = func(evidence *roomEvidence) {
		injectionErr = evidence.participant("a").deltas.close()
	}
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

	seenOpened := make(map[string]bool, len(ids))
	for len(seenOpened) != len(ids) {
		select {
		case participantID := <-opened:
			seenOpened[participantID] = true
		case <-time.After(2 * time.Second):
			t.Fatal("both participants did not become live")
		}
	}
	if injectionErr != nil {
		t.Fatalf("inject evidence failure: %v", injectionErr)
	}

	aSession := inferencers["a"].sessionsSnapshot()[0]
	bSession := inferencers["b"].sessionsSnapshot()[0]
	textDelta := messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextDeltaValue("survivor remains live"),
	}
	aSession.publish(textDelta)
	bSession.publish(textDelta)
	seenText := make(map[string]bool, len(ids))
	for len(seenText) != len(ids) {
		select {
		case participantID := <-streamedText:
			seenText[participantID] = true
		case <-time.After(2 * time.Second):
			t.Fatal("evidence failure interrupted participant stream processing")
		}
	}
	if aSession.doneSnapshot() || bSession.doneSnapshot() {
		t.Fatalf("participant sessions stopped after evidence failure: a_done=%v b_done=%v", aSession.doneSnapshot(), bSession.doneSnapshot())
	}
	if aSession.closeCallsSnapshot() != 0 || bSession.closeCallsSnapshot() != 0 {
		t.Fatalf("participant sessions were cleaned up before room stop: a_close=%d b_close=%d", aSession.closeCallsSnapshot(), bSession.closeCallsSnapshot())
	}

	cancel()
	var got roomTestRunOutcome
	select {
	case got = <-outcome:
	case <-time.After(3 * time.Second):
		t.Fatal("room did not finish after explicit stop")
	}
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

	manifestData := readRoomEvidenceFile(t, filepath.Join(outputDir, RoomEvidenceManifestPath))
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
