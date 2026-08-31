package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

type roomSpeechOverlapFanout struct {
	sourceID string
	targetID string
	pcm      []byte
}

type roomSpeechOverlapScenario struct {
	harness       *roomRealtimeReplayHarness
	peerCadence   *roomRealtimeReplayCadence
	targetCadence *roomRealtimeReplayCadence
	targetAudio   <-chan []byte
	targetInput   <-chan []byte
	fanouts       <-chan roomSpeechOverlapFanout
	diagnostic    <-chan string
	speakerEnds   <-chan struct{}
	targetEnds    <-chan struct{}
	runDone       <-chan roomTestRunOutcome
	ctx           context.Context
	cancel        context.CancelFunc

	silence        []byte
	expectedSpeech []byte
	peerOutput     []byte
	targetOutput   []byte
	followupOutput []byte
}

func newRoomSpeechOverlapScenario(t *testing.T, peerOutput []byte) *roomSpeechOverlapScenario {
	t.Helper()
	const (
		peerID   = "speaker"
		targetID = "target"
		model    = openAIRealtimeDefaultModel
	)

	silence := []byte{0, 0, 0, 0}
	expectedSpeech := []byte{0x20, 0x03, 0xe0, 0xfc}
	targetOutput := []byte{0x34, 0x12, 0x78, 0x56}
	followupOutput := []byte{0x56, 0x34, 0x12, 0x78}
	peerOutput = append([]byte(nil), peerOutput...)
	silenceBase64 := base64.StdEncoding.EncodeToString(silence)
	expectedSpeechBase64 := base64.StdEncoding.EncodeToString(expectedSpeech)
	targetOutputBase64 := base64.StdEncoding.EncodeToString(targetOutput)
	peerOutputBase64 := base64.StdEncoding.EncodeToString(peerOutput)
	followupOutputBase64 := base64.StdEncoding.EncodeToString(followupOutput)

	peerCapture := roomRealtimeReplayCapture(model,
		roomRealtimeReplayEvent(1, gwtesting.DirectionClientToServer, "session.update", roomRealtimeReplaySessionUpdate(t, model, "peer system")),
		roomRealtimeReplayEvent(2, gwtesting.DirectionServerToClient, "session.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "session.created", "session": map[string]any{"id": "sess-peer-overlap", "model": model},
		})),
		// The peer receives the target's first response through its real mixer
		// before its scripted response emits the overlapping speech frames.
		roomRealtimeReplayEvent(3, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": targetOutputBase64,
		})),
		roomRealtimeReplayEvent(4, gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.created", "response": map[string]any{"id": "resp-peer-overlap"},
		})),
		roomRealtimeReplayEvent(5, gwtesting.DirectionServerToClient, "response.output_audio.delta", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_audio.delta", "response_id": "resp-peer-overlap", "delta": peerOutputBase64,
		})),
		roomRealtimeReplayEvent(6, gwtesting.DirectionServerToClient, "response.output_audio.delta", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_audio.delta", "response_id": "resp-peer-overlap", "delta": peerOutputBase64,
		})),
		roomRealtimeReplayEvent(7, gwtesting.DirectionServerToClient, "response.output_audio.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_audio.done", "response_id": "resp-peer-overlap",
		})),
		roomRealtimeReplayEvent(8, gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.done", "response": map[string]any{"id": "resp-peer-overlap", "status": "completed"},
		})),
	)
	targetCapture := roomRealtimeReplayCapture(model,
		roomRealtimeReplayEvent(1, gwtesting.DirectionClientToServer, "session.update", roomRealtimeReplaySessionUpdate(t, model, "target system")),
		roomRealtimeReplayEvent(2, gwtesting.DirectionServerToClient, "session.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "session.created", "session": map[string]any{"id": "sess-target-overlap", "model": model},
		})),
		// This first idle frame establishes the active response without
		// creating a cancellation candidate.
		roomRealtimeReplayEvent(3, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": silenceBase64,
		})),
		roomRealtimeReplayEvent(4, gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.created", "response": map[string]any{"id": "resp-target-overlap-1"},
		})),
		roomRealtimeReplayEvent(5, gwtesting.DirectionServerToClient, "response.output_audio.delta", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_audio.delta", "response_id": "resp-target-overlap-1", "delta": targetOutputBase64,
		})),
		// The first contentful overlap must cancel the active response before
		// its append. The second append remains in this same overlap and must
		// not produce another cancellation.
		roomRealtimeReplayEvent(6, gwtesting.DirectionClientToServer, "response.cancel", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.cancel",
		})),
		roomRealtimeReplayEvent(7, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": expectedSpeechBase64,
		})),
		roomRealtimeReplayEvent(8, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": expectedSpeechBase64,
		})),
		// A cancelled provider response still has a wire terminal event. The
		// runtime must keep the boundary but exclude it from completed turns.
		roomRealtimeReplayEvent(9, gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.done", "response": map[string]any{"id": "resp-target-overlap-1", "status": "cancelled"},
		})),
		// A later idle frame starts a normal response so max-turn shutdown is
		// an explicit room boundary rather than timeout or transport EOF.
		roomRealtimeReplayEvent(10, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": silenceBase64,
		})),
		roomRealtimeReplayEvent(11, gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.created", "response": map[string]any{"id": "resp-target-overlap-2"},
		})),
		roomRealtimeReplayEvent(12, gwtesting.DirectionServerToClient, "response.output_audio.delta", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_audio.delta", "response_id": "resp-target-overlap-2", "delta": followupOutputBase64,
		})),
		roomRealtimeReplayEvent(13, gwtesting.DirectionServerToClient, "response.output_audio.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_audio.done", "response_id": "resp-target-overlap-2",
		})),
		roomRealtimeReplayEvent(14, gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.done", "response": map[string]any{"id": "resp-target-overlap-2", "status": "completed"},
		})),
	)
	harness := newRoomRealtimeReplayHarness(t, map[string]gwtesting.SessionCapture{
		peerID:   peerCapture,
		targetID: targetCapture,
	})

	configDir := t.TempDir()
	writeSessionConfigFile(t, configDir, "model:\n  provider: openai\n")
	credentials := map[string]string{
		"ROOM_SPEAKER_KEY": "room-speaker-overlap-key",
		"ROOM_TARGET_KEY":  "room-target-overlap-key",
	}
	manifest := room.Manifest{
		SchemaVersion: room.SchemaVersion,
		Room:          room.Room{MaxTurns: 1},
		Participants: []room.Participant{
			{ID: peerID, SystemPrompt: "peer system", Provider: config.ProviderOpenAI, Model: model, APIKeyEnv: "ROOM_SPEAKER_KEY", Tools: []string{}},
			{ID: targetID, SystemPrompt: "target system", Provider: config.ProviderOpenAI, Model: model, APIKeyEnv: "ROOM_TARGET_KEY", Tools: []string{}},
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
	targetAudio := make(chan []byte, 8)
	targetInput := make(chan []byte, 8)
	fanouts := make(chan roomSpeechOverlapFanout, 16)
	diagnosticTurns := make(chan string, 8)
	opened := make(chan string, len(manifest.Participants))
	speakerEnds := make(chan struct{}, 4)
	targetEnds := make(chan struct{}, 4)
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
			// The startup barrier below observes this through the channel so no
			// cadence can be released before all real sessions are admitted.
			opened <- participantID
		},
		onParticipantAudioFanned: func(sourceID, targetID string, pcm []byte) {
			fanouts <- roomSpeechOverlapFanout{
				sourceID: sourceID,
				targetID: targetID,
				pcm:      append([]byte(nil), pcm...),
			}
		},
		onParticipantStream: func(participantID string, msg messages.StreamMessage) {
			if participantID == peerID && msg.Type == messages.StreamTypeMessageEnd {
				speakerEnds <- struct{}{}
			}
			if participantID == targetID && msg.Type == messages.StreamTypeMessageEnd {
				targetEnds <- struct{}{}
			}
		},
		OnAudioOutput: func(participantID string, pcm []byte) error {
			if participantID == targetID {
				targetAudio <- append([]byte(nil), pcm...)
			}
			return nil
		},
		OnAudioInput: func(participantID string, pcm []byte) error {
			if participantID == targetID {
				targetInput <- append([]byte(nil), pcm...)
			}
			return nil
		},
		OnDiagnostic: func(participantID string, record SessionDiagnosticRecord) {
			if record.Event == SessionDiagnosticEventTurn {
				diagnosticTurns <- participantID
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
		t.Fatalf("peer overlap mixer cadence was not created: %v", roomCtx.Err())
	}
	select {
	case targetCadence = <-cadenceReady:
	case <-roomCtx.Done():
		t.Fatalf("target overlap mixer cadence was not created: %v", roomCtx.Err())
	}
	for range manifest.Participants {
		select {
		case <-opened:
		case <-roomCtx.Done():
			t.Fatalf("overlap room sessions did not open: %v", roomCtx.Err())
		}
	}

	return &roomSpeechOverlapScenario{
		harness:        harness,
		peerCadence:    peerCadence,
		targetCadence:  targetCadence,
		targetAudio:    targetAudio,
		targetInput:    targetInput,
		fanouts:        fanouts,
		diagnostic:     diagnosticTurns,
		speakerEnds:    speakerEnds,
		targetEnds:     targetEnds,
		runDone:        runDone,
		ctx:            roomCtx,
		cancel:         cancel,
		silence:        silence,
		expectedSpeech: expectedSpeech,
		peerOutput:     peerOutput,
		targetOutput:   targetOutput,
		followupOutput: followupOutput,
	}
}

func TestRunRoomWithResult_SpeechOverlapCancelsExactlyOnce(t *testing.T) {
	t.Run("speech overlap", func(t *testing.T) {
		scenario := newRoomSpeechOverlapScenario(t, []byte{0x20, 0x03, 0xe0, 0xfc})

		scenario.targetCadence.Advance()
		assertRoomSpeechOverlapAppend(t, scenario.harness.participant("target"), scenario.silence)
		awaitRoomSpeechOverlapInput(t, scenario.targetInput, scenario.silence)
		awaitRoomSpeechOverlapAudio(t, scenario.targetAudio, scenario.targetOutput)
		awaitRoomSpeechOverlapFanout(t, scenario.fanouts, "target", "speaker", scenario.targetOutput)

		// The speaker's first mixer frame is the target's response audio. Its
		// scripted response then emits two speech-shaped frames to the target.
		scenario.peerCadence.Advance()
		assertRoomSpeechOverlapAppend(t, scenario.harness.participant("speaker"), scenario.targetOutput)
		awaitRoomSpeechOverlapFanout(t, scenario.fanouts, "speaker", "target", scenario.peerOutput)
		awaitRoomSpeechOverlapFanout(t, scenario.fanouts, "speaker", "target", scenario.peerOutput)
		// Establish the peer's provider terminal boundary before advancing the
		// target overlap so replay teardown remains causally ordered.
		awaitRoomSpeechOverlapMessageEnd(t, scenario.speakerEnds, "speaker")

		scenario.targetCadence.Advance()
		cancelMessage := scenario.harness.participant("target").awaitOutbound(t, "response.cancel")
		if !bytes.Equal(cancelMessage.Payload, roomRealtimeReplayJSON(t, map[string]any{"type": "response.cancel"})) {
			t.Fatalf("target response.cancel payload = %s, want exact response.cancel", cancelMessage.Payload)
		}
		assertRoomSpeechOverlapAppend(t, scenario.harness.participant("target"), scenario.expectedSpeech)

		// A second contentful frame from the same overlap must be forwarded
		// without another RESPONSE.CANCEL.
		scenario.targetCadence.Advance()
		assertRoomSpeechOverlapAppend(t, scenario.harness.participant("target"), scenario.expectedSpeech)

		// The cancelled response.done is consumed before this idle frame opens
		// the normal follow-up response that reaches the room max-turn boundary.
		awaitRoomSpeechOverlapTargetEnd(t, scenario.targetEnds)
		scenario.targetCadence.Advance()
		assertRoomSpeechOverlapAppend(t, scenario.harness.participant("target"), scenario.silence)
		awaitRoomSpeechOverlapAudio(t, scenario.targetAudio, scenario.followupOutput)

		outcome := awaitRoomSpeechOverlapRun(t, scenario)
		if outcome.err != nil {
			t.Fatalf("speech-overlap room replay: %v", outcome.err)
		}
		if outcome.result.Reason != RoomTerminationMaxTurnsReached {
			t.Fatalf("speech-overlap room termination = %q, want %q", outcome.result.Reason, RoomTerminationMaxTurnsReached)
		}
		for _, participantID := range []string{"speaker", "target"} {
			participantResult, ok := outcome.result.Participants[participantID]
			if !ok {
				t.Fatalf("speech-overlap result missing participant %q", participantID)
			}
			if !participantResult.Connected || participantResult.TurnsCompleted != 1 {
				t.Fatalf("speech-overlap participant %q result = %+v, want one normal completed turn", participantID, participantResult)
			}
			if err := scenario.harness.participant(participantID).dialer.Err(); err != nil {
				t.Fatalf("speech-overlap participant %q strict wire: %v", participantID, err)
			}
		}

		diagnosticCounts := map[string]int{}
		for range []int{0, 1} {
			select {
			case participantID := <-scenario.diagnostic:
				diagnosticCounts[participantID]++
			case <-scenario.ctx.Done():
				t.Fatalf("speech-overlap diagnostics did not report both normal turns: %v", scenario.ctx.Err())
			}
		}
		if diagnosticCounts["speaker"] != 1 || diagnosticCounts["target"] != 1 {
			t.Fatalf("speech-overlap diagnostic turns = %v, want one per participant and none for cancelled response", diagnosticCounts)
		}

		targetWrites := scenario.harness.participant("target").outboundSnapshot()
		wantTypes := []string{"session.update", "input_audio_buffer.append", "response.cancel", "input_audio_buffer.append", "input_audio_buffer.append", "input_audio_buffer.append"}
		gotTypes := make([]string, 0, len(targetWrites))
		for _, write := range targetWrites {
			gotTypes = append(gotTypes, write.Type)
		}
		if !sameRoomReplayStrings(gotTypes, wantTypes) {
			t.Fatalf("target overlap outbound types = %v, want %v", gotTypes, wantTypes)
		}
		wantAppends := [][]byte{scenario.silence, scenario.expectedSpeech, scenario.expectedSpeech, scenario.silence}
		appendWriteIndexes := []int{1, 3, 4, 5}
		for index, wantPCM := range wantAppends {
			assertRoomSpeechOverlapWireAppendPayload(t, targetWrites[appendWriteIndexes[index]], wantPCM)
		}
		appendCount := 0
		for _, write := range targetWrites {
			if write.Type == "input_audio_buffer.append" {
				appendCount++
			}
		}
		if got := appendCount; got != len(wantAppends) {
			t.Fatalf("target overlap append count = %d, want %d", got, len(wantAppends))
		}
		if got := len(scenario.harness.participant("speaker").outboundSnapshot()); got != 2 {
			t.Fatalf("speaker overlap outbound count = %d, want session.update plus one append", got)
		}
		if got := scenario.harness.participant("target").inboundTypes(); !sameRoomReplayStrings(got, []string{
			"session.created", "response.created", "response.output_audio.delta", "response.done",
			"response.created", "response.output_audio.delta", "response.output_audio.done", "response.done",
		}) {
			t.Fatalf("target overlap inbound provider events = %v", got)
		}
	})

	t.Run("digital silence negative control", func(t *testing.T) {
		// The target provider script is unchanged: replacing the two peer
		// frames with digital silence must fail at the exact response.cancel
		// slot instead of falsely satisfying the speech-overlap expectation.
		scenario := newRoomSpeechOverlapScenario(t, []byte{0, 0, 0, 0})

		scenario.targetCadence.Advance()
		assertRoomSpeechOverlapAppend(t, scenario.harness.participant("target"), scenario.silence)
		awaitRoomSpeechOverlapInput(t, scenario.targetInput, scenario.silence)
		awaitRoomSpeechOverlapAudio(t, scenario.targetAudio, scenario.targetOutput)
		awaitRoomSpeechOverlapFanout(t, scenario.fanouts, "target", "speaker", scenario.targetOutput)
		scenario.peerCadence.Advance()
		assertRoomSpeechOverlapAppend(t, scenario.harness.participant("speaker"), scenario.targetOutput)
		awaitRoomSpeechOverlapFanout(t, scenario.fanouts, "speaker", "target", scenario.peerOutput)
		awaitRoomSpeechOverlapFanout(t, scenario.fanouts, "speaker", "target", scenario.peerOutput)
		awaitRoomSpeechOverlapMessageEnd(t, scenario.speakerEnds, "speaker")

		scenario.targetCadence.Advance()
		// The target mixer must carry exact digital silence across the session
		// boundary; the strict replay below separately proves it cannot cancel.
		awaitRoomSpeechOverlapInput(t, scenario.targetInput, scenario.silence)
		select {
		case <-scenario.harness.participant("target").dialer.Done():
		case <-scenario.ctx.Done():
			t.Fatalf("digital-silence negative control did not reach strict replay mismatch: %v", scenario.ctx.Err())
		}
		error := scenario.harness.participant("target").dialer.Err()
		if error == nil {
			t.Fatal("digital-silence negative control did not retain strict replay mismatch")
		}
		scenario.cancel()
		outcome := awaitRoomSpeechOverlapRun(t, scenario)
		if outcome.err != nil || outcome.result.Reason != RoomTerminationStopped {
			t.Fatalf("digital-silence negative control cleanup outcome = (%v, %q), want stopped room after strict mismatch", outcome.err, outcome.result.Reason)
		}
		errorText := error.Error()
		for _, want := range []string{`participant "target"`, "response.cancel", "input_audio_buffer.append"} {
			if !strings.Contains(errorText, want) {
				t.Fatalf("digital-silence negative control error = %q, want %q", errorText, want)
			}
		}
		writes := scenario.harness.participant("target").outboundSnapshot()
		if len(writes) != 2 || writes[0].Type != "session.update" || writes[1].Type != "input_audio_buffer.append" {
			t.Fatalf("digital-silence target successful wire = %+v, want no response.cancel", writes)
		}
		if err := scenario.harness.participant("target").dialer.Err(); err == nil {
			t.Fatal("digital-silence target replay did not retain strict mismatch")
		}
	})
}

func TestRunRoomWithResult_BidirectionalOverlapRecordsPeerOnlyEvidence(t *testing.T) {
	const (
		aliceID = "alice"
		bobID   = "bob"
		model   = openAIRealtimeDefaultModel
	)

	silence := []byte{0, 0, 0, 0}
	alicePCM := []byte{0x21, 0x43, 0x65, 0x87}
	bobPCM := []byte{0x10, 0x32, 0x54, 0x76}
	silenceBase64 := base64.StdEncoding.EncodeToString(silence)

	makeCapture := func(id string, ownPCM, peerPCM []byte) gwtesting.SessionCapture {
		ownBase64 := base64.StdEncoding.EncodeToString(ownPCM)
		peerBase64 := base64.StdEncoding.EncodeToString(peerPCM)
		return roomRealtimeReplayCapture(model,
			roomRealtimeReplayEvent(1, gwtesting.DirectionClientToServer, "session.update", roomRealtimeReplaySessionUpdate(t, model, id+" system")),
			roomRealtimeReplayEvent(2, gwtesting.DirectionServerToClient, "session.created", roomRealtimeReplayJSON(t, map[string]any{
				"type": "session.created", "session": map[string]any{"id": "sess-" + id + "-bidirectional", "model": model},
			})),
			// The first idle frame opens an active response. Both participants
			// produce audio before either receives the other's next frame.
			roomRealtimeReplayEvent(3, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
				"type": "input_audio_buffer.append", "audio": silenceBase64,
			})),
			roomRealtimeReplayEvent(4, gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
				"type": "response.created", "response": map[string]any{"id": "resp-" + id + "-overlap"},
			})),
			roomRealtimeReplayEvent(5, gwtesting.DirectionServerToClient, "response.output_audio.delta", roomRealtimeReplayJSON(t, map[string]any{
				"type": "response.output_audio.delta", "response_id": "resp-" + id + "-overlap", "delta": ownBase64,
			})),
			// Peer input arrives while this response is still open. The strict
			// script requires exactly one cancel before that input append.
			roomRealtimeReplayEvent(6, gwtesting.DirectionClientToServer, "response.cancel", roomRealtimeReplayJSON(t, map[string]any{
				"type": "response.cancel",
			})),
			roomRealtimeReplayEvent(7, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
				"type": "input_audio_buffer.append", "audio": peerBase64,
			})),
			roomRealtimeReplayEvent(8, gwtesting.DirectionServerToClient, "input_audio_buffer.speech_started", roomRealtimeReplayJSON(t, map[string]any{
				"type": "input_audio_buffer.speech_started",
			})),
			roomRealtimeReplayEvent(9, gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, map[string]any{
				"type": "response.done", "response": map[string]any{"id": "resp-" + id + "-overlap", "status": "cancelled"},
			})),
			// A later idle frame must remain usable after the interruption and
			// complete one normal text response.
			roomRealtimeReplayEvent(10, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
				"type": "input_audio_buffer.append", "audio": silenceBase64,
			})),
			roomRealtimeReplayEvent(11, gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
				"type": "response.created", "response": map[string]any{"id": "resp-" + id + "-followup"},
			})),
			roomRealtimeReplayEvent(12, gwtesting.DirectionServerToClient, "response.output_text.delta", roomRealtimeReplayJSON(t, map[string]any{
				"type": "response.output_text.delta", "response_id": "resp-" + id + "-followup", "delta": id + " follow-up survived",
			})),
			roomRealtimeReplayEvent(13, gwtesting.DirectionServerToClient, "response.output_text.done", roomRealtimeReplayJSON(t, map[string]any{
				"type": "response.output_text.done", "response_id": "resp-" + id + "-followup",
			})),
			roomRealtimeReplayEvent(14, gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, map[string]any{
				"type": "response.done", "response": map[string]any{"id": "resp-" + id + "-followup", "status": "completed"},
			})),
		)
	}
	harness := newRoomRealtimeReplayHarness(t, map[string]gwtesting.SessionCapture{
		aliceID: makeCapture(aliceID, alicePCM, bobPCM),
		bobID:   makeCapture(bobID, bobPCM, alicePCM),
	})

	configDir := t.TempDir()
	writeSessionConfigFile(t, configDir, "model:\n  provider: openai\n")
	credentials := map[string]string{
		"ROOM_ALICE_KEY": "room-alice-bidirectional-key",
		"ROOM_BOB_KEY":   "room-bob-bidirectional-key",
	}
	manifest := room.Manifest{
		SchemaVersion: room.SchemaVersion,
		Room:          room.Room{MaxTurns: 1},
		Participants: []room.Participant{
			{ID: aliceID, SystemPrompt: "alice system", Provider: config.ProviderOpenAI, Model: model, APIKeyEnv: "ROOM_ALICE_KEY", Tools: []string{}},
			{ID: bobID, SystemPrompt: "bob system", Provider: config.ProviderOpenAI, Model: model, APIKeyEnv: "ROOM_BOB_KEY", Tools: []string{}},
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
	audioOutputs := make(chan roomSpeechOverlapFanout, 8)
	audioInputs := make(chan roomSpeechOverlapFanout, 16)
	fanouts := make(chan roomSpeechOverlapFanout, 8)
	vadStarted := make(chan string, len(manifest.Participants))
	messageEnds := make(chan string, 8)
	var eventMu sync.Mutex
	eventsByParticipant := make(map[string][]messages.StreamMessageType, len(manifest.Participants))
	var diagnosticMu sync.Mutex
	metricsByParticipant := make(map[string]SessionDiagnosticRecord, len(manifest.Participants))
	ingressByParticipant := make(map[string]SessionDiagnosticRecord, len(manifest.Participants))
	outputDir := filepath.Join(t.TempDir(), "room-run")
	roomCtx, cancel := context.WithTimeout(context.Background(), roomRealtimeReplayTestTimeout)
	t.Cleanup(cancel)

	opts := RoomRunOptions{
		Manifest:    manifest,
		ConfigDir:   configDir,
		OutputDir:   outputDir,
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
			fanouts <- roomSpeechOverlapFanout{sourceID: sourceID, targetID: targetID, pcm: append([]byte(nil), pcm...)}
		},
		onParticipantStream: func(participantID string, msg messages.StreamMessage) {
			eventMu.Lock()
			eventsByParticipant[participantID] = append(eventsByParticipant[participantID], msg.Type)
			eventMu.Unlock()
			switch msg.Type {
			case messages.StreamTypeVADSpeechStarted:
				vadStarted <- participantID
			case messages.StreamTypeMessageEnd:
				messageEnds <- participantID
			}
		},
		OnAudioOutput: func(participantID string, pcm []byte) error {
			audioOutputs <- roomSpeechOverlapFanout{targetID: participantID, pcm: append([]byte(nil), pcm...)}
			return nil
		},
		OnAudioInput: func(participantID string, pcm []byte) error {
			audioInputs <- roomSpeechOverlapFanout{targetID: participantID, pcm: append([]byte(nil), pcm...)}
			return nil
		},
		OnDiagnostic: func(participantID string, record SessionDiagnosticRecord) {
			diagnosticMu.Lock()
			switch record.Event {
			case SessionDiagnosticEventMetrics:
				metricsByParticipant[participantID] = record
			case SessionDiagnosticEventRoomAudioIngressSummary:
				ingressByParticipant[participantID] = record
			}
			diagnosticMu.Unlock()
		},
	}
	runDone := make(chan roomTestRunOutcome, 1)
	go func() {
		result, err := RunRoomWithResult(roomCtx, io.Discard, opts)
		runDone <- roomTestRunOutcome{result: result, err: err}
	}()

	var aliceCadence, bobCadence *roomRealtimeReplayCadence
	select {
	case aliceCadence = <-cadenceReady:
	case <-roomCtx.Done():
		t.Fatalf("alice bidirectional mixer cadence was not created: %v", roomCtx.Err())
	}
	select {
	case bobCadence = <-cadenceReady:
	case <-roomCtx.Done():
		t.Fatalf("bob bidirectional mixer cadence was not created: %v", roomCtx.Err())
	}
	for range manifest.Participants {
		select {
		case <-opened:
		case <-roomCtx.Done():
			t.Fatalf("bidirectional room sessions did not open: %v", roomCtx.Err())
		}
	}

	// Advance both participants into active responses before allowing either
	// response's peer frame to arrive. This is the controlled overlap point.
	aliceCadence.Advance()
	bobCadence.Advance()
	awaitRoomBidirectionalFrames(t, audioInputs, map[string][]byte{aliceID: silence, bobID: silence})
	assertRoomSpeechOverlapAppend(t, harness.participant(aliceID), silence)
	assertRoomSpeechOverlapAppend(t, harness.participant(bobID), silence)
	awaitRoomBidirectionalFrames(t, audioOutputs, map[string][]byte{aliceID: alicePCM, bobID: bobPCM})
	awaitRoomBidirectionalFanouts(t, fanouts, map[string][]byte{
		aliceID + "\x00" + bobID: alicePCM,
		bobID + "\x00" + aliceID: bobPCM,
	})

	// Each mixer now emits exactly the opposite participant's active-response
	// audio. Strict provider replay enforces cancel-before-append per target.
	aliceCadence.Advance()
	bobCadence.Advance()
	awaitRoomBidirectionalFrames(t, audioInputs, map[string][]byte{aliceID: bobPCM, bobID: alicePCM})
	aliceCancel := harness.participant(aliceID).awaitOutbound(t, "response.cancel")
	bobCancel := harness.participant(bobID).awaitOutbound(t, "response.cancel")
	if !bytes.Equal(aliceCancel.Payload, roomRealtimeReplayJSON(t, map[string]any{"type": "response.cancel"})) ||
		!bytes.Equal(bobCancel.Payload, roomRealtimeReplayJSON(t, map[string]any{"type": "response.cancel"})) {
		t.Fatalf("bidirectional cancel payloads = %s/%s, want exact response.cancel", aliceCancel.Payload, bobCancel.Payload)
	}
	assertRoomSpeechOverlapAppend(t, harness.participant(aliceID), bobPCM)
	assertRoomSpeechOverlapAppend(t, harness.participant(bobID), alicePCM)
	awaitRoomBidirectionalID(t, vadStarted, aliceID)
	awaitRoomBidirectionalID(t, vadStarted, bobID)
	awaitRoomBidirectionalID(t, messageEnds, aliceID)
	awaitRoomBidirectionalID(t, messageEnds, bobID)

	// The next idle frame is a real post-interruption turn for both sessions.
	aliceCadence.Advance()
	bobCadence.Advance()
	assertRoomSpeechOverlapAppend(t, harness.participant(aliceID), silence)
	assertRoomSpeechOverlapAppend(t, harness.participant(bobID), silence)

	var outcome roomTestRunOutcome
	select {
	case outcome = <-runDone:
	case <-roomCtx.Done():
		t.Fatalf("bidirectional room did not reach its explicit max-turn boundary: %v", roomCtx.Err())
	}
	if outcome.err != nil {
		t.Fatalf("bidirectional room replay: %v", outcome.err)
	}
	if outcome.result.Reason != RoomTerminationMaxTurnsReached {
		t.Fatalf("bidirectional room termination = %q, want %q", outcome.result.Reason, RoomTerminationMaxTurnsReached)
	}

	diagnosticMu.Lock()
	metrics := make(map[string]SessionDiagnosticRecord, len(metricsByParticipant))
	for id, record := range metricsByParticipant {
		metrics[id] = record
	}
	ingress := make(map[string]SessionDiagnosticRecord, len(ingressByParticipant))
	for id, record := range ingressByParticipant {
		ingress[id] = record
	}
	diagnosticMu.Unlock()
	eventMu.Lock()
	participantEvents := make(map[string][]messages.StreamMessageType, len(eventsByParticipant))
	for id, streamEvents := range eventsByParticipant {
		participantEvents[id] = append([]messages.StreamMessageType(nil), streamEvents...)
	}
	eventMu.Unlock()

	for _, check := range []struct {
		id      string
		peerPCM []byte
		ownPCM  []byte
		cancelN int
	}{
		{id: aliceID, peerPCM: bobPCM, ownPCM: alicePCM, cancelN: 1},
		{id: bobID, peerPCM: alicePCM, ownPCM: bobPCM, cancelN: 1},
	} {
		participantResult, ok := outcome.result.Participants[check.id]
		if !ok || !participantResult.Connected || participantResult.TurnsCompleted != 1 || participantResult.Error != "" {
			t.Fatalf("bidirectional participant %q result = %+v, want one clean follow-up turn", check.id, participantResult)
		}
		if err := harness.participant(check.id).dialer.Err(); err != nil {
			t.Fatalf("bidirectional participant %q strict wire: %v", check.id, err)
		}
		streamEvents := participantEvents[check.id]
		firstVAD := indexRoomStreamEvent(streamEvents, messages.StreamTypeVADSpeechStarted)
		firstEnd := indexRoomStreamEvent(streamEvents, messages.StreamTypeMessageEnd)
		if firstVAD < 0 || firstEnd < 0 || firstVAD > firstEnd {
			t.Fatalf("bidirectional participant %q stream order = %v, want peer VAD before first MESSAGE.END", check.id, streamEvents)
		}
		if countRoomStreamEvent(streamEvents, messages.StreamTypeMessageEnd) != 2 || countRoomStreamEvent(streamEvents, messages.StreamTypeTextDelta) != 1 {
			t.Fatalf("bidirectional participant %q stream lifecycle = %v, want cancelled boundary plus one text follow-up", check.id, streamEvents)
		}
		metric, ok := metrics[check.id]
		if !ok {
			t.Fatalf("bidirectional participant %q missing terminal metrics", check.id)
		}
		inputBytes, err := strconv.ParseUint(metric.Fields["input_audio_bytes"], 10, 64)
		if err != nil || inputBytes == 0 {
			t.Fatalf("bidirectional participant %q input_audio_bytes = %q, want non-zero", check.id, metric.Fields["input_audio_bytes"])
		}
		summary, ok := ingress[check.id]
		if !ok || summary.Fields[SessionDiagnosticFieldDeliveredBytes] != "4" || summary.Fields[SessionDiagnosticFieldRejectedBytes] != "0" || summary.Fields[SessionDiagnosticFieldContentLoss] != "false" {
			t.Fatalf("bidirectional participant %q ingress summary = %v, want one delivered peer frame and no loss", check.id, summary.Fields)
		}

		participantManifest, ok := manifestFromRoomEvidence(t, outputDir, check.id)
		if !ok {
			t.Fatalf("bidirectional room manifest is missing participant %q", check.id)
		}
		received := readRoomEvidenceFile(t, filepath.Join(outputDir, participantManifest.Artifacts.ReceivedPCM))
		if !bytes.Equal(received, check.peerPCM) || bytes.Equal(received, check.ownPCM) {
			t.Fatalf("bidirectional participant %q received PCM = %v, want exact peer-only PCM %v", check.id, received, check.peerPCM)
		}
		if participantManifest.Artifacts.ReceivedPCM != filepath.ToSlash(filepath.Join("participants", check.id, "received.pcm")) {
			t.Fatalf("bidirectional participant %q received artifact = %q, want manifest-addressed path", check.id, participantManifest.Artifacts.ReceivedPCM)
		}
		writes := harness.participant(check.id).outboundSnapshot()
		if countRoomReplayWireType(writes, "response.cancel") != check.cancelN {
			t.Fatalf("bidirectional participant %q response.cancel count = %d, want %d", check.id, countRoomReplayWireType(writes, "response.cancel"), check.cancelN)
		}
		wantTypes := []string{"session.update", "input_audio_buffer.append", "response.cancel", "input_audio_buffer.append", "input_audio_buffer.append"}
		gotTypes := make([]string, 0, len(writes))
		for _, write := range writes {
			gotTypes = append(gotTypes, write.Type)
		}
		if !sameRoomReplayStrings(gotTypes, wantTypes) {
			t.Fatalf("bidirectional participant %q outbound wire = %v, want %v", check.id, gotTypes, wantTypes)
		}
	}

	manifestData := readRoomEvidenceFile(t, filepath.Join(outputDir, RoomEvidenceManifestPath))
	var evidenceManifest roomEvidenceManifest
	if err := json.Unmarshal(manifestData, &evidenceManifest); err != nil {
		t.Fatalf("decode bidirectional room manifest: %v", err)
	}
	if !evidenceManifest.Finalized || evidenceManifest.AudioFormat.Encoding != "pcm_s16le" || evidenceManifest.AudioFormat.SampleRateHz != 100 || evidenceManifest.AudioFormat.Channels != 1 || evidenceManifest.AudioFormat.BitsPerSample != 16 || evidenceManifest.AudioFormat.FrameDuration != "20ms" {
		t.Fatalf("bidirectional room manifest finalization/format = %+v, want finalized PCM16 100Hz mono 20ms", evidenceManifest)
	}
}

