package integration

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func v8PCMStats(payload []byte) (string, float64) {
	digest := sha256.Sum256(payload)
	if len(payload) == 0 || len(payload)%2 != 0 {
		return hex.EncodeToString(digest[:]), 0
	}
	var energy float64
	for offset := 0; offset < len(payload); offset += 2 {
		sample := int16(binary.LittleEndian.Uint16(payload[offset:]))
		energy += float64(sample) * float64(sample)
	}
	return hex.EncodeToString(digest[:]), math.Sqrt(energy / float64(len(payload)/2))
}

func v8PCMHash(payload []byte) string {
	hash, _ := v8PCMStats(payload)
	return hash
}

func v8PCM16Bytes(samples []int16) []byte {
	payload := make([]byte, len(samples)*2)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(payload[i*2:], uint16(sample))
	}
	return payload
}

func v8AudioFixturePath(t *testing.T, name string) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve v8 audio fixture path: runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "go-agent-loop", "testdata", "audio", name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("v8 audio fixture %q not found at %q: %v", name, path, err)
	}
	return path
}

func v8LoudFrames(t *testing.T, path string) ([]byte, []byte) {
	frames := v8LoudFrameSet(t, path, 2)
	return frames[0], frames[1]
}

func v8LoudFrameSet(t *testing.T, path string, count int) [][]byte {
	t.Helper()
	if count <= 0 {
		t.Fatalf("v8 loud frame count = %d, want positive", count)
	}
	wav, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read v8 overlap fixture: %v", err)
	}
	rate, samples, err := wavio.Read(bytes.NewReader(wav))
	if err != nil {
		t.Fatalf("parse v8 overlap fixture: %v", err)
	}
	if rate != audio.SampleRate {
		t.Fatalf("v8 overlap fixture rate = %d, want %d", rate, audio.SampleRate)
	}
	starts := make([]int, 0, count)
	frames := make([][]byte, 0, count)
	for len(frames) < count {
		bestStart := -1
		bestEnergy := -1.0
		for start := 0; start+audio.FrameSize <= len(samples); start += audio.FrameSize {
			allowed := true
			for _, selectedStart := range starts {
				if absInt(start-selectedStart) < audio.FrameSize*4 {
					allowed = false
					break
				}
			}
			if !allowed {
				continue
			}
			var energy float64
			for _, sample := range samples[start : start+audio.FrameSize] {
				energy += float64(sample) * float64(sample)
			}
			if energy > bestEnergy {
				bestStart, bestEnergy = start, energy
			}
		}
		if bestStart < 0 {
			t.Fatalf("v8 overlap fixture has fewer than %d distinct energetic frames", count)
		}
		starts = append(starts, bestStart)
		frames = append(frames, v8PCM16Bytes(samples[bestStart:bestStart+audio.FrameSize]))
	}
	for _, payload := range frames {
		_, rms := v8PCMStats(payload)
		if rms <= v8VADThreshold {
			t.Fatalf("v8 overlap fixture frame RMS = %.1f, want > %.1f", rms, v8VADThreshold)
		}
	}
	return frames
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func v8CaptureRecord(sequence int, direction gwtesting.SessionEventDirection, msg messages.StreamMessage) gwtesting.CapturedSessionEvent {
	payload, err := gwtesting.MarshalStreamMessage(msg)
	if err != nil {
		panic(fmt.Sprintf("marshal v8 capture event %s: %v", msg.Type, err))
	}
	return gwtesting.CapturedSessionEvent{
		Sequence:    sequence,
		Direction:   direction,
		TimestampMs: int64(sequence - 1),
		Type:        string(msg.Type),
		PayloadType: gwtesting.SessionPayloadTypeStreamMessage,
		Payload:     payload,
	}
}

func writeV8ReplayCapture(t *testing.T, path, sessionID, instruction string, output, expectedInput []byte) {
	t.Helper()
	records := []gwtesting.CapturedSessionEvent{
		v8CaptureRecord(1, gwtesting.DirectionServerToClient, messages.StreamMessage{
			Type:  messages.StreamTypeSessionOpen,
			Value: messages.NewSessionOpenValue(sessionID, "audio_inference"),
		}),
		v8CaptureRecord(2, gwtesting.DirectionClientToServer, messages.StreamMessage{
			Type:  messages.StreamTypeTextDelta,
			Value: messages.NewTextDeltaValue(instruction),
		}),
		v8CaptureRecord(3, gwtesting.DirectionServerToClient, messages.StreamMessage{
			Type:  messages.StreamTypeAudioDelta,
			Value: messages.NewAudioDeltaValue(output),
		}),
		v8CaptureRecord(4, gwtesting.DirectionClientToServer, messages.StreamMessage{
			Type:  messages.StreamTypeAudioDelta,
			Value: messages.NewAudioDeltaValue(expectedInput),
		}),
		// The audio source sends a type-only MESSAGE.END after it reads EOF.
		v8CaptureRecord(5, gwtesting.DirectionClientToServer, messages.StreamMessage{
			Type: messages.StreamTypeMessageEnd,
		}),
		v8CaptureRecord(6, gwtesting.DirectionServerToClient, messages.StreamMessage{
			Type:  messages.StreamTypeAudioEnd,
			Value: messages.NewAudioEndValue(),
		}),
		v8CaptureRecord(7, gwtesting.DirectionServerToClient, messages.StreamMessage{
			Type:  messages.StreamTypeMessageEnd,
			Value: messages.NewMessageEndValue(messages.TokenUsage{}),
		}),
	}
	capture := gwtesting.SessionCapture{
		Version:  gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{Name: "synthetic-t1", Model: "session-replay"},
		Session: gwtesting.SessionMetadata{
			ID:                sessionID,
			StartedAtUTC:      "2026-08-26T00:00:00Z",
			FixtureProvenance: gwtesting.SessionFixtureProvenanceSynthetic,
		},
		Records: records,
	}
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal v8 replay capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write v8 replay capture: %v", err)
	}
}

