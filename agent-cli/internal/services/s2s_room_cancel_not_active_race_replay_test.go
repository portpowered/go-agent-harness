package services

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
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

type roomCancelRaceAudio struct {
	participantID string
	pcm           []byte
}

type roomCancelRaceFanout struct {
	sourceID string
	targetID string
	pcm      []byte
}

type roomCancelRaceScenario struct {
	harness        *roomRealtimeReplayHarness
	speakerCadence *roomRealtimeReplayCadence
	targetCadence  *roomRealtimeReplayCadence
	audio          <-chan roomCancelRaceAudio
	fanouts        <-chan roomCancelRaceFanout
	speakerEnds    <-chan struct{}
	targetEnds     <-chan struct{}
	targetErrors   <-chan messages.StreamMessage
	diagnostic     <-chan string
	runDone        <-chan roomTestRunOutcome
	ctx            context.Context
}

func newRoomCancelRaceScenario(
	t *testing.T,
	captures map[string]gwtesting.SessionCapture,
	maxTurns int,
) *roomCancelRaceScenario {
	t.Helper()
	const (
		speakerID = "speaker"
		targetID  = "target"
	)

	harness := newRoomRealtimeReplayHarness(t, captures)
	configDir := t.TempDir()
	writeSessionConfigFile(t, configDir, "model:\n  provider: openai\n")
	credentials := map[string]string{
		"ROOM_CANCEL_RACE_SPEAKER_KEY": "room-cancel-race-speaker-key",
		"ROOM_CANCEL_RACE_TARGET_KEY":  "room-cancel-race-target-key",
	}
	manifest := room.Manifest{
		SchemaVersion: room.SchemaVersion,
		Room:          room.Room{MaxTurns: maxTurns},
		Participants: []room.Participant{
			{ID: speakerID, SystemPrompt: "speaker system", Provider: config.ProviderOpenAI, Model: openAIRealtimeDefaultModel, APIKeyEnv: "ROOM_CANCEL_RACE_SPEAKER_KEY", Tools: []string{}},
			{ID: targetID, SystemPrompt: "target system", Provider: config.ProviderOpenAI, Model: openAIRealtimeDefaultModel, APIKeyEnv: "ROOM_CANCEL_RACE_TARGET_KEY", Tools: []string{}},
		},
	}

	cadenceReady := make(chan *roomRealtimeReplayCadence, len(manifest.Participants))
	mixerConfig := room.PCM16MixerConfig{
		Format:            room.PCM16Format{SampleRate: 100, Channels: 1, FrameDuration: 20 * time.Millisecond},
		InputQueueFrames:  8,
		OutputQueueFrames: 8,
		CadenceFactory: func(time.Duration) room.PCM16Cadence {
			cadence := newRoomRealtimeReplayCadence()
			cadenceReady <- cadence
			return cadence
		},
	}
	opened := make(chan string, len(manifest.Participants))
	audio := make(chan roomCancelRaceAudio, 16)
	fanouts := make(chan roomCancelRaceFanout, 16)
	speakerEnds := make(chan struct{}, 8)
	targetEnds := make(chan struct{}, 8)
	targetErrors := make(chan messages.StreamMessage, 4)
	diagnostic := make(chan string, 8)
	roomCtx, cancel := context.WithTimeout(context.Background(), roomRealtimeReplayTestTimeout)
	t.Cleanup(cancel)

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
		onParticipantAudioFanned: func(sourceID, targetID string, pcm []byte) {
			fanouts <- roomCancelRaceFanout{
				sourceID: sourceID,
				targetID: targetID,
				pcm:      append([]byte(nil), pcm...),
			}
		},
		onParticipantStream: func(participantID string, msg messages.StreamMessage) {
			if participantID == targetID {
				switch msg.Type {
				case messages.StreamTypeMessageEnd:
					targetEnds <- struct{}{}
				case messages.StreamTypeError:
					targetErrors <- msg
				}
			} else if participantID == speakerID && msg.Type == messages.StreamTypeMessageEnd {
				speakerEnds <- struct{}{}
			}
		},
		OnAudioOutput: func(participantID string, pcm []byte) error {
			audio <- roomCancelRaceAudio{
				participantID: participantID,
				pcm:           append([]byte(nil), pcm...),
			}
			return nil
		},
		OnDiagnostic: func(participantID string, record SessionDiagnosticRecord) {
			if record.Event == SessionDiagnosticEventTurn {
				diagnostic <- participantID
			}
		},
	}
	runDone := make(chan roomTestRunOutcome, 1)
	go func() {
		result, err := RunRoomWithResult(roomCtx, io.Discard, opts)
		runDone <- roomTestRunOutcome{result: result, err: err}
	}()

	var speakerCadence, targetCadence *roomRealtimeReplayCadence
	select {
	case speakerCadence = <-cadenceReady:
	case <-roomCtx.Done():
		t.Fatalf("speaker cancel-race mixer cadence was not created: %v", roomCtx.Err())
	}
	select {
	case targetCadence = <-cadenceReady:
	case <-roomCtx.Done():
		t.Fatalf("target cancel-race mixer cadence was not created: %v", roomCtx.Err())
	}
	for range manifest.Participants {
		select {
		case <-opened:
		case <-roomCtx.Done():
			t.Fatalf("cancel-race room sessions did not open: %v", roomCtx.Err())
		}
	}

	return &roomCancelRaceScenario{
		harness:        harness,
		speakerCadence: speakerCadence,
		targetCadence:  targetCadence,
		audio:          audio,
		fanouts:        fanouts,
		speakerEnds:    speakerEnds,
		targetEnds:     targetEnds,
		targetErrors:   targetErrors,
		diagnostic:     diagnostic,
		runDone:        runDone,
		ctx:            roomCtx,
	}
}

