package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	longConversationTurnsPerParticipant = 4
	longConversationFramesPerTurn       = 3
)

// TestRunRoomWithResult_LongConversationEndsBothParticipantsCleanly drives
// the production room/session composition through eight ordered turns. Three
// one-second mixer frames make each scripted turn multi-second-equivalent,
// while the manually advanced cadence keeps the regression hermetic and fast.
func TestRunRoomWithResult_LongConversationEndsBothParticipantsCleanly(t *testing.T) {
	const (
		alphaID = "alpha"
		betaID  = "beta"
		model   = openAIRealtimeDefaultModel
	)

	const (
		frameSamples = 100
		frameBytes   = frameSamples * 2
	)
	silence := make([]byte, frameBytes)
	responses := map[string][]string{
		alphaID: {"alpha turn one", "alpha turn two", "alpha turn three", "alpha turn four"},
		betaID:  {"beta turn one", "beta turn two", "beta turn three", "beta turn four"},
	}
	captures := map[string]gwtesting.SessionCapture{
		alphaID: roomRealtimeLongConversationCapture(t, alphaID, model, silence, responses[alphaID]),
		betaID:  roomRealtimeLongConversationCapture(t, betaID, model, silence, responses[betaID]),
	}
	harness := newRoomRealtimeReplayHarness(t, captures)

	configDir := t.TempDir()
	writeSessionConfigFile(t, configDir, "model:\n  provider: openai\n")
	credentials := map[string]string{
		"ROOM_LONG_ALPHA_KEY": "room-long-alpha-key",
		"ROOM_LONG_BETA_KEY":  "room-long-beta-key",
	}
	manifest := room.Manifest{
		SchemaVersion: room.SchemaVersion,
		Room:          room.Room{MaxTurns: longConversationTurnsPerParticipant},
		Participants: []room.Participant{
			{ID: alphaID, SystemPrompt: "alpha system", Provider: config.ProviderOpenAI, Model: model, APIKeyEnv: "ROOM_LONG_ALPHA_KEY", Tools: []string{}},
			{ID: betaID, SystemPrompt: "beta system", Provider: config.ProviderOpenAI, Model: model, APIKeyEnv: "ROOM_LONG_BETA_KEY", Tools: []string{}},
		},
	}

	cadenceReady := make(chan *roomRealtimeReplayCadence, len(manifest.Participants))
	mixerConfig := room.PCM16MixerConfig{
		Format:            room.PCM16Format{SampleRate: frameSamples, Channels: 1, FrameDuration: time.Second},
		InputQueueFrames:  longConversationFramesPerTurn,
		OutputQueueFrames: longConversationFramesPerTurn,
		CadenceFactory: func(time.Duration) room.PCM16Cadence {
			cadence := newRoomRealtimeReplayCadence()
			cadenceReady <- cadence
			return cadence
		},
	}
	opened := make(chan string, len(manifest.Participants))
	responseEnds := make(chan string, longConversationTurnsPerParticipant*len(manifest.Participants))
	turnDiagnostics := make(chan string, longConversationTurnsPerParticipant*len(manifest.Participants))
	participantTerminals := make(chan RoomParticipantResult, len(manifest.Participants))
	diagnostics := make(chan struct {
		participantID string
		record        SessionDiagnosticRecord
	}, 128)
	roomCtx, cancel := context.WithTimeout(context.Background(), roomRealtimeReplayTestTimeout)
	defer cancel()

	outputDir := filepath.Join(t.TempDir(), "long-room")
	opts := RoomRunOptions{
		Manifest:           manifest,
		ConfigDir:          configDir,
		BaseURL:            "wss://room-replay.invalid/v1/realtime",
		MixerConfig:        mixerConfig,
		OutputDir:          outputDir,
		BoundShutdownGrace: 25 * time.Millisecond,
		CredentialLookup: func(name string) (string, bool) {
			value, ok := credentials[name]
			return value, ok
		},
		WebSocketDialerFactory: harness.DialerFactory,
		onParticipantSessionOpen: func(participantID string) {
			opened <- participantID
		},
		onParticipantStream: func(participantID string, msg messages.StreamMessage) {
			if msg.Type == messages.StreamTypeMessageEnd {
				responseEnds <- participantID
			}
		},
		OnParticipantTerminated: func(result RoomParticipantResult) {
			participantTerminals <- result
		},
		OnDiagnostic: func(participantID string, record SessionDiagnosticRecord) {
			diagnostics <- struct {
				participantID string
				record        SessionDiagnosticRecord
			}{participantID: participantID, record: record}
			if record.Event == SessionDiagnosticEventTurn {
				turnDiagnostics <- participantID
			}
		},
	}

	runDone := make(chan roomTestRunOutcome, 1)
	go func() {
		result, err := RunRoomWithResult(roomCtx, io.Discard, opts)
		runDone <- roomTestRunOutcome{result: result, err: err}
	}()

	var alphaCadence, betaCadence *roomRealtimeReplayCadence
	select {
	case alphaCadence = <-cadenceReady:
	case <-roomCtx.Done():
		t.Fatalf("alpha long-conversation cadence was not created: %v", roomCtx.Err())
	}
	select {
	case betaCadence = <-cadenceReady:
	case <-roomCtx.Done():
		t.Fatalf("beta long-conversation cadence was not created: %v", roomCtx.Err())
	}
	for range manifest.Participants {
		select {
		case <-opened:
		case <-roomCtx.Done():
			t.Fatalf("long-conversation room sessions did not open: %v", roomCtx.Err())
		}
	}

	cadences := map[string]*roomRealtimeReplayCadence{
		alphaID: alphaCadence,
		betaID:  betaCadence,
	}
	turnOrder := []string{alphaID, betaID, alphaID, betaID, alphaID, betaID, alphaID, betaID}
	for turnIndex, participantID := range turnOrder {
		cadence := cadences[participantID]
		for frameIndex := 0; frameIndex < longConversationFramesPerTurn; frameIndex++ {
			cadence.Advance()
			assertRoomRealtimeReplayAppend(t, harness.participant(participantID), silence)
		}
		awaitRoomRealtimeReplayResponseEnd(t, responseEnds, participantID)
		select {
		case got := <-turnDiagnostics:
			if got != participantID {
				t.Fatalf("completed turn %d participant = %q, want %q", turnIndex+1, got, participantID)
			}
		case <-roomCtx.Done():
			t.Fatalf("long-conversation turn %d was not admitted: %v", turnIndex+1, roomCtx.Err())
		}
	}

	var outcome roomTestRunOutcome
	select {
	case outcome = <-runDone:
	case <-roomCtx.Done():
		t.Fatalf("long-conversation room did not reach its turn bound: %v", roomCtx.Err())
	}
	if outcome.err != nil {
		t.Fatalf("long-conversation room replay: %v", outcome.err)
	}
	if outcome.result.Reason != RoomTerminationMaxTurnsReached || outcome.result.Error != "" {
		t.Fatalf("long-conversation room result = %+v, want max-turn clean result", outcome.result)
	}
	for _, participantID := range []string{alphaID, betaID} {
		participant, ok := outcome.result.Participants[participantID]
		if !ok {
			t.Fatalf("long-conversation result missing participant %q", participantID)
		}
		if !participant.Connected || participant.TurnsCompleted != longConversationTurnsPerParticipant || participant.Reason != ParticipantTerminationEnded || participant.Error != "" {
			t.Fatalf("long-conversation participant %q = %+v, want connected, %d turns, ended, and no error", participantID, participant, longConversationTurnsPerParticipant)
		}
		if participant.TerminationDisposition != ParticipantTerminationDispositionCompleted || participant.Classification != "" || participant.TerminalReason != string(messages.TerminalReasonProviderAuthoredCompletion) || participant.TerminalProvenance != string(messages.TerminalProvenanceProvider) || participant.OutputState != string(messages.TerminalOutputComplete) {
			t.Fatalf("long-conversation participant %q terminal metadata = %+v, want ordinary provider completion", participantID, participant)
		}
		if err := harness.participant(participantID).dialer.Err(); err != nil {
			t.Fatalf("long-conversation participant %q strict wire: %v", participantID, err)
		}
	}

	terminalCount := 0
	for terminalCount < len(manifest.Participants) {
		select {
		case terminal := <-participantTerminals:
			terminalCount++
			if terminal.Reason != ParticipantTerminationEnded || terminal.Error != "" {
				t.Fatalf("long-conversation terminal callback = %+v, want clean ended participant", terminal)
			}
		case <-roomCtx.Done():
			t.Fatalf("long-conversation participant terminal callbacks incomplete: %v", roomCtx.Err())
		}
	}

	failureCount := 0
	for {
		select {
		case diagnostic := <-diagnostics:
			if diagnostic.record.Event == SessionDiagnosticEventFailure {
				failureCount++
			}
		default:
			if failureCount != 0 {
				t.Fatalf("long-conversation session_failure diagnostics = %d, want none", failureCount)
			}
			goto diagnosticsDrained
		}
	}

diagnosticsDrained:
	manifestData := readRoomEvidenceFile(t, filepath.Join(outputDir, RoomEvidenceManifestPath))
	var evidenceManifest roomEvidenceManifest
	if err := json.Unmarshal(manifestData, &evidenceManifest); err != nil {
		t.Fatalf("decode long-conversation run manifest: %v", err)
	}
	if !evidenceManifest.Finalized || evidenceManifest.TerminationReason != RoomTerminationMaxTurnsReached || evidenceManifest.Reason != RoomTerminationMaxTurnsReached || evidenceManifest.Error != "" || evidenceManifest.Bounds.MaxTurns != longConversationTurnsPerParticipant {
		t.Fatalf("long-conversation run manifest = %+v, want finalized clean max-turn manifest", evidenceManifest)
	}
	for _, participantID := range []string{alphaID, betaID} {
		manifestParticipant, ok := evidenceManifest.Participants[participantID]
		if !ok {
			t.Fatalf("long-conversation manifest missing participant %q", participantID)
		}
		participant := outcome.result.Participants[participantID]
		if manifestParticipant.CompletedTurns != participant.TurnsCompleted || manifestParticipant.Connected != participant.Connected || manifestParticipant.TerminationReason != participant.TerminationReason || manifestParticipant.Reason != participant.Reason || manifestParticipant.TerminationDisposition != participant.TerminationDisposition || manifestParticipant.Classification != participant.Classification || manifestParticipant.TerminalReason != participant.TerminalReason || manifestParticipant.TerminalProvenance != participant.TerminalProvenance || manifestParticipant.OutputState != participant.OutputState || manifestParticipant.Error != participant.Error {
			t.Fatalf("long-conversation manifest participant %q = %+v, result = %+v", participantID, manifestParticipant, participant)
		}
		if evidenceManifest.TurnCounts[participantID] != longConversationTurnsPerParticipant {
			t.Fatalf("long-conversation manifest turn count for %q = %d, want %d", participantID, evidenceManifest.TurnCounts[participantID], longConversationTurnsPerParticipant)
		}
	}

	timelineLines := readRoomEvidenceJSONLLines(t, filepath.Join(outputDir, evidenceManifest.RoomTimeline))
	if len(timelineLines) == 0 {
		t.Fatal("long-conversation room timeline is empty")
	}
	timelineTurns := make([]string, 0, len(turnOrder))
	for index, line := range timelineLines {
		var entry roomTimelineEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("decode long-conversation timeline entry %d: %v", index+1, err)
		}
		if index > 0 {
			var prior roomTimelineEntry
			if err := json.Unmarshal(timelineLines[index-1], &prior); err != nil {
				t.Fatalf("decode prior long-conversation timeline entry %d: %v", index, err)
			}
			if entry.TOffsetMS < prior.TOffsetMS {
				t.Fatalf("long-conversation timeline moved backward at index %d: %v then %v", index, prior, entry)
			}
		}
		if entry.Event == "turn_completed" {
			timelineTurns = append(timelineTurns, entry.Participant)
		}
		if entry.Event == "provider_error" {
			t.Fatalf("long-conversation timeline contains provider error: %+v", entry)
		}
	}
	if !sameRoomReplayStrings(timelineTurns, turnOrder) {
		t.Fatalf("long-conversation timeline turn order = %v, want %v", timelineTurns, turnOrder)
	}
}

