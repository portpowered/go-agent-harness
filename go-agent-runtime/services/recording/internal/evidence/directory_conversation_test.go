package evidence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// Both streams preserve their own order, but either may run ahead. Enumerate
// every interleaving of two media frames with four normalized response events.
func TestRecordedResponseAudioIsIndependentOfQueueScheduling(t *testing.T) {
	for first := 0; first <= 4; first++ {
		for second := first; second <= 4; second++ {
			t.Run(fmt.Sprintf("audio-at-%d-%d", first, second), func(t *testing.T) { assertRecordedAudioInterleaving(t, first, second) })
		}
	}
}

func assertRecordedAudioInterleaving(t *testing.T, first, second int) {
	t.Helper()
	r := newEvidenceRecorder(t)
	for position := 0; position <= 4; position++ {
		for index, at := range []int{first, second} {
			if at == position {
				recordResponsePCM(t, r, index)
			}
		}
		if position < 4 {
			recordResponseMessage(t, r, position)
		}
	}
	recordEvidenceTerminal(t, r)
	if err := r.Finalize(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	assertResponseAudioIndex(t, r)
}

func recordResponsePCM(t *testing.T, r *directoryRecorder, index int) {
	t.Helper()
	frame := audio.PCMFrame{Samples: []int16{int16(index + 1)}, Format: audio.PCM16DeviceFormat(24000), EndOfResponse: true, PlaybackResponse: audio.PlaybackResponse{ResponseID: fmt.Sprintf("response-%d", index)}}
	if err := r.RecordAudio(t.Context(), session.LiveAudioRecord{Direction: session.LiveRecordAgent, Timestamp: evidenceTime(), Frame: frame}); err != nil {
		t.Fatal(err)
	}
}

func recordResponseMessage(t *testing.T, r *directoryRecorder, position int) {
	t.Helper()
	msg := messages.StreamMessage{Role: messages.RoleAssistant, ResponseID: fmt.Sprintf("response-%d", position/2), Type: messages.StreamTypeMessageEnd}
	if position%2 == 0 {
		msg.Type = messages.StreamTypeTextDelta
		msg.Value = messages.NewTextDeltaValue(fmt.Sprintf("reply-%d", position/2))
	}
	if err := r.RecordMessage(t.Context(), session.LiveRecord{Direction: session.LiveRecordAgent, Timestamp: evidenceTime(), Message: msg}); err != nil {
		t.Fatal(err)
	}
}

func assertResponseAudioIndex(t *testing.T, r *directoryRecorder) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(string(readEvidenceFile(t, r, "session-log.jsonl"))), "\n")
	if len(lines) != 2 {
		t.Fatalf("recorded turns = %d, want 2: %s", len(lines), lines)
	}
	for index, line := range lines {
		var entry evidenceLogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		if !entry.Response.Complete || entry.Response.Text != fmt.Sprintf("reply-%d", index) || entry.Response.AudioBytes != 2 || entry.Response.AudioOffsetBytes != uint64(index*2) {
			t.Fatalf("response %d lost its audio association: %+v", index, entry.Response)
		}
	}
}

func TestMixedIdentifiedAndLegacyPCMUsesArtifactOffsets(t *testing.T) {
	r := newEvidenceRecorder(t)
	recordResponsePCM(t, r, 0)
	recordResponseMessage(t, r, 0)
	recordResponseMessage(t, r, 1)
	frame := audio.PCMFrame{Samples: []int16{2}, Format: audio.PCM16DeviceFormat(24000)}
	if err := r.RecordAudio(t.Context(), session.LiveAudioRecord{Direction: session.LiveRecordAgent, Timestamp: evidenceTime(), Frame: frame}); err != nil {
		t.Fatal(err)
	}
	recordResponseMessage(t, r, 2)
	recordResponseMessage(t, r, 3)
	recordEvidenceTerminal(t, r)
	if err := r.Finalize(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	assertResponseAudioIndex(t, r)
	if got := readEvidenceFile(t, r, "audio/out-000.pcm"); !bytes.Equal(got, []byte{1, 0, 2, 0}) {
		t.Fatalf("recorded PCM = %v", got)
	}
}