func TestRunRoomWithResult_RealtimeTerminalBoundaryPrecedesPeerSpeech(t *testing.T) {
	const (
		speakerID = "speaker"
		targetID  = "target"
		model     = openAIRealtimeDefaultModel
	)

	silence := []byte{0, 0, 0, 0}
	targetFirstPCM := roomReplayOnTargetPCM()
	speakerSpeech := roomReplayOnTargetPCM()
	targetNextPCM := roomReplayOnTargetPCM()
	silenceBase64 := base64.StdEncoding.EncodeToString(silence)
	targetFirstBase64 := base64.StdEncoding.EncodeToString(targetFirstPCM)
	speakerSpeechBase64 := base64.StdEncoding.EncodeToString(speakerSpeech)
	targetNextBase64 := base64.StdEncoding.EncodeToString(targetNextPCM)

	speakerCapture := roomRealtimeReplayCapture(model,
		roomRealtimeReplayEvent(1, gwtesting.DirectionClientToServer, "session.update", roomRealtimeReplaySessionUpdate(t, model, "speaker system")),
		roomRealtimeReplayEvent(2, gwtesting.DirectionServerToClient, "session.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "session.created", "session": map[string]any{"id": "sess-terminal-speaker", "model": model},
		})),
		roomRealtimeReplayEvent(3, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": targetFirstBase64,
		})),
		roomRealtimeReplayEvent(4, gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.created", "response": map[string]any{"id": "resp-terminal-speaker-1"},
		})),
		roomRealtimeReplayEvent(5, gwtesting.DirectionServerToClient, "response.output_audio.delta", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_audio.delta", "response_id": "resp-terminal-speaker-1", "delta": speakerSpeechBase64,
		})),
		roomRealtimeReplayEvent(6, gwtesting.DirectionServerToClient, "response.output_audio.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_audio.done", "response_id": "resp-terminal-speaker-1",
		})),
		roomRealtimeReplayEvent(7, gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.done", "response": map[string]any{"id": "resp-terminal-speaker-1", "status": "completed"},
		})),
		roomRealtimeReplayEvent(8, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": targetNextBase64,
		})),
		roomRealtimeReplayEvent(9, gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.created", "response": map[string]any{"id": "resp-terminal-speaker-2"},
		})),
		roomRealtimeReplayEvent(10, gwtesting.DirectionServerToClient, "response.output_text.delta", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_text.delta", "response_id": "resp-terminal-speaker-2", "delta": "speaker follow-up",
		})),
		roomRealtimeReplayEvent(11, gwtesting.DirectionServerToClient, "response.output_text.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_text.done", "response_id": "resp-terminal-speaker-2",
		})),
		roomRealtimeReplayEvent(12, gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.done", "response": map[string]any{"id": "resp-terminal-speaker-2", "status": "completed"},
		})),
	)
	targetCapture := roomRealtimeReplayCapture(model,
		roomRealtimeReplayEvent(1, gwtesting.DirectionClientToServer, "session.update", roomRealtimeReplaySessionUpdate(t, model, "target system")),
		roomRealtimeReplayEvent(2, gwtesting.DirectionServerToClient, "session.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "session.created", "session": map[string]any{"id": "sess-terminal-target", "model": model},
		})),
		roomRealtimeReplayEvent(3, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": silenceBase64,
		})),
		roomRealtimeReplayEvent(4, gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.created", "response": map[string]any{"id": "resp-terminal-target-1"},
		})),
		roomRealtimeReplayEvent(5, gwtesting.DirectionServerToClient, "response.output_audio.delta", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_audio.delta", "response_id": "resp-terminal-target-1", "delta": targetFirstBase64,
		})),
		roomRealtimeReplayEvent(6, gwtesting.DirectionServerToClient, "response.output_audio.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_audio.done", "response_id": "resp-terminal-target-1",
		})),
		// MESSAGE.END is delivered before the next peer speech frame is
		// admitted. The target wire therefore has no response.cancel slot.
		roomRealtimeReplayEvent(7, gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.done", "response": map[string]any{"id": "resp-terminal-target-1", "status": "completed"},
		})),
		roomRealtimeReplayEvent(8, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": speakerSpeechBase64,
		})),
		roomRealtimeReplayEvent(9, gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.created", "response": map[string]any{"id": "resp-terminal-target-2"},
		})),
		roomRealtimeReplayEvent(10, gwtesting.DirectionServerToClient, "response.output_audio.delta", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_audio.delta", "response_id": "resp-terminal-target-2", "delta": targetNextBase64,
		})),
		roomRealtimeReplayEvent(11, gwtesting.DirectionServerToClient, "response.output_audio.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_audio.done", "response_id": "resp-terminal-target-2",
		})),
		roomRealtimeReplayEvent(12, gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.done", "response": map[string]any{"id": "resp-terminal-target-2", "status": "completed"},
		})),
	)
	scenario := newRoomCancelRaceScenario(t, map[string]gwtesting.SessionCapture{
		speakerID: speakerCapture,
		targetID:  targetCapture,
	}, 2)

	scenario.targetCadence.Advance()
	assertRoomRealtimeReplayAppend(t, scenario.harness.participant(targetID), silence)
	awaitRoomCancelRaceAudio(t, scenario.audio, targetID, targetFirstPCM)
	awaitRoomCancelRaceFanout(t, scenario.fanouts, targetID, speakerID, targetFirstPCM)
	awaitRoomCancelRaceTargetEnd(t, scenario.targetEnds)

	// The peer's speech is admitted only after the target MESSAGE.END has
	// become observable. It must be forwarded directly for the next response.
	scenario.speakerCadence.Advance()
	assertRoomRealtimeReplayAppend(t, scenario.harness.participant(speakerID), targetFirstPCM)
	awaitRoomCancelRaceAudio(t, scenario.audio, speakerID, speakerSpeech)
	awaitRoomCancelRaceFanout(t, scenario.fanouts, speakerID, targetID, speakerSpeech)
	awaitRoomCancelRaceEnd(t, scenario.speakerEnds, "speaker")
	scenario.targetCadence.Advance()
	assertRoomRealtimeReplayAppend(t, scenario.harness.participant(targetID), speakerSpeech)
	awaitRoomCancelRaceAudio(t, scenario.audio, targetID, targetNextPCM)
	awaitRoomCancelRaceFanout(t, scenario.fanouts, targetID, speakerID, targetNextPCM)
	awaitRoomCancelRaceTargetEnd(t, scenario.targetEnds)

	// The target's second response is complete; release the peer's second
	// input so both participants reach the explicit max-turn boundary.
	scenario.speakerCadence.Advance()
	assertRoomRealtimeReplayAppend(t, scenario.harness.participant(speakerID), targetNextPCM)

	outcome := awaitRoomCancelRaceRun(t, scenario)
	if outcome.err != nil {
		t.Fatalf("terminal-boundary room replay: %v", outcome.err)
	}
	if outcome.result.Reason != RoomTerminationMaxTurnsReached {
		t.Fatalf("terminal-boundary room termination = %q, want %q", outcome.result.Reason, RoomTerminationMaxTurnsReached)
	}
	assertRoomCancelRaceParticipant(t, outcome.result.Participants, speakerID, 2)
	assertRoomCancelRaceParticipant(t, outcome.result.Participants, targetID, 2)
	assertRoomCancelRaceDiagnostics(t, scenario.diagnostic, map[string]int{speakerID: 2, targetID: 2}, scenario.ctx)

	targetWrites := scenario.harness.participant(targetID).outboundSnapshot()
	if got := roomCancelRaceWireTypes(targetWrites); !sameRoomReplayStrings(got, []string{
		"session.update", "input_audio_buffer.append", "input_audio_buffer.append",
	}) {
		t.Fatalf("terminal-boundary target outbound types = %v, want no response.cancel", got)
	}
	assertRoomRealtimeReplayWireAppend(t, targetWrites[1], silence)
	assertRoomRealtimeReplayWireAppend(t, targetWrites[2], speakerSpeech)
	if got := countRoomCancelRaceWireType(targetWrites, "response.cancel"); got != 0 {
		t.Fatalf("terminal-boundary target response.cancel count = %d, want zero", got)
	}
	for _, participantID := range []string{speakerID, targetID} {
		if err := scenario.harness.participant(participantID).dialer.Err(); err != nil {
			t.Fatalf("terminal-boundary participant %q strict wire: %v", participantID, err)
		}
	}
}

