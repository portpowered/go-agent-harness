package service

import (
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// Observe folds one stream message into the reducer. inputIndex/outputIndex
// refer to the already-persisted audio segment indices and are never inferred
// from message arrival order.
func (s *Service) Observe(msg messages.StreamMessage, outbound bool, inputIndex, outputIndex int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observeLocked(msg, outbound, inputIndex, outputIndex)
}

func (s *Service) observeLocked(msg messages.StreamMessage, outbound bool, inputIndex, outputIndex int) {
	//nolint:exhaustive // Unknown protocol values intentionally fall through.
	switch msg.Type {
	case messages.StreamTypeInputItemAdded:
		s.observeInputItemAdded(msg)
	case messages.StreamTypeAudioDelta:
		s.observeAudioDelta(msg, outbound, inputIndex, outputIndex)
	case messages.StreamTypeTextDelta:
		s.observeTextDelta(msg, outbound)
	case messages.StreamTypeTranscriptDelta:
		s.observeTranscriptDelta(msg, outbound)
	case messages.StreamTypeTranscriptEnd:
		s.observeTranscriptEnd(msg, outbound)
	case messages.StreamTypeToolCallStart, messages.StreamTypeToolCallDelta, messages.StreamTypeToolCallEnd:
		s.observeToolCallBoundary(outbound)
	case messages.StreamTypeMessageEnd:
		s.observeMessageEnd(outbound)
	default:
		// Unknown or lifecycle-only messages do not contribute to the
		// conversation projection and are intentionally ignored.
		return
	}
}

func (s *Service) observeInputItemAdded(msg messages.StreamMessage) {
	added, ok := msg.Value.(*messages.InputItemAddedValue)
	if ok && added != nil && added.ItemID != "" {
		s.inputOrdinalForItem(added.ItemID)
	}
}

func (s *Service) observeAudioDelta(msg messages.StreamMessage, outbound bool, inputIndex, outputIndex int) {
	audio, ok := msg.Value.(*messages.AudioDeltaValue)
	if !ok || audio == nil {
		return
	}
	if outbound {
		s.current.inputAudioBytes += uint64(len(audio.Content))
		if inputIndex >= 0 {
			s.current.inputSegments = append(s.current.inputSegments, fmt.Sprintf("audio/in-%03d.pcm", inputIndex))
		}
		return
	}
	if s.current.inputCommitted && s.current.firstResponseAudioAt.IsZero() && len(audio.Content) > 0 {
		s.current.firstResponseAudioAt = s.wallNow()
	}
	s.current.outputAudioBytes += uint64(len(audio.Content))
	if outputIndex >= 0 {
		s.current.outputSegments = append(s.current.outputSegments, fmt.Sprintf("audio/out-%03d.pcm", outputIndex))
	}
}

func (s *Service) observeTextDelta(msg messages.StreamMessage, outbound bool) {
	text, ok := msg.Value.(*messages.TextDeltaValue)
	if !ok || text == nil {
		return
	}
	if outbound {
		s.current.inputText += text.Content
		return
	}
	s.current.responseDeltas.WriteString(text.Content)
}

func (s *Service) observeTranscriptDelta(msg messages.StreamMessage, outbound bool) {
	if outbound {
		return
	}
	transcript, ok := msg.Value.(*messages.TranscriptDeltaValue)
	if !ok || transcript == nil {
		return
	}
	if msg.Role == messages.RoleUser {
		s.observeUserTranscriptDelta(transcript)
		return
	}
	s.current.responseDeltas.WriteString(transcript.Text)
}

func (s *Service) observeUserTranscriptDelta(transcript *messages.TranscriptDeltaValue) {
	if transcript.ItemID == "" {
		s.current.inputTranscript.WriteString(transcript.Text)
		return
	}
	accum := s.inputAccum(s.inputOrdinalForItem(transcript.ItemID))
	if !accum.completed {
		accum.deltas.WriteString(transcript.Text)
	}
}

func (s *Service) observeTranscriptEnd(msg messages.StreamMessage, outbound bool) {
	if outbound {
		return
	}
	transcript, ok := msg.Value.(*messages.TranscriptEndValue)
	if !ok || transcript == nil {
		return
	}
	if msg.Role == messages.RoleUser {
		s.observeUserTranscriptEnd(transcript)
		return
	}
	if transcript.FullText != "" {
		s.current.responseFullText = transcript.FullText
	}
}

func (s *Service) observeUserTranscriptEnd(transcript *messages.TranscriptEndValue) {
	if transcript.ItemID != "" {
		accum := s.inputAccum(s.inputOrdinalForItem(transcript.ItemID))
		// Completion is authoritative even when FullText is empty: an interim
		// delta must never invent a user utterance.
		accum.completed = true
		accum.fullText = transcript.FullText
		return
	}
	s.current.inputTranscriptCompleted = true
	s.current.inputFullText = transcript.FullText
}

func (s *Service) observeToolCallBoundary(outbound bool) {
	if !outbound {
		// Provider tool-call MESSAGE.END is an intermediate boundary. Keep the
		// spoken turn open for the execution-boundary result and its later
		// continuation.
		s.current.toolCallMessage = true
	}
}

func (s *Service) observeMessageEnd(outbound bool) {
	if outbound {
		s.commitInput()
		return
	}
	if s.current.toolCallMessage {
		s.current.toolCallMessage = false
		return
	}
	s.closeTurnLocked()
}

func (s *Service) commitInput() {
	if s.current.inputCommitted {
		return
	}
	s.current.inputCommitted = true
	s.current.inputOrdinal = s.committedInputs
	s.current.inputOrdinalSet = true
	s.committedInputs++
	s.current.committedAt = s.wallNow()
}

func (s *Service) closeTurnLocked() {
	if s.current.observed() {
		closed := cloneTurn(&s.current)
		closed.complete = true
		s.closed = append(s.closed, closed)
	}
	s.current = turn{}
}