func awaitRoomBidirectionalFrames(t *testing.T, frames <-chan roomSpeechOverlapFanout, wants map[string][]byte) {
	t.Helper()
	pending := make(map[string][]byte, len(wants))
	for participantID, want := range wants {
		pending[participantID] = want
	}
	timer := time.NewTimer(roomRealtimeReplayTestTimeout)
	defer timer.Stop()
	for len(pending) > 0 {
		select {
		case frame := <-frames:
			want, ok := pending[frame.targetID]
			if !ok {
				continue
			}
			if !bytes.Equal(frame.pcm, want) {
				t.Fatalf("bidirectional participant %q frame = %v, want %v", frame.targetID, frame.pcm, want)
			}
			delete(pending, frame.targetID)
		case <-timer.C:
			t.Fatalf("bidirectional participants did not receive frames %v", pending)
		}
	}
}

func awaitRoomBidirectionalFanouts(t *testing.T, fanouts <-chan roomSpeechOverlapFanout, wants map[string][]byte) {
	t.Helper()
	pending := make(map[string][]byte, len(wants))
	for key, want := range wants {
		pending[key] = want
	}
	timer := time.NewTimer(roomRealtimeReplayTestTimeout)
	defer timer.Stop()
	for len(pending) > 0 {
		select {
		case fanout := <-fanouts:
			key := fanout.sourceID + "\x00" + fanout.targetID
			want, ok := pending[key]
			if !ok {
				continue
			}
			if !bytes.Equal(fanout.pcm, want) {
				t.Fatalf("bidirectional fanout %s -> %s = %v, want %v", fanout.sourceID, fanout.targetID, fanout.pcm, want)
			}
			delete(pending, key)
		case <-timer.C:
			t.Fatalf("bidirectional fanouts did not carry frames %v", pending)
		}
	}
}

