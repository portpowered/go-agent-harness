package services

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestLoadRoomReplayPlanAcceptsFractionalRecordingTimeline(t *testing.T) {
	bundle, manifest := writeRoomReplayBundle(t)
	clockBase := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	timeline := []byte(fmt.Sprintf(
		`{"t_offset_ms":0.5,"t_unix_ms":%d,"event":"speech_start","participant":"alpha"}`+"\n"+`{"t_offset_ms":10.25,"t_unix_ms":%d,"event":"speech_start","participant":"beta"}`+"\n",
		clockBase.UnixMilli(), clockBase.UnixMilli()+10,
	))
	if err := os.WriteFile(filepath.Join(bundle, "room-timeline.jsonl"), timeline, 0o600); err != nil {
		t.Fatalf("write fractional timeline: %v", err)
	}
	updateArtifactDigest(t, manifest, "room_timeline", timeline)
	writeManifestValue(t, bundle, manifest)

	plan, err := LoadRoomReplayPlan(bundle)
	if err != nil {
		t.Fatalf("LoadRoomReplayPlan: %v", err)
	}
	if len(plan.Timeline) != 2 || plan.Timeline[0].ParticipantID != "alpha" || plan.Timeline[1].ParticipantID != "beta" {
		t.Fatalf("fractional timeline = %+v", plan.Timeline)
	}
	if plan.Timeline[0].OffsetNanos != 500000 || plan.Timeline[1].OffsetNanos != 10250000 {
		t.Fatalf("fractional offsets = %d/%d, want 500000/10250000 ns", plan.Timeline[0].OffsetNanos, plan.Timeline[1].OffsetNanos)
	}
}

