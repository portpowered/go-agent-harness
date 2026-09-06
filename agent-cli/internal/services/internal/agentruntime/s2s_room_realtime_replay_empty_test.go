package agentruntime

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestRunRoomWithResult_EmptyResponseDoesNotAdvanceTurnLedger(t *testing.T) {
	const (
		peerID   = "peer"
		targetID = "target"
		model    = openAIRealtimeDefaultModel
	)

	silence := []byte{0, 0, 0, 0}
	silenceBase64 := base64.StdEncoding.EncodeToString(silence)
	peerText := "peer response"
	targetText := "target follow-up response"

	peerCapture := roomRealtimeReplayCapture(model,
		roomRealtimeReplayEvent(1, gwtesting.DirectionClientToServer, "session.update", roomRealtimeReplaySessionUpdate(t, model, "peer system")),
		roomRealtimeReplayEvent(2, gwtesting.DirectionServerToClient, "session.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "session.created", "session": map[string]any{"id": "sess-peer-empty", "model": model},
		})),
		roomRealtimeReplayEvent(3, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": silenceBase64,
		})),
		roomRealtimeReplayEvent(4, gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.created", "response": map[string]any{"id": "resp-peer-empty-test"},
		})),
		roomRealtimeReplayEvent(5, gwtesting.DirectionServerToClient, "response.output_text.delta", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_text.delta", "delta": peerText,
		})),
		roomRealtimeReplayEvent(6, gwtesting.DirectionServerToClient, "response.output_text.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_text.done",
		})),
		roomRealtimeReplayEvent(7, gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.done", "response": map[string]any{"id": "resp-peer-empty-test", "status": "completed"},
		})),
	)
	targetCapture := roomRealtimeReplayCapture(model,
		roomRealtimeReplayEvent(1, gwtesting.DirectionClientToServer, "session.update", roomRealtimeReplaySessionUpdate(t, model, "target system")),
		roomRealtimeReplayEvent(2, gwtesting.DirectionServerToClient, "session.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "session.created", "session": map[string]any{"id": "sess-target-empty-test", "model": model},
		})),
		// The first response has a complete provider lifecycle but no output
		// item or output delta. It must not satisfy the room's max-turn bound.
		roomRealtimeReplayEvent(3, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": silenceBase64,
		})),
		roomRealtimeReplayEvent(4, gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.created", "response": map[string]any{"id": "resp-target-empty-test"},
		})),
		roomRealtimeReplayEvent(5, gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.done", "response": map[string]any{"id": "resp-target-empty-test", "status": "completed", "output": []any{}},
		})),
		// A later input unlocks the contentful response. If the bare response
		// above is admitted as a turn, the room closes before this append.
		roomRealtimeReplayEvent(6, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": silenceBase64,
		})),
		roomRealtimeReplayEvent(7, gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.created", "response": map[string]any{"id": "resp-target-contentful-test"},
		})),
		roomRealtimeReplayEvent(8, gwtesting.DirectionServerToClient, "response.output_text.delta", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_text.delta", "delta": targetText,
		})),
		roomRealtimeReplayEvent(9, gwtesting.DirectionServerToClient, "response.output_text.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_text.done",
		})),
		roomRealtimeReplayEvent(10, gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.done", "response": map[string]any{"id": "resp-target-contentful-test", "status": "completed"},
		})),
	)
	harness := newRoomRealtimeReplayHarness(t, map[string]gwtesting.SessionCapture{
		peerID:   peerCapture,
		targetID: targetCapture,
	})

	configDir := t.TempDir()
	writeSessionConfigFile(t, configDir, "model:\n  provider: openai\n")
	credentials := map[string]string{
		"ROOM_EMPTY_PEER_KEY":   "room-empty-peer-key",
		"ROOM_EMPTY_TARGET_KEY": "room-empty-target-key",
	}
	manifest := room.Manifest{
		SchemaVersion: room.SchemaVersion,
		Room:          room.Room{MaxTurns: 1},
		Participants: []room.Participant{
			{ID: peerID, SystemPrompt: "peer system", Provider: config.ProviderOpenAI, Model: model, APIKeyEnv: "ROOM_EMPTY_PEER_KEY", Tools: []string{}},
			{ID: targetID, SystemPrompt: "target system", Provider: config.ProviderOpenAI, Model: model, APIKeyEnv: "ROOM_EMPTY_TARGET_KEY", Tools: []string{}},
		},
	}

	cadenceReady := make(chan *roomRealtimeReplayCadence, len(manifest.Participants))
	mixerConfig := room.PCM16MixerConfig{
		Format:            room.PCM16Format{SampleRate: 100, Channels: 1, FrameDuration: 20 * time.Millisecond},
		InputQueueFrames:  4,
		OutputQueueFrames: 4,
		CadenceFactory: func(time.Duration) room.PCM16Cadence {
			cadence := newRoomRealtimeReplayCadence()
			cadenceReady <- cadence
			return cadence
		},
	}
	opened := make(chan string, len(manifest.Participants))
	responseEnds := make(chan string, 4)
	turns := make(chan string, 4)
	roomCtx, cancel := context.WithTimeout(context.Background(), roomRealtimeReplayTestTimeout)
	defer cancel()

	opts := RoomRunOptions{
		Manifest:    manifest,
		ConfigDir:   configDir,
		BaseURL:     "wss://room-replay.invalid/v1/realtime",
		MixerConfig: mixerConfig,
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
		OnDiagnostic: func(participantID string, record SessionDiagnosticRecord) {
			if record.Event == SessionDiagnosticEventTurn {
				turns <- participantID
			}
		},
	}

	runDone := make(chan roomTestRunOutcome, 1)
	go func() {
		result, err := RunRoomWithResult(roomCtx, io.Discard, opts)
		runDone <- roomTestRunOutcome{result: result, err: err}
	}()

	var peerCadence, targetCadence *roomRealtimeReplayCadence
	select {
	case peerCadence = <-cadenceReady:
	case <-roomCtx.Done():
		t.Fatalf("peer empty-response cadence was not created: %v", roomCtx.Err())
	}
	select {
	case targetCadence = <-cadenceReady:
	case <-roomCtx.Done():
		t.Fatalf("target empty-response cadence was not created: %v", roomCtx.Err())
	}
	for range manifest.Participants {
		select {
		case <-opened:
		case <-roomCtx.Done():
			t.Fatalf("empty-response room sessions did not open: %v", roomCtx.Err())
		}
	}

	peerCadence.Advance()
	assertRoomRealtimeReplayAppend(t, harness.participant(peerID), silence)
	awaitRoomRealtimeReplayResponseEnd(t, responseEnds, peerID)

	targetCadence.Advance()
	assertRoomRealtimeReplayAppend(t, harness.participant(targetID), silence)
	awaitRoomRealtimeReplayResponseEnd(t, responseEnds, targetID)
	// The strict replayer holds the second target append behind the first
	// response.done. The following advance is therefore a causal release, not
	// a wall-clock wait.
	targetCadence.Advance()
	assertRoomRealtimeReplayAppend(t, harness.participant(targetID), silence)

	var outcome roomTestRunOutcome
	select {
	case outcome = <-runDone:
	case <-roomCtx.Done():
		t.Fatalf("empty-response room did not reach its explicit max-turn boundary: %v", roomCtx.Err())
	}
	if outcome.err != nil {
		t.Fatalf("empty-response room replay: %v", outcome.err)
	}
	if outcome.result.Reason != RoomTerminationMaxTurnsReached {
		t.Fatalf("empty-response room termination = %q, want %q", outcome.result.Reason, RoomTerminationMaxTurnsReached)
	}
	diagnosticCounts := map[string]int{}
	for range []int{0, 1} {
		select {
		case participantID := <-turns:
			diagnosticCounts[participantID]++
		case <-roomCtx.Done():
			t.Fatalf("empty-response diagnostics did not report both admitted turns: %v", roomCtx.Err())
		}
	}
	if diagnosticCounts[peerID] != 1 || diagnosticCounts[targetID] != 1 {
		t.Fatalf("empty-response diagnostic turns = %v, want one per participant and none for the empty response", diagnosticCounts)
	}
	for _, participantID := range []string{peerID, targetID} {
		participantResult, ok := outcome.result.Participants[participantID]
		if !ok {
			t.Fatalf("empty-response result missing participant %q", participantID)
		}
		if !participantResult.Connected || participantResult.TurnsCompleted != 1 || participantResult.TerminationReason != ParticipantTerminationEnded {
			t.Fatalf("empty-response participant %q result = %+v, want one admitted turn and clean end", participantID, participantResult)
		}
		if err := harness.participant(participantID).dialer.Err(); err != nil {
			t.Fatalf("empty-response participant %q strict wire: %v", participantID, err)
		}
	}

	targetWrites := harness.participant(targetID).outboundSnapshot()
	if len(targetWrites) != 3 || targetWrites[0].Type != "session.update" || targetWrites[1].Type != "input_audio_buffer.append" || targetWrites[2].Type != "input_audio_buffer.append" {
		t.Fatalf("target empty-response outbound wire = %+v, want session.update plus two input appends", targetWrites)
	}
	assertRoomRealtimeReplayWireAppend(t, targetWrites[1], silence)
	assertRoomRealtimeReplayWireAppend(t, targetWrites[2], silence)
	if got := harness.participant(targetID).inboundTypes(); !sameRoomReplayStrings(got, []string{
		"session.created", "response.created", "response.done",
		"response.created", "response.output_text.delta", "response.output_text.done", "response.done",
	}) {
		t.Fatalf("target empty-response inbound provider events = %v", got)
	}

}