func awaitRoomBidirectionalID(t *testing.T, ids <-chan string, want string) {
	t.Helper()
	timer := time.NewTimer(roomRealtimeReplayTestTimeout)
	defer timer.Stop()
	for {
		select {
		case id := <-ids:
			if id == want {
				return
			}
		case <-timer.C:
			t.Fatalf("bidirectional participant %q event was not observed", want)
		}
	}
}

func indexRoomStreamEvent(events []messages.StreamMessageType, want messages.StreamMessageType) int {
	for index, event := range events {
		if event == want {
			return index
		}
	}
	return -1
}

func countRoomStreamEvent(events []messages.StreamMessageType, want messages.StreamMessageType) int {
	count := 0
	for _, event := range events {
		if event == want {
			count++
		}
	}
	return count
}

func countRoomReplayWireType(writes []roomRealtimeReplayWireMessage, want string) int {
	count := 0
	for _, write := range writes {
		if write.Type == want {
			count++
		}
	}
	return count
}

func manifestFromRoomEvidence(t *testing.T, outputDir, participantID string) (roomEvidenceParticipantManifest, bool) {
	t.Helper()
	manifestData := readRoomEvidenceFile(t, filepath.Join(outputDir, RoomEvidenceManifestPath))
	var manifest roomEvidenceManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("decode room manifest for participant %q: %v", participantID, err)
	}
	participant, ok := manifest.Participants[participantID]
	return participant, ok
}

