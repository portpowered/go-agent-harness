package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestRunRoomWithResult_SilenceCadenceDoesNotCancelActiveResponse(t *testing.T) {
	const (
		peerID   = "peer"
		targetID = "target"
		model    = openAIRealtimeDefaultModel
	)

	silence := []byte{0, 0, 0, 0}
	responsePCM := roomReplayOnTargetPCM()
	responseAudioBase64 := base64.StdEncoding.EncodeToString(responsePCM)
	silenceBase64 := base64.StdEncoding.EncodeToString(silence)

	peerCapture := roomRealtimeReplayCapture(model,
		roomRealtimeReplayEvent(1, gwtesting.DirectionClientToServer, "session.update", roomRealtimeReplaySessionUpdate(t, model, "peer system")),
		roomRealtimeReplayEvent(2, gwtesting.DirectionServerToClient, "session.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "session.created", "session": map[string]any{"id": "sess-peer", "model": model},
		})),
		roomRealtimeReplayEvent(3, gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.created", "response": map[string]any{"id": "resp-peer"},
		})),
		roomRealtimeReplayEvent(4, gwtesting.DirectionServerToClient, "response.output_text.delta", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_text.delta", "delta": "peer response",
		})),
		roomRealtimeReplayEvent(5, gwtesting.DirectionServerToClient, "response.output_text.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_text.done",
		})),
		roomRealtimeReplayEvent(6, gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.done",
		})),
	)
	targetCapture := roomRealtimeReplayCapture(model,
		roomRealtimeReplayEvent(1, gwtesting.DirectionClientToServer, "session.update", roomRealtimeReplaySessionUpdate(t, model, "target system")),
		roomRealtimeReplayEvent(2, gwtesting.DirectionServerToClient, "session.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "session.created", "session": map[string]any{"id": "sess-target", "model": model},
		})),
		// The first zero frame is idle input. It unlocks response.created; the
		// output delta then proves the target response is active before the
		// remaining zero frames are advanced.
		roomRealtimeReplayEvent(3, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": silenceBase64,
		})),
		roomRealtimeReplayEvent(4, gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.created", "response": map[string]any{"id": "resp-target"},
		})),
		roomRealtimeReplayEvent(5, gwtesting.DirectionServerToClient, "response.output_audio.delta", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_audio.delta", "delta": responseAudioBase64,
		})),
		// Keep the provider response open behind three exact silence appends.
		// A response.cancel at any of these positions is a strict replay error.
		roomRealtimeReplayEvent(6, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": silenceBase64,
		})),
		roomRealtimeReplayEvent(7, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": silenceBase64,
		})),
		roomRealtimeReplayEvent(8, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": silenceBase64,
		})),
		roomRealtimeReplayEvent(9, gwtesting.DirectionServerToClient, "response.output_audio.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_audio.done",
		})),
		roomRealtimeReplayEvent(10, gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.done",
		})),
	)
	harness := newRoomRealtimeReplayHarness(t, map[string]gwtesting.SessionCapture{
		peerID:   peerCapture,
		targetID: targetCapture,
	})

	configDir := t.TempDir()
	writeSessionConfigFile(t, configDir, "model:\n  provider: openai\n")
	credentials := map[string]string{
		"ROOM_PEER_KEY":   "room-peer-test-key",
		"ROOM_TARGET_KEY": "room-target-test-key",
	}
	manifest := room.Manifest{
		SchemaVersion: room.SchemaVersion,
		Room:          room.Room{MaxTurns: 1},
		Participants: []room.Participant{
			{ID: peerID, SystemPrompt: "peer system", Provider: config.ProviderOpenAI, Model: model, APIKeyEnv: "ROOM_PEER_KEY", Tools: []string{}},
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
	targetAudio := make(chan []byte, 1)
	opened := make(chan string, len(manifest.Participants))
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
		OnAudioOutput: func(participantID string, pcm []byte) error {
			if participantID == targetID {
				targetAudio <- append([]byte(nil), pcm...)
			}
			return nil
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
		t.Fatalf("peer mixer cadence was not created: %v", roomCtx.Err())
	}
	select {
	case targetCadence = <-cadenceReady:
	case <-roomCtx.Done():
		t.Fatalf("target mixer cadence was not created: %v", roomCtx.Err())
	}
	if peerCadence == nil || targetCadence == nil {
		t.Fatal("room did not create both deterministic mixer cadences")
	}
	for range manifest.Participants {
		select {
		case <-opened:
		case <-roomCtx.Done():
			t.Fatalf("room sessions did not open: %v", roomCtx.Err())
		}
	}

	// No peer audio is ever queued, so this frame is deterministically all
	// zeroes. It starts the scripted response without asserting on scheduling.
	targetCadence.Advance()
	firstAppend := harness.participant(targetID).awaitOutbound(t, "input_audio_buffer.append")
	if !bytes.Equal(firstAppend.Payload, roomRealtimeReplayJSON(t, map[string]any{
		"type": "input_audio_buffer.append", "audio": silenceBase64,
	})) {
		t.Fatalf("target idle append payload = %s, want exact silence append", firstAppend.Payload)
	}
	select {
	case got := <-targetAudio:
		if !bytes.Equal(got, responsePCM) {
			t.Fatalf("target response audio = %v, want %v", got, responsePCM)
		}
	case <-roomCtx.Done():
		t.Fatalf("target response did not become active: %v", roomCtx.Err())
	}

	for activeFrame := 1; activeFrame <= 3; activeFrame++ {
		targetCadence.Advance()
		appendMessage := harness.participant(targetID).awaitOutbound(t, "input_audio_buffer.append")
		wantPayload := roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": silenceBase64,
		})
		if !bytes.Equal(appendMessage.Payload, wantPayload) {
			t.Fatalf("target active silence frame %d payload = %s, want exact silence append", activeFrame, appendMessage.Payload)
		}
	}

	var outcome roomTestRunOutcome
	select {
	case outcome = <-runDone:
	case <-roomCtx.Done():
		t.Fatalf("room did not reach explicit max-turn boundary after silence: %v", roomCtx.Err())
	}
	if outcome.err != nil {
		t.Fatalf("silence room replay: %v", outcome.err)
	}
	if outcome.result.Reason != RoomTerminationMaxTurnsReached {
		t.Fatalf("room termination reason = %q, want %q", outcome.result.Reason, RoomTerminationMaxTurnsReached)
	}
	for _, participantID := range []string{peerID, targetID} {
		participantResult, ok := outcome.result.Participants[participantID]
		if !ok {
			t.Fatalf("room result missing participant %q", participantID)
		}
		if !participantResult.Connected || participantResult.TurnsCompleted != 1 {
			t.Fatalf("participant %q result = %+v, want connected with one completed turn", participantID, participantResult)
		}
		if err := harness.participant(participantID).dialer.Err(); err != nil {
			t.Fatalf("participant %q strict wire: %v", participantID, err)
		}
	}

	peerWrites := harness.participant(peerID).outboundSnapshot()
	if len(peerWrites) != 1 || peerWrites[0].Type != "session.update" {
		t.Fatalf("peer outbound raw wire = %+v, want one session.update", peerWrites)
	}
	targetWrites := harness.participant(targetID).outboundSnapshot()
	if len(targetWrites) != 5 || targetWrites[0].Type != "session.update" {
		t.Fatalf("target outbound raw wire = %+v, want session.update plus four silence appends", targetWrites)
	}
	for index, write := range targetWrites[1:] {
		if write.Type != "input_audio_buffer.append" || !bytes.Equal(write.Payload, roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": silenceBase64,
		})) {
			t.Fatalf("target outbound raw wire step %d = %+v, want silence append and no response.cancel", index+2, write)
		}
	}
}

type roomRealtimeReplayCadence struct {
	ticks   chan time.Time
	stopped chan struct{}
	once    sync.Once
}

func newRoomRealtimeReplayCadence() *roomRealtimeReplayCadence {
	return &roomRealtimeReplayCadence{
		ticks:   make(chan time.Time, 4),
		stopped: make(chan struct{}),
	}
}

func (c *roomRealtimeReplayCadence) C() <-chan time.Time { return c.ticks }

func (c *roomRealtimeReplayCadence) Stop() {
	c.once.Do(func() { close(c.stopped) })
}

func (c *roomRealtimeReplayCadence) Advance() {
	select {
	case c.ticks <- time.Time{}:
	case <-c.stopped:
	}
}