func TestRunRoomWithResult_RealtimeInactiveCancelKeepsParticipantAlive(t *testing.T) {
	const (
		speakerID = "speaker"
		targetID  = "target"
		model     = openAIRealtimeDefaultModel
	)

	silence := []byte{0, 0, 0, 0}
	targetFirstPCM := roomReplayOnTargetPCM()
	speakerSpeech := roomReplayOnTargetPCM()
	silenceBase64 := base64.StdEncoding.EncodeToString(silence)
	targetFirstBase64 := base64.StdEncoding.EncodeToString(targetFirstPCM)
	speakerSpeechBase64 := base64.StdEncoding.EncodeToString(speakerSpeech)

	speakerCapture := roomRealtimeReplayCapture(model,
		roomRealtimeReplayEvent(1, gwtesting.DirectionClientToServer, "session.update", roomRealtimeReplaySessionUpdate(t, model, "speaker system")),
		roomRealtimeReplayEvent(2, gwtesting.DirectionServerToClient, "session.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "session.created", "session": map[string]any{"id": "sess-inactive-speaker", "model": model},
		})),
		roomRealtimeReplayEvent(3, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": targetFirstBase64,
		})),
		roomRealtimeReplayEvent(4, gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.created", "response": map[string]any{"id": "resp-inactive-speaker"},
		})),
		roomRealtimeReplayEvent(5, gwtesting.DirectionServerToClient, "response.output_audio.delta", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_audio.delta", "response_id": "resp-inactive-speaker", "delta": speakerSpeechBase64,
		})),
		roomRealtimeReplayEvent(6, gwtesting.DirectionServerToClient, "response.output_audio.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_audio.done", "response_id": "resp-inactive-speaker",
		})),
		roomRealtimeReplayEvent(7, gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.done", "response": map[string]any{"id": "resp-inactive-speaker", "status": "completed"},
		})),
	)
	targetCapture := roomRealtimeReplayCapture(model,
		roomRealtimeReplayEvent(1, gwtesting.DirectionClientToServer, "session.update", roomRealtimeReplaySessionUpdate(t, model, "target system")),
		roomRealtimeReplayEvent(2, gwtesting.DirectionServerToClient, "session.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "session.created", "session": map[string]any{"id": "sess-inactive-target", "model": model},
		})),
		roomRealtimeReplayEvent(3, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": silenceBase64,
		})),
		roomRealtimeReplayEvent(4, gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.created", "response": map[string]any{"id": "resp-inactive-target-1"},
		})),
		roomRealtimeReplayEvent(5, gwtesting.DirectionServerToClient, "response.output_audio.delta", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_audio.delta", "response_id": "resp-inactive-target-1", "delta": targetFirstBase64,
		})),
		roomRealtimeReplayEvent(6, gwtesting.DirectionClientToServer, "response.cancel", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.cancel",
		})),
		roomRealtimeReplayEvent(7, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": speakerSpeechBase64,
		})),
		roomRealtimeReplayEvent(8, gwtesting.DirectionServerToClient, "error", roomRealtimeReplayJSON(t, map[string]any{
			"type":  "error",
			"error": map[string]any{"type": "invalid_request_error", "code": "response_cancel_not_active", "message": "Cannot cancel a response that is not active.", "param": "response.cancel", "event_id": "evt-inactive-cancel-race"},
		})),
		// The error is non-terminal, so the next cadence frame reaches the
		// same session without another cancel and opens a normal response.
		roomRealtimeReplayEvent(9, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": silenceBase64,
		})),
		roomRealtimeReplayEvent(10, gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.created", "response": map[string]any{"id": "resp-inactive-target-2"},
		})),
		roomRealtimeReplayEvent(11, gwtesting.DirectionServerToClient, "response.output_text.delta", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_text.delta", "response_id": "resp-inactive-target-2", "delta": "target follow-up survived",
		})),
		roomRealtimeReplayEvent(12, gwtesting.DirectionServerToClient, "response.output_text.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_text.done", "response_id": "resp-inactive-target-2",
		})),
		roomRealtimeReplayEvent(13, gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.done", "response": map[string]any{"id": "resp-inactive-target-2", "status": "completed"},
		})),
	)
	scenario := newRoomCancelRaceScenario(t, map[string]gwtesting.SessionCapture{
		speakerID: speakerCapture,
		targetID:  targetCapture,
	}, 1)

	scenario.targetCadence.Advance()
	assertRoomRealtimeReplayAppend(t, scenario.harness.participant(targetID), silence)
	awaitRoomCancelRaceAudio(t, scenario.audio, targetID, targetFirstPCM)
	awaitRoomCancelRaceFanout(t, scenario.fanouts, targetID, speakerID, targetFirstPCM)

	scenario.speakerCadence.Advance()
	assertRoomRealtimeReplayAppend(t, scenario.harness.participant(speakerID), targetFirstPCM)
	awaitRoomCancelRaceAudio(t, scenario.audio, speakerID, speakerSpeech)
	awaitRoomCancelRaceFanout(t, scenario.fanouts, speakerID, targetID, speakerSpeech)

	scenario.targetCadence.Advance()
	cancelMessage := scenario.harness.participant(targetID).awaitOutbound(t, "response.cancel")
	if !bytes.Equal(cancelMessage.Payload, roomRealtimeReplayJSON(t, map[string]any{"type": "response.cancel"})) {
		t.Fatalf("inactive-cancel response.cancel payload = %s, want exact response.cancel", cancelMessage.Payload)
	}
	assertRoomRealtimeReplayAppend(t, scenario.harness.participant(targetID), speakerSpeech)

	var diagnosticMessage messages.StreamMessage
	select {
	case diagnosticMessage = <-scenario.targetErrors:
	case <-scenario.ctx.Done():
		t.Fatalf("inactive-cancel diagnostic was not observed: %v", scenario.ctx.Err())
	}
	value, ok := diagnosticMessage.Value.(*messages.ErrorValue)
	if !ok || value == nil {
		t.Fatalf("inactive-cancel room diagnostic value = %T, want *messages.ErrorValue", diagnosticMessage.Value)
	}
	if value.IsTerminal() || value.Classification != providers.ErrorClassResponseCancelNotActive ||
		value.ErrorType != "invalid_request_error" || value.Code != "response_cancel_not_active" ||
		value.Param != "response.cancel" || value.EventID != "evt-inactive-cancel-race" {
		t.Fatalf("inactive-cancel room diagnostic = %#v", value)
	}

	// This append is the next logical turn. The strict target script would
	// diverge if the non-terminal diagnostic had killed the session or caused
	// a duplicate cancel.
	scenario.targetCadence.Advance()
	assertRoomRealtimeReplayAppend(t, scenario.harness.participant(targetID), silence)
	awaitRoomCancelRaceTargetEnd(t, scenario.targetEnds)

	outcome := awaitRoomCancelRaceRun(t, scenario)
	if outcome.err != nil {
		t.Fatalf("inactive-cancel room replay: %v", outcome.err)
	}
	if outcome.result.Reason != RoomTerminationMaxTurnsReached {
		t.Fatalf("inactive-cancel room termination = %q, want %q", outcome.result.Reason, RoomTerminationMaxTurnsReached)
	}
	assertRoomCancelRaceParticipant(t, outcome.result.Participants, speakerID, 1)
	assertRoomCancelRaceParticipant(t, outcome.result.Participants, targetID, 1)
	assertRoomCancelRaceDiagnostics(t, scenario.diagnostic, map[string]int{speakerID: 1, targetID: 1}, scenario.ctx)

	targetWrites := scenario.harness.participant(targetID).outboundSnapshot()
	wantTypes := []string{
		"session.update", "input_audio_buffer.append", "response.cancel",
		"input_audio_buffer.append", "input_audio_buffer.append",
	}
	if got := roomCancelRaceWireTypes(targetWrites); !sameRoomReplayStrings(got, wantTypes) {
		t.Fatalf("inactive-cancel target outbound types = %v, want %v", got, wantTypes)
	}
	assertRoomRealtimeReplayWireAppend(t, targetWrites[1], silence)
	assertRoomRealtimeReplayWireAppend(t, targetWrites[3], speakerSpeech)
	assertRoomRealtimeReplayWireAppend(t, targetWrites[4], silence)
	if got := countRoomCancelRaceWireType(targetWrites, "response.cancel"); got != 1 {
		t.Fatalf("inactive-cancel target response.cancel count = %d, want one", got)
	}
	if got := scenario.harness.participant(targetID).inboundTypes(); !sameRoomReplayStrings(got, []string{
		"session.created", "response.created", "response.output_audio.delta", "error",
		"response.created", "response.output_text.delta", "response.output_text.done", "response.done",
	}) {
		t.Fatalf("inactive-cancel target inbound provider events = %v", got)
	}
	for _, participantID := range []string{speakerID, targetID} {
		if err := scenario.harness.participant(participantID).dialer.Err(); err != nil {
			t.Fatalf("inactive-cancel participant %q strict wire: %v", participantID, err)
		}
	}
}

