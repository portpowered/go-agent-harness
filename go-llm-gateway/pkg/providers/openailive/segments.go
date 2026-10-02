package openailive

import (
	"fmt"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// Synthetic ids. GPT-Live has no response or input item ids, so the provider
// numbers its speech segments and user utterances itself.
const (
	segmentIDPrefix   = "live_seg_"
	utteranceIDPrefix = "live_utt_"
)

// MESSAGE.END statuses of a speech segment.
const (
	segmentStatusCompleted = "completed"
	segmentStatusCancelled = "cancelled"
)

// segment is one open run of assistant speech.
type segment struct {
	id           string
	openedAt     time.Time
	lastOutputAt time.Time
	audioBytes   int64
	audioStarted bool
	transcript   strings.Builder
	// transcriptEndMS is the server-timeline end of the last output
	// transcript fragment; hasTranscript says whether one arrived.
	transcriptEndMS int64
	hasTranscript   bool
}

// utterance is one open run of user speech, known from input transcripts.
type utterance struct {
	id     string
	text   strings.Builder
	endMS  int64
	lastAt time.Time
}

// segmenter turns GPT-Live's boundary-free output and transcripts into
// speech segments (MESSAGE.START .. MESSAGE.END) and user utterances
// (INPUT_ITEM.ADDED .. TRANSCRIPT.END). It does no I/O and is not safe for
// concurrent use; the session serializes it.
//
// A segment opens at the first assistant audio or transcript while none is
// open, and closes at the first of: an output-transcript gap of at least gap
// on the server timeline, gap of quiet on the clock after its last output
// (and after its audio, measured by byte count, would have finished
// playing), a cancel, or the end of the session. An utterance closes at a
// server-timeline gap of at least gap before the next transcript fragment,
// after gap of quiet on the clock, or at the end of the session.
type segmenter struct {
	gap time.Duration
	// bytesPerSecond converts output audio bytes to playback duration.
	bytesPerSecond int64

	segments, utterances int
	seg                  *segment
	utt                  *utterance

	// suppressing drops output after a cancel until gap of quiet passes.
	suppressing      bool
	lastSuppressedAt time.Time
}

func newSegmenter(gap time.Duration, format AudioFormat) *segmenter {
	bytesPerSecond := int64(format.Rate)
	if format.Type == AudioTypePCM {
		bytesPerSecond *= 2
	}
	return &segmenter{gap: gap, bytesPerSecond: bytesPerSecond}
}

// outputAudio maps one output audio chunk. dropped is true while output is
// suppressed after a cancel.
func (s *segmenter) outputAudio(now time.Time, audio []byte, mediaType string) (out []messages.StreamMessage, dropped bool) {
	if s.suppress(now) {
		return nil, true
	}
	out = s.openSegment(now)
	seg := s.seg
	if !seg.audioStarted {
		seg.audioStarted = true
		out = append(out, messages.StreamMessage{Type: messages.StreamTypeAudioStart, ResponseID: seg.id, Value: messages.NewAudioStartValue()})
	}
	seg.audioBytes += int64(len(audio))
	seg.lastOutputAt = now
	out = append(out, messages.StreamMessage{
		Type: messages.StreamTypeAudioDelta, ResponseID: seg.id,
		Value: messages.NewAudioDeltaValueWithMediaType(audio, mediaType),
	})
	return out, false
}

// outputTranscript maps one assistant transcript fragment.
func (s *segmenter) outputTranscript(now time.Time, fragment TranscriptDelta) []messages.StreamMessage {
	if s.suppress(now) {
		return nil
	}
	out := s.closeUtteranceBefore(fragment.StartMS)
	if seg := s.seg; seg != nil && seg.hasTranscript && fragment.StartMS-seg.transcriptEndMS >= s.gap.Milliseconds() {
		out = append(out, s.closeSegment(segmentStatusCompleted)...)
	}
	out = append(out, s.openSegment(now)...)
	seg := s.seg
	seg.lastOutputAt = now
	seg.hasTranscript = true
	seg.transcriptEndMS = fragment.EndMS
	seg.transcript.WriteString(fragment.Delta)
	if fragment.Delta == "" {
		return out
	}
	return append(out, messages.StreamMessage{
		Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant, ResponseID: seg.id,
		Value: messages.NewTranscriptDeltaValue(fragment.Delta),
	})
}

// inputTranscript maps one user transcript fragment. User transcripts never
// touch the open segment: full-duplex overlap is not an interruption.
func (s *segmenter) inputTranscript(now time.Time, fragment TranscriptDelta) []messages.StreamMessage {
	out := s.closeUtteranceBefore(fragment.StartMS)
	if s.utt == nil {
		s.utterances++
		s.utt = &utterance{id: fmt.Sprintf("%s%d", utteranceIDPrefix, s.utterances)}
		out = append(out, messages.StreamMessage{Type: messages.StreamTypeInputItemAdded, Value: messages.NewInputItemAddedValue(s.utt.id)})
	}
	utt := s.utt
	utt.lastAt = now
	utt.endMS = fragment.EndMS
	utt.text.WriteString(fragment.Delta)
	if fragment.Delta == "" {
		return out
	}
	return append(out, messages.StreamMessage{
		Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleUser,
		Value: messages.NewTranscriptDeltaValueForItem(fragment.Delta, utt.id),
	})
}

// cancel ends the open segment as cancelled and drops later output until gap
// of quiet passes, because GPT-Live keeps no response the cancel can reach.
func (s *segmenter) cancel(now time.Time) []messages.StreamMessage {
	if s.seg == nil {
		return nil
	}
	s.suppressing = true
	s.lastSuppressedAt = now
	return s.closeSegment(segmentStatusCancelled)
}

// due closes whatever has been quiet for gap by now.
func (s *segmenter) due(now time.Time) []messages.StreamMessage {
	var out []messages.StreamMessage
	if s.utt != nil && !now.Before(s.utt.lastAt.Add(s.gap)) {
		out = append(out, s.closeUtterance()...)
	}
	if s.seg != nil && !now.Before(s.segmentDeadline()) {
		out = append(out, s.closeSegment(segmentStatusCompleted)...)
	}
	if s.suppressing && !now.Before(s.lastSuppressedAt.Add(s.gap)) {
		s.suppressing = false
	}
	return out
}

// nextDeadline is the earliest time due has work, if anything is pending.
func (s *segmenter) nextDeadline() (time.Time, bool) {
	var next time.Time
	pending := false
	consider := func(at time.Time) {
		if !pending || at.Before(next) {
			next, pending = at, true
		}
	}
	if s.utt != nil {
		consider(s.utt.lastAt.Add(s.gap))
	}
	if s.seg != nil {
		consider(s.segmentDeadline())
	}
	if s.suppressing {
		consider(s.lastSuppressedAt.Add(s.gap))
	}
	return next, pending
}

// finish closes the open utterance and segment at the end of the session
// and reports whether a segment was still open.
func (s *segmenter) finish() (out []messages.StreamMessage, segmentOpen bool) {
	segmentOpen = s.seg != nil
	out = append(out, s.closeUtterance()...)
	out = append(out, s.closeSegment(segmentStatusCompleted)...)
	s.suppressing = false
	return out, segmentOpen
}

// segmentDeadline is gap after the later of the last output and the moment
// the segment's audio would finish playing.
func (s *segmenter) segmentDeadline() time.Time {
	seg := s.seg
	end := seg.lastOutputAt
	if s.bytesPerSecond > 0 {
		played := seg.openedAt.Add(time.Duration(seg.audioBytes * int64(time.Second) / s.bytesPerSecond))
		if played.After(end) {
			end = played
		}
	}
	return end.Add(s.gap)
}

func (s *segmenter) suppress(now time.Time) bool {
	if !s.suppressing {
		return false
	}
	if !now.Before(s.lastSuppressedAt.Add(s.gap)) {
		s.suppressing = false
		return false
	}
	s.lastSuppressedAt = now
	return true
}

func (s *segmenter) openSegment(now time.Time) []messages.StreamMessage {
	if s.seg != nil {
		return nil
	}
	s.segments++
	s.seg = &segment{id: fmt.Sprintf("%s%d", segmentIDPrefix, s.segments), openedAt: now, lastOutputAt: now}
	return []messages.StreamMessage{{Type: messages.StreamTypeMessageStart, ResponseID: s.seg.id, Value: messages.NewMessageStartValue()}}
}

func (s *segmenter) closeSegment(status string) []messages.StreamMessage {
	seg := s.seg
	if seg == nil {
		return nil
	}
	s.seg = nil
	var out []messages.StreamMessage
	if seg.audioStarted {
		out = append(out, messages.StreamMessage{Type: messages.StreamTypeAudioEnd, ResponseID: seg.id, Value: messages.NewAudioEndValue()})
	}
	if seg.hasTranscript {
		out = append(out, messages.StreamMessage{
			Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleAssistant, ResponseID: seg.id,
			Value: messages.NewTranscriptEndValue(seg.transcript.String()),
		})
	}
	return append(out, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, ResponseID: seg.id, Value: segmentEnd(status)})
}

