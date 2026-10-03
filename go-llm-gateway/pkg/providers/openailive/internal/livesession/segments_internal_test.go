package livesession

import (
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// testSegmentGap is the provider's default quiet gap G.
const testSegmentGap = 600 * time.Millisecond

// audioTypePCMU is the G.711 mu-law media type, whose bytes pass through.
const audioTypePCMU = "audio/pcmu"

func messageTypes(msgs []messages.StreamMessage) []messages.StreamMessageType {
	out := make([]messages.StreamMessageType, len(msgs))
	for i, msg := range msgs {
		out[i] = msg.Type
	}
	return out
}

// A user utterance closes when the next fragment starts a full gap after it
// on the server timeline, and empty fragments extend runs without deltas.
func TestSegmenterClosesUtterancesAtTimelineGapsAndKeepsEmptyFragmentsSilent(t *testing.T) {
	gap := testSegmentGap
	s := newSegmenter(gap, Format{Type: AudioTypePCM, Rate: 24000})
	now := time.Unix(0, 0)
	s.inputTranscript(now, Transcript{Delta: "first", StartMS: 0, EndMS: 100})
	if out := s.inputTranscript(now, Transcript{StartMS: 100, EndMS: 150}); len(out) != 0 {
		t.Fatalf("empty user fragment emitted %v", messageTypes(out))
	}
	out := s.inputTranscript(now, Transcript{Delta: "second", StartMS: 150 + gap.Milliseconds(), EndMS: 900})
	want := []messages.StreamMessageType{messages.StreamTypeTranscriptEnd, messages.StreamTypeInputItemAdded, messages.StreamTypeTranscriptDelta}
	if got := messageTypes(out); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("messages = %v, want the first utterance closed and a second opened", got)
	}
	if out := s.outputTranscript(now, Transcript{StartMS: 900, EndMS: 950}); len(out) != 1 || out[0].Type != messages.StreamTypeMessageStart {
		t.Fatalf("empty assistant fragment emitted %v, want only the segment start", messageTypes(out))
	}
}

// After a cancel, output is dropped while it keeps arriving within the gap;
// the suppression ends once the gap passes quietly.
func TestSegmenterSuppressesOutputAfterACancelUntilAQuietGap(t *testing.T) {
	gap := testSegmentGap
	s := newSegmenter(gap, Format{Type: audioTypePCMU, Rate: 8000})
	now := time.Unix(0, 0)
	s.outputAudio(now, []byte{1}, audioTypePCMU)
	if out := s.cancel(now); len(out) == 0 || out[len(out)-1].Type != messages.StreamTypeMessageEnd {
		t.Fatalf("cancel emitted %v, want the segment closed", messageTypes(out))
	}
	if out := s.cancel(now); out != nil {
		t.Fatalf("second cancel emitted %v, want nothing", messageTypes(out))
	}
	if deadline, pending := s.nextDeadline(); !pending || !deadline.Equal(now.Add(gap)) {
		t.Fatalf("next deadline = %v %v, want the end of the suppression", deadline, pending)
	}
	later := now.Add(gap / 2)
	if out := s.outputTranscript(later, Transcript{Delta: "late", StartMS: 10, EndMS: 20}); out != nil {
		t.Fatalf("suppressed transcript emitted %v", messageTypes(out))
	}
	if _, dropped := s.outputAudio(later, []byte{2}, audioTypePCMU); !dropped {
		t.Fatal("suppressed audio was not dropped")
	}
	s.due(later.Add(gap))
	if out, dropped := s.outputAudio(later.Add(gap), []byte{3}, audioTypePCMU); dropped || out[0].ResponseID != "live_seg_2" {
		t.Fatalf("output after the quiet gap = %v (dropped %v), want a new segment", messageTypes(out), dropped)
	}
	s2 := newSegmenter(gap, Format{Type: AudioTypePCM, Rate: 24000})
	s2.outputAudio(now, []byte{1, 0}, AudioTypePCM)
	s2.cancel(now)
	if out, dropped := s2.outputAudio(now.Add(gap), []byte{2, 0}, AudioTypePCM); dropped || len(out) == 0 {
		t.Fatalf("output a full gap after the cancel was dropped (%v)", messageTypes(out))
	}
}