func writeV8MultiTurnReplayCapture(t *testing.T, path, sessionID, instruction, harness string, outputs, expectedInputs [][]byte) {
	t.Helper()
	if len(outputs) != v8MultiTurnCount || len(expectedInputs) != v8MultiTurnCount {
		t.Fatalf("v8 multi-turn capture %s has outputs=%d inputs=%d, want %d each", harness, len(outputs), len(expectedInputs), v8MultiTurnCount)
	}
	sequence := 1
	records := []gwtesting.CapturedSessionEvent{
		v8CaptureRecord(sequence, gwtesting.DirectionServerToClient, messages.StreamMessage{
			Type:  messages.StreamTypeSessionOpen,
			Value: messages.NewSessionOpenValue(sessionID, "audio_inference"),
		}),
	}
	sequence++
	records = append(records, v8CaptureRecord(sequence, gwtesting.DirectionClientToServer, messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue(instruction),
	}))
	sequence++
	appendServerTurn := func(turn int) {
		marker := fmt.Sprintf("%s transcript turn %d", harness, turn)
		records = append(records,
			v8CaptureRecord(sequence, gwtesting.DirectionServerToClient, messages.StreamMessage{
				Type:  messages.StreamTypeTextDelta,
				Value: messages.NewTextDeltaValue(marker),
			}),
			v8CaptureRecord(sequence+1, gwtesting.DirectionServerToClient, messages.StreamMessage{
				Type:  messages.StreamTypeAudioDelta,
				Value: messages.NewAudioDeltaValue(outputs[turn-1]),
			}),
		)
		sequence += 2
	}
	appendPeerInput := func(turn int) {
		records = append(records, v8CaptureRecord(sequence, gwtesting.DirectionClientToServer, messages.StreamMessage{
			Type:  messages.StreamTypeAudioDelta,
			Value: messages.NewAudioDeltaValue(expectedInputs[turn-1]),
		}))
		sequence++
	}
	appendResponseEnd := func() {
		records = append(records,
			v8CaptureRecord(sequence, gwtesting.DirectionServerToClient, messages.StreamMessage{
				Type:  messages.StreamTypeAudioEnd,
				Value: messages.NewAudioEndValue(),
			}),
			v8CaptureRecord(sequence+1, gwtesting.DirectionServerToClient, messages.StreamMessage{
				Type:  messages.StreamTypeMessageEnd,
				Value: messages.NewMessageEndValue(messages.TokenUsage{}),
			}),
		)
		sequence += 2
	}
	appendInputEnd := func() {
		records = append(records, v8CaptureRecord(sequence, gwtesting.DirectionClientToServer, messages.StreamMessage{
			Type: messages.StreamTypeMessageEnd,
		}))
		sequence++
	}

	for turn := 1; turn <= 2; turn++ {
		appendServerTurn(turn)
		appendPeerInput(turn)
		appendInputEnd()
		appendResponseEnd()
	}
	// Turn three is the ordinary sequential boundary. A's output interval is
	// scheduled first; B receives it and commits its finite input before its
	// own output interval begins. A receives B's final output before its own
	// end-of-input commit, so both raw streams remain coupled until the script
	// is complete and each bridge emits EOF only once.
	if harness == "A" {
		appendServerTurn(3)
		appendPeerInput(3)
		appendInputEnd()
		appendResponseEnd()
	} else {
		appendPeerInput(3)
		appendInputEnd()
		appendServerTurn(3)
		appendResponseEnd()
	}
	records = append(records, v8CaptureRecord(sequence, gwtesting.DirectionServerToClient, messages.StreamMessage{
		Type:  messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValue(sessionID, "provider_closed"),
	}))
	capture := gwtesting.SessionCapture{
		Version:  gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{Name: "synthetic-t1", Model: "session-replay"},
		Session: gwtesting.SessionMetadata{
			ID:                sessionID,
			StartedAtUTC:      "2026-08-26T00:00:00Z",
			FixtureProvenance: gwtesting.SessionFixtureProvenanceSynthetic,
		},
		Records: records,
	}
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal v8 multi-turn replay capture %s: %v", harness, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write v8 multi-turn replay capture %s: %v", harness, err)
	}
}