func TestRunRoomReplaySchedulesOverlapThroughProductionMixer(t *testing.T) {
	bundle, manifest := writeRoomReplayBundle(t)
	manifest["pcm_format"] = map[string]any{
		"sample_rate_hz":    100,
		"channels":          1,
		"sample_width_bits": 16,
		"byte_order":        "little",
		"encoding":          "signed_pcm16",
	}
	alphaPCM := roomPCM16(1000, 2)
	betaPCM := roomPCM16(2000, 2)
	writeRoomReplayArtifact(t, bundle, manifest, "alpha", roomReplayArtifactRoleSentPCM, alphaPCM)
	writeRoomReplayArtifact(t, bundle, manifest, "beta", roomReplayArtifactRoleSentPCM, betaPCM)

	model := "gpt-realtime"
	for _, participant := range []struct {
		id        string
		system    string
		inputPCM  []byte
		outputPCM []byte
	}{
		{id: "alpha", system: "alpha system", inputPCM: betaPCM, outputPCM: alphaPCM},
		{id: "beta", system: "beta system", inputPCM: alphaPCM, outputPCM: betaPCM},
	} {
		input := base64.StdEncoding.EncodeToString(participant.inputPCM)
		output := base64.StdEncoding.EncodeToString(participant.outputPCM)
		capture := roomRealtimeReplayCapture(model,
			roomRealtimeReplayEvent(1, gwtesting.DirectionClientToServer, "session.update", roomRealtimeReplaySessionUpdate(t, model, participant.system)),
			roomRealtimeReplayEvent(2, gwtesting.DirectionServerToClient, "session.created", roomRealtimeReplayJSON(t, map[string]any{
				"type": "session.created", "session": map[string]any{"id": "sess-" + participant.id, "model": model},
			})),
			roomRealtimeReplayEvent(3, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
				"type": "input_audio_buffer.append", "audio": input,
			})),
			roomRealtimeReplayEvent(4, gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
				"type": "response.created", "response": map[string]any{"id": "resp-" + participant.id},
			})),
			roomRealtimeReplayEvent(5, gwtesting.DirectionServerToClient, "response.output_audio.delta", roomRealtimeReplayJSON(t, map[string]any{
				"type": "response.output_audio.delta", "response_id": "resp-" + participant.id, "delta": output,
			})),
			roomRealtimeReplayEvent(6, gwtesting.DirectionServerToClient, "response.output_audio.done", roomRealtimeReplayJSON(t, map[string]any{
				"type": "response.output_audio.done", "response_id": "resp-" + participant.id,
			})),
			roomRealtimeReplayEvent(7, gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, map[string]any{
				"type": "response.done", "response": map[string]any{"id": "resp-" + participant.id, "status": "completed"},
			})),
			roomRealtimeReplayEvent(8, gwtesting.DirectionServerToClient, "session.closed", roomRealtimeReplayJSON(t, map[string]any{
				"type": "session.closed",
			})),
		)
		writeRoomReplayRuntimeCapture(t, bundle, manifest, participant.id, capture)
	}
	writeManifestValue(t, bundle, manifest)

	var mu sync.Mutex
	var inputFrames map[string][][]byte = map[string][][]byte{"alpha": {}, "beta": {}}
	var fanouts [][2]string
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := RunRoomWithResult(ctx, io.Discard, RoomRunOptions{
		ReplayPath: bundle,
		PCMFormat:  room.PCM16Format{SampleRate: 100, Channels: 1, FrameDuration: 20 * time.Millisecond},
		OnAudioInput: func(id string, pcm []byte) error {
			mu.Lock()
			inputFrames[id] = append(inputFrames[id], append([]byte(nil), pcm...))
			mu.Unlock()
			return nil
		},
		onParticipantAudioFanned: func(sourceID, targetID string, _ []byte) {
			mu.Lock()
			fanouts = append(fanouts, [2]string{sourceID, targetID})
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("room replay: %v", err)
	}
	if result.Reason != RoomTerminationStopped {
		t.Fatalf("room replay reason = %q, want stopped", result.Reason)
	}
	mu.Lock()
	gotAlpha := append([][]byte(nil), inputFrames["alpha"]...)
	gotBeta := append([][]byte(nil), inputFrames["beta"]...)
	gotFanouts := append([][2]string(nil), fanouts...)
	mu.Unlock()
	if len(gotAlpha) != 1 || string(gotAlpha[0]) != string(betaPCM) {
		t.Fatalf("alpha mixed frames = %x, want %x", gotAlpha, betaPCM)
	}
	if len(gotBeta) != 1 || string(gotBeta[0]) != string(alphaPCM) {
		t.Fatalf("beta mixed frames = %x, want %x", gotBeta, alphaPCM)
	}
	wantFanouts := [][2]string{{"alpha", "beta"}, {"beta", "alpha"}}
	if len(gotFanouts) != len(wantFanouts) {
		t.Fatalf("scheduler fanout order = %v, want %v", gotFanouts, wantFanouts)
	}
	for index := range wantFanouts {
		if gotFanouts[index] != wantFanouts[index] {
			t.Fatalf("scheduler fanout[%d] = %v, want %v", index, gotFanouts[index], wantFanouts[index])
		}
	}
}

func TestRoomReplaySchedulerCancellationStopsManualMixer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	format := room.PCM16Format{SampleRate: 100, Channels: 1, FrameDuration: 20 * time.Millisecond}
	mixer, err := room.NewPCM16MixerWithConfig(ctx, room.PCM16MixerConfig{
		Format:            format,
		InputQueueFrames:  4,
		OutputQueueFrames: 1,
		Manual:            true,
	})
	if err != nil {
		t.Fatalf("new manual mixer: %v", err)
	}
	t.Cleanup(func() { _ = mixer.Close() })
	if err := mixer.AddInput("source"); err != nil {
		t.Fatalf("add source input: %v", err)
	}
	target := &roomParticipantRuntime{
		plan:            &roomParticipantPlan{manifest: room.Participant{ID: "target"}},
		ctx:             ctx,
		mixer:           mixer,
		replayFrameAcks: make(chan struct{}, 1),
	}
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		for {
			if _, err := mixer.ReadFrame(ctx); err != nil {
				return
			}
			select {
			case target.replayFrameAcks <- struct{}{}:
			case <-ctx.Done():
				return
			}
		}
	}()

	schedule := &roomReplaySchedule{
		frameDuration: format.FrameDuration,
		frameBytes:    4,
		frames: []roomReplayScheduledFrame{
			{contributions: []roomReplayContribution{{sourceID: "source", sequence: 1, pcm: roomPCM16(1000, 2)}}},
			{contributions: []roomReplayContribution{{sourceID: "source", sequence: 2, pcm: roomPCM16(2000, 2)}}},
		},
		targetIDs: []string{"target"},
	}
	var cancelOnce sync.Once
	err = schedule.run(ctx, []*roomParticipantRuntime{target}, nil, RoomRunOptions{
		onParticipantAudioFanned: func(string, string, []byte) {
			cancelOnce.Do(cancel)
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("scheduler cancellation error = %v, want context cancellation", err)
	}
	select {
	case <-pumpDone:
	case <-time.After(time.Second):
		t.Fatal("manual mixer pump did not stop after scheduler cancellation")
	}
}

func writeRoomReplayArtifact(t *testing.T, bundle string, manifest map[string]any, participantID, role string, data []byte) {
	t.Helper()
	participant := manifest["participants"].(map[string]any)[participantID].(map[string]any)
	artifact := participant["artifacts"].(map[string]any)[role].(map[string]any)
	path := artifact["path"].(string)
	if err := os.WriteFile(filepath.Join(bundle, filepath.FromSlash(path)), data, 0o600); err != nil {
		t.Fatalf("write %s artifact: %v", role, err)
	}
	digest := sha256.Sum256(data)
	artifact["size"] = len(data)
	artifact["sha256"] = hex.EncodeToString(digest[:])
}