func roomRealtimeLongConversationCapture(t *testing.T, participantID, model string, input []byte, responses []string) gwtesting.SessionCapture {
	t.Helper()
	sequence := 1
	records := make([]gwtesting.CapturedSessionEvent, 0, 2+len(responses)*(longConversationFramesPerTurn+5))
	add := func(direction gwtesting.SessionEventDirection, eventType string, payload []byte) {
		records = append(records, roomRealtimeReplayEvent(sequence, direction, eventType, payload))
		sequence++
	}
	add(gwtesting.DirectionClientToServer, "session.update", roomRealtimeReplaySessionUpdate(t, model, participantID+" system"))
	add(gwtesting.DirectionServerToClient, "session.created", roomRealtimeReplayJSON(t, map[string]any{
		"type": "session.created", "session": map[string]any{"id": "sess-" + participantID + "-long", "model": model},
	}))
	inputBase64 := base64.StdEncoding.EncodeToString(input)
	for turnIndex, responseText := range responses {
		for range longConversationFramesPerTurn {
			add(gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
				"type": "input_audio_buffer.append", "audio": inputBase64,
			}))
		}
		responseID := fmt.Sprintf("resp-%s-long-%d", participantID, turnIndex+1)
		add(gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.created", "response": map[string]any{"id": responseID},
		}))
		add(gwtesting.DirectionServerToClient, "response.output_text.delta", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_text.delta", "response_id": responseID, "delta": responseText,
		}))
		add(gwtesting.DirectionServerToClient, "response.output_text.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_text.done", "response_id": responseID,
		}))
		add(gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.done", "response": map[string]any{"id": responseID, "status": "completed"},
		}))
	}
	return roomRealtimeReplayCapture(model, records...)
}
