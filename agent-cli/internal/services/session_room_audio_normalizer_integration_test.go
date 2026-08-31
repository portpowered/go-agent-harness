package services

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

func TestRunRoom_NormalizesEachSynthesizedParticipantBeforeFanoutAndEvidence(t *testing.T) {
	ids := []string{"alloy", "verse"}
	levels := map[string]int16{"alloy": 220, "verse": 4200}
	const sampleCount = 480
	inferencers := make(map[string]*roomTestInferencer, len(ids))
	for _, id := range ids {
		inferencers[id] = &roomTestInferencer{events: []messages.StreamMessage{
			roomTestSessionOpen(id),
			{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, Value: messages.NewAudioStartValue()},
			roomTestAudioSignalEvent(levels[id], sampleCount),
		}}
	}

	outputDir := filepath.Join(t.TempDir(), "room-run")
	outputs := make(chan roomAudioFrame, len(ids))
	fanouts := make(chan roomFanoutFrame, len(ids))
	inputs := make(chan roomAudioFrame, 256)
	opts, _ := newRoomTestRunOptions(ids, inferencers)
	opts.MixerConfig = room.PCM16MixerConfig{
		Format:            room.DefaultPCM16Format(),
		InputQueueFrames:  4,
		OutputQueueFrames: 8,
	}
	opts.OutputDir = outputDir
	opts.OnAudioOutput = func(id string, pcm []byte) error {
		outputs <- roomAudioFrame{id: id, pcm: append([]byte(nil), pcm...)}
		return nil
	}
	opts.OnAudioInput = func(id string, pcm []byte) error {
		inputs <- roomAudioFrame{id: id, pcm: append([]byte(nil), pcm...)}
		return nil
	}
	opts.onParticipantAudioFanned = func(sourceID, targetID string, pcm []byte) {
		fanouts <- roomFanoutFrame{sourceID: sourceID, targetID: targetID, pcm: append([]byte(nil), pcm...)}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan roomTestRunOutcome, 1)
	go func() {
		result, err := RunRoomWithResult(ctx, io.Discard, opts)
		runDone <- roomTestRunOutcome{result: result, err: err}
	}()

	outputByID := make(map[string][]byte, len(ids))
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for len(outputByID) < len(ids) {
		select {
		case output := <-outputs:
			if _, exists := outputByID[output.id]; exists {
				continue
			}
			outputByID[output.id] = output.pcm
			assertSessionNormalizedPCM(t, output.pcm, sampleCount, float64(levels[output.id]))
		case <-deadline.C:
			t.Fatalf("timed out waiting for normalized room outputs: %v", outputByID)
		}
	}
	if delta := sessionPCM16DBDelta(outputByID[ids[0]], outputByID[ids[1]]); delta > 3 {
		t.Fatalf("normalized room voice RMS delta = %.3f dB, want <= 3 dB", delta)
	}

	seenFanouts := make(map[string]bool, len(ids))
	for len(seenFanouts) < len(ids) {
		select {
		case fanout := <-fanouts:
			key := fanout.sourceID + "->" + fanout.targetID
			if seenFanouts[key] {
				continue
			}
			seenFanouts[key] = true
			if !bytes.Equal(fanout.pcm, outputByID[fanout.sourceID]) {
				t.Fatalf("fanout %s = %v, want normalized source output %v", key, fanout.pcm, outputByID[fanout.sourceID])
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for normalized room fan-outs: %v", seenFanouts)
		}
	}

	// The opposite participant's mixer is the next customer-facing boundary.
	// Ignore idle cadence frames, then require the complete normalized peer
	// stream rather than merely observing that some audio was delivered.
	receivedByID := make(map[string][]byte, len(ids))
	for len(receivedByID) < len(ids) {
		select {
		case input := <-inputs:
			if !pcm16HasSignal(input.pcm) {
				continue
			}
			receivedByID[input.id] = append(receivedByID[input.id], input.pcm...)
			otherID := ids[0]
			if input.id == otherID {
				otherID = ids[1]
			}
			if len(receivedByID[input.id]) >= len(outputByID[otherID]) {
				if !bytes.Equal(receivedByID[input.id][:len(outputByID[otherID])], outputByID[otherID]) {
					t.Fatalf("mixer delivery to %s differs from normalized %s output", input.id, otherID)
				}
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for normalized mixer delivery: %v", receivedByID)
		}
	}

	cancel()
	select {
	case outcome := <-runDone:
		if outcome.err != nil {
			t.Fatalf("room normalization run: %v", outcome.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("room normalization run did not terminate")
	}

	for _, id := range ids {
		sentPath := filepath.Join(outputDir, "participants", id, "sent.pcm")
		sent, err := os.ReadFile(sentPath)
		if err != nil {
			t.Fatalf("read %s sent PCM: %v", id, err)
		}
		if !bytes.Equal(sent, outputByID[id]) {
			t.Fatalf("%s sent evidence differs from normalized output", id)
		}

		wavPath := filepath.Join(outputDir, "agent-"+id+".wav")
		wavData, err := os.ReadFile(wavPath)
		if err != nil {
			t.Fatalf("read %s WAV evidence: %v", id, err)
		}
		rate, samples, err := wavio.Read(bytes.NewReader(wavData))
		if err != nil {
			t.Fatalf("decode %s WAV evidence: %v", id, err)
		}
		if rate != room.DefaultPCM16SampleRate || !bytes.Equal(pcm16Bytes(samples), outputByID[id]) {
			t.Fatalf("%s WAV evidence rate/samples = %d/%d, want %d/%d normalized samples", id, rate, len(samples), room.DefaultPCM16SampleRate, len(outputByID[id])/2)
		}
	}
}