// closeUtteranceBefore closes the open utterance when a fragment starting at
// startMS follows its end by at least gap on the server timeline.
func (s *segmenter) closeUtteranceBefore(startMS int64) []messages.StreamMessage {
	if s.utt == nil || startMS-s.utt.endMS < s.gap.Milliseconds() {
		return nil
	}
	return s.closeUtterance()
}

func (s *segmenter) closeUtterance() []messages.StreamMessage {
	utt := s.utt
	if utt == nil {
		return nil
	}
	s.utt = nil
	return []messages.StreamMessage{{
		Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleUser,
		Value: messages.NewTranscriptEndValueForItem(utt.text.String(), utt.id),
	}}
}

// segmentEnd is the MESSAGE.END of a segment. A segment the caller cancelled
// ends cancelled with partial output; every other segment ends completed.
func segmentEnd(status string) *messages.MessageEndValue {
	if status == segmentStatusCancelled {
		value := messages.NewMessageEndValueWithTerminal(messages.TokenUsage{}, messages.TerminalReasonCancellation, messages.TerminalProvenanceProvider, messages.TerminalOutputPartial)
		value.Status = status
		value.TerminalSource = messages.TerminalSourceProvider
		return value
	}
	value := messages.NewMessageEndValueWithTerminal(messages.TokenUsage{}, messages.TerminalReasonProviderAuthoredCompletion, messages.TerminalProvenanceProvider, messages.TerminalOutputComplete)
	value.Status = status
	value.TerminalSource = messages.TerminalSourceProvider
	return value
}
