package lifecycle

import (
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type terminalState struct {
	durationExpired  bool
	terminalWritten  bool
	responseOutput   bool
	responseComplete bool
	deadline         <-chan time.Time
}

func (s *terminalState) observe(msg messages.StreamMessage) {
	switch msg.Type {
	case messages.StreamTypeMessageStart:
		s.responseOutput = false
		s.responseComplete = false
	case messages.StreamTypeTextDelta, messages.StreamTypeReasoningDelta, messages.StreamTypeAudioDelta,
		messages.StreamTypeImageDelta, messages.StreamTypeVideoDelta, messages.StreamTypeFileDelta,
		messages.StreamTypeEmbeddingDelta, messages.StreamTypeToolCallDelta, messages.StreamTypeToolCallEnd,
		messages.StreamTypeRefusal:
		s.responseOutput = true
	case messages.StreamTypeTranscriptDelta:
		if msg.Role != messages.RoleUser {
			s.responseOutput = true
		}
	case messages.StreamTypeMessageEnd:
		s.responseComplete = true
	case messages.StreamTypeTextStart, messages.StreamTypeTextEnd,
		messages.StreamTypeToolCallStart,
		messages.StreamTypeAudioStart, messages.StreamTypeAudioEnd,
		messages.StreamTypeImageStart, messages.StreamTypeImageEnd,
		messages.StreamTypeVideoStart, messages.StreamTypeVideoEnd,
		messages.StreamTypeFileStart, messages.StreamTypeFileEnd,
		messages.StreamTypeEmbeddingStart, messages.StreamTypeEmbeddingEnd,
		messages.StreamTypeReasoningStart, messages.StreamTypeReasoningEnd,
		messages.StreamTypeVADSpeechStarted, messages.StreamTypeVADSpeechStopped,
		messages.StreamTypeTranscriptStart, messages.StreamTypeTranscriptEnd,
		messages.StreamTypeInputItemAdded, messages.StreamTypePong,
		messages.StreamTypeSessionOpen, messages.StreamTypeSessionClose,
		messages.StreamTypeSessionCreated, messages.StreamTypeSessionUpdated,
		messages.StreamTypeSessionUpdate, messages.StreamTypeResponseCancel,
		messages.StreamTypeResponseCreate, messages.StreamTypeLoopEnd,
		messages.StreamTypeUsageInfo, messages.StreamTypeError,
		messages.StreamTypeSystemFullMessage:
		return
	}
}

func (s *terminalState) snapshot() MessageState {
	return MessageState{
		DurationExpired:  s.durationExpired,
		TerminalWritten:  s.terminalWritten,
		ResponseOutput:   s.responseOutput,
		ResponseComplete: s.responseComplete,
		Deadline:         s.deadline,
	}
}

func (s *terminalState) outputState() messages.TerminalOutputState {
	if !s.responseOutput {
		return messages.TerminalOutputNone
	}
	if s.responseComplete {
		return messages.TerminalOutputComplete
	}
	return messages.TerminalOutputPartial
}
