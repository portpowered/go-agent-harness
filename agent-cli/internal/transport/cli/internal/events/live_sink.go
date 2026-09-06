package events

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

// LiveSink projects the runtime's bounded live observations into the CLI's
// ordered room stream. It deliberately owns no session or provider state.
type LiveSink struct {
	broker *Broker

	mu           sync.Mutex
	livenessSent map[string]struct{}
}

func NewLiveSink(broker *Broker) rooms.EventSink {
	if broker == nil {
		return nil
	}
	return &LiveSink{broker: broker, livenessSent: make(map[string]struct{})}
}

func (s *LiveSink) Publish(ctx context.Context, participantID string, event session.LiveEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.broker == nil {
		return nil
	}
	if classification, ok := livenessClassification(event); ok && s.markLiveness(participantID) {
		s.broker.Publish(EventParticipantLivenessFault, participantID, classification)
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(event.Kind)) {
	case string(messages.StreamTypeTranscriptDelta):
		s.broker.TranscriptDelta(participantID, event.Text)
	case string(messages.StreamTypeTranscriptEnd):
		s.broker.TranscriptEnd(participantID, event.Text)
	case string(session.LiveEventOverflow):
		s.broker.Diagnostic(participantID, "live_event_overflow", map[string]string{
			"dropped": fmt.Sprint(event.Dropped),
		})
	default:
		if event.Capability != nil || strings.HasPrefix(strings.ToLower(strings.TrimSpace(event.Kind)), "browser.") {
			s.broker.Diagnostic(participantID, "live_browser_event", map[string]string{
				"kind": event.Kind, "browser_id": event.BrowserID, "target_id": event.TargetID,
				"invocation_id": event.InvocationID, "state": event.State, "reason": event.Reason,
				"source_sequence": fmt.Sprint(capabilitySequence(event)),
				"tool_name":       capabilityToolName(event),
			})
		}
	}
	return nil
}

func (s *LiveSink) markLiveness(participantID string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, sent := s.livenessSent[participantID]; sent {
		return false
	}
	s.livenessSent[participantID] = struct{}{}
	return true
}

func livenessClassification(event session.LiveEvent) (string, bool) {
	if event.Liveness != nil {
		classification := strings.TrimSpace(event.Liveness.Classification)
		return classification, classification != ""
	}
	if event.Terminal == nil {
		return "", false
	}
	classification := strings.TrimSpace(event.Terminal.Classification)
	if classification == "silent_provider_empty_response" || classification == "silent_provider_timeout" {
		return classification, true
	}
	if event.Terminal.TerminalReason == messages.TerminalReasonPartialOutput && event.Terminal.OutputState == messages.TerminalOutputNone {
		return "silent_provider_empty_response", true
	}
	return "", false
}

func capabilitySequence(event session.LiveEvent) uint64 {
	if event.Capability == nil {
		return 0
	}
	return event.Capability.Sequence
}

func capabilityToolName(event session.LiveEvent) string {
	if event.Capability == nil {
		return ""
	}
	return event.Capability.ToolName
}