func awaitRoomRealtimeReplayResponseEnd(t *testing.T, ends <-chan string, participantID string) {
	t.Helper()
	timer := time.NewTimer(roomRealtimeReplayTestTimeout)
	defer timer.Stop()
	for {
		select {
		case got := <-ends:
			if got == participantID {
				return
			}
		case <-timer.C:
			t.Fatalf("participant %q did not reach the scripted response.done boundary", participantID)
		}
	}
}

func assertRoomRealtimeReplayAppend(t *testing.T, participant *roomRealtimeReplayParticipant, wantPCM []byte) {
	t.Helper()
	message := participant.awaitOutbound(t, "input_audio_buffer.append")
	assertRoomRealtimeReplayWireAppend(t, message, wantPCM)
}

func assertRoomRealtimeReplayWireAppend(t *testing.T, message roomRealtimeReplayWireMessage, wantPCM []byte) {
	t.Helper()
	want := roomRealtimeReplayJSON(t, map[string]any{
		"type":  "input_audio_buffer.append",
		"audio": base64.StdEncoding.EncodeToString(wantPCM),
	})
	if message.Type != "input_audio_buffer.append" || !bytes.Equal(message.Payload, want) {
		t.Fatalf("raw input_audio_buffer.append = %s, want exact PCM %v", message.Payload, wantPCM)
	}
}