func assertRoomSpeechOverlapAppend(t *testing.T, participant *roomRealtimeReplayParticipant, wantPCM []byte) {
	t.Helper()
	message := participant.awaitOutbound(t, "input_audio_buffer.append")
	assertRoomSpeechOverlapWireAppendPayload(t, message, wantPCM)
}

func assertRoomSpeechOverlapWireAppendPayload(t *testing.T, message roomRealtimeReplayWireMessage, wantPCM []byte) {
	t.Helper()
	want := roomRealtimeReplayJSON(t, map[string]any{
		"type":  "input_audio_buffer.append",
		"audio": base64.StdEncoding.EncodeToString(wantPCM),
	})
	if message.Type != "input_audio_buffer.append" || !bytes.Equal(message.Payload, want) {
		t.Fatalf("raw input_audio_buffer.append = %s, want exact PCM %v", message.Payload, wantPCM)
	}
}

func awaitRoomSpeechOverlapAudio(t *testing.T, audio <-chan []byte, want []byte) {
	t.Helper()
	timer := time.NewTimer(roomRealtimeReplayTestTimeout)
	defer timer.Stop()
	select {
	case got := <-audio:
		if !bytes.Equal(got, want) {
			t.Fatalf("room overlap output audio = %v, want %v", got, want)
		}
	case <-timer.C:
		t.Fatalf("room overlap did not emit expected output audio %v", want)
	}
}