func awaitRoomCancelRaceAudio(t *testing.T, audio <-chan roomCancelRaceAudio, participantID string, want []byte) {
	t.Helper()
	timer := time.NewTimer(roomRealtimeReplayTestTimeout)
	defer timer.Stop()
	for {
		select {
		case got := <-audio:
			if got.participantID != participantID {
				continue
			}
			if !bytes.Equal(got.pcm, want) {
				t.Fatalf("cancel-race %s output PCM = %v, want %v", participantID, got.pcm, want)
			}
			return
		case <-timer.C:
			t.Fatalf("cancel-race participant %q did not emit output PCM %v", participantID, want)
		}
	}
}

func awaitRoomCancelRaceFanout(t *testing.T, fanouts <-chan roomCancelRaceFanout, sourceID, targetID string, want []byte) {
	t.Helper()
	timer := time.NewTimer(roomRealtimeReplayTestTimeout)
	defer timer.Stop()
	for {
		select {
		case got := <-fanouts:
			if got.sourceID != sourceID || got.targetID != targetID {
				continue
			}
			if !bytes.Equal(got.pcm, want) {
				t.Fatalf("cancel-race fanout %s -> %s PCM = %v, want %v", sourceID, targetID, got.pcm, want)
			}
			return
		case <-timer.C:
			t.Fatalf("cancel-race fanout %s -> %s did not carry PCM %v", sourceID, targetID, want)
		}
	}
}