func awaitRoomSpeechOverlapInput(t *testing.T, audio <-chan []byte, want []byte) {
	t.Helper()
	timer := time.NewTimer(roomRealtimeReplayTestTimeout)
	defer timer.Stop()
	select {
	case got := <-audio:
		if !bytes.Equal(got, want) {
			t.Fatalf("room overlap mixer input = %v, want %v", got, want)
		}
	case <-timer.C:
		t.Fatalf("room overlap did not emit expected mixer input %v", want)
	}
}

func awaitRoomSpeechOverlapFanout(t *testing.T, fanouts <-chan roomSpeechOverlapFanout, sourceID, targetID string, want []byte) {
	t.Helper()
	timer := time.NewTimer(roomRealtimeReplayTestTimeout)
	defer timer.Stop()
	for {
		select {
		case fanout := <-fanouts:
			if fanout.sourceID != sourceID || fanout.targetID != targetID {
				continue
			}
			if !bytes.Equal(fanout.pcm, want) {
				t.Fatalf("room overlap fanout %s -> %s = %v, want %v", sourceID, targetID, fanout.pcm, want)
			}
			return
		case <-timer.C:
			t.Fatalf("room overlap did not fan out %s -> %s", sourceID, targetID)
		}
	}
}

func awaitRoomSpeechOverlapRun(t *testing.T, scenario *roomSpeechOverlapScenario) roomTestRunOutcome {
	t.Helper()
	timer := time.NewTimer(roomRealtimeReplayTestTimeout)
	defer timer.Stop()
	select {
	case outcome := <-scenario.runDone:
		return outcome
	case <-timer.C:
		t.Fatalf("room overlap run did not terminate: %v; target replay_err=%v target_outbound=%+v speaker_outbound=%+v", scenario.ctx.Err(), scenario.harness.participant("target").dialer.Err(), scenario.harness.participant("target").outboundSnapshot(), scenario.harness.participant("speaker").outboundSnapshot())
		return roomTestRunOutcome{}
	}
}

func awaitRoomSpeechOverlapTargetEnd(t *testing.T, ends <-chan struct{}) {
	awaitRoomSpeechOverlapMessageEnd(t, ends, "target")
}

func awaitRoomSpeechOverlapMessageEnd(t *testing.T, ends <-chan struct{}, participantID string) {
	t.Helper()
	timer := time.NewTimer(roomRealtimeReplayTestTimeout)
	defer timer.Stop()
	select {
	case <-ends:
	case <-timer.C:
		t.Fatalf("room overlap did not observe the %s message-end boundary", participantID)
	}
}