func awaitRoomCancelRaceTargetEnd(t *testing.T, ends <-chan struct{}) {
	awaitRoomCancelRaceEnd(t, ends, "target")
}

func awaitRoomCancelRaceEnd(t *testing.T, ends <-chan struct{}, participantID string) {
	t.Helper()
	timer := time.NewTimer(roomRealtimeReplayTestTimeout)
	defer timer.Stop()
	select {
	case <-ends:
	case <-timer.C:
		t.Fatalf("cancel-race %s did not observe MESSAGE.END", participantID)
	}
}

func awaitRoomCancelRaceRun(t *testing.T, scenario *roomCancelRaceScenario) roomTestRunOutcome {
	t.Helper()
	select {
	case outcome := <-scenario.runDone:
		return outcome
	case <-scenario.ctx.Done():
		t.Fatalf("cancel-race room did not reach its explicit boundary: %v", scenario.ctx.Err())
		return roomTestRunOutcome{}
	}
}

func assertRoomCancelRaceParticipant(t *testing.T, participants map[string]RoomParticipantResult, participantID string, wantTurns int) {
	t.Helper()
	result, ok := participants[participantID]
	if !ok {
		t.Fatalf("cancel-race result missing participant %q", participantID)
	}
	if !result.Connected || result.TurnsCompleted != wantTurns || result.TerminationReason != ParticipantTerminationEnded || result.Error != "" {
		t.Fatalf("cancel-race participant %q result = %+v, want %d clean turns", participantID, result, wantTurns)
	}
}

func assertRoomCancelRaceDiagnostics(t *testing.T, diagnostic <-chan string, want map[string]int, ctx context.Context) {
	t.Helper()
	counts := make(map[string]int, len(want))
	total := 0
	for _, count := range want {
		total += count
	}
	for range total {
		select {
		case participantID := <-diagnostic:
			counts[participantID]++
		case <-ctx.Done():
			t.Fatalf("cancel-race turn diagnostics = %v, want %v: %v", counts, want, ctx.Err())
		}
	}
	for participantID, count := range want {
		if counts[participantID] != count {
			t.Fatalf("cancel-race participant %q diagnostics = %d, want %d", participantID, counts[participantID], count)
		}
	}
}

func roomCancelRaceWireTypes(writes []roomRealtimeReplayWireMessage) []string {
	types := make([]string, 0, len(writes))
	for _, write := range writes {
		types = append(types, write.Type)
	}
	return types
}

func countRoomCancelRaceWireType(writes []roomRealtimeReplayWireMessage, eventType string) int {
	count := 0
	for _, write := range writes {
		if write.Type == eventType {
			count++
		}
	}
	return count
}
