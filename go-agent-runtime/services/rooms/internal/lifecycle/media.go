package lifecycle

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

type recordingEventSink struct {
	host     rooms.EventSink
	recorder roomevidence.Recorder
}

func (s recordingEventSink) Publish(ctx context.Context, participantID string, event session.LiveEvent) error {
	var hostErr error
	if s.host != nil {
		hostErr = s.host.Publish(ctx, participantID, event)
	}
	if s.recorder != nil {
		_ = s.recorder.RecordLiveEvent(participantID, event)
		if participant := s.recorder.Participant(participantID); participant != nil {
			if event.Message != nil {
				_ = participant.ObserveDelta(*event.Message)
			}
			_ = participant.RecordDiagnostic(roomevidence.DiagnosticRecord{Event: event.Kind, Fields: liveEventFields(event), At: event.Timestamp})
		}
	}
	return hostErr
}

func liveEventFields(event session.LiveEvent) map[string]string {
	fields := map[string]string{}
	if event.Reason != "" {
		fields["reason"] = event.Reason
	}
	if event.State != "" {
		fields["state"] = event.State
	}
	if event.ResponseID != "" {
		fields["response_id"] = event.ResponseID
	}
	if event.ToolCallID != "" {
		fields["tool_call_id"] = event.ToolCallID
	}
	if event.Text != "" {
		fields["text"] = event.Text
	}
	if event.Error != nil {
		fields["error"] = event.Error.Error()
	}
	if event.Liveness != nil {
		fields["classification"] = event.Liveness.Classification
		fields["terminal_reason"] = string(event.Liveness.TerminalReason)
		fields["terminal_provenance"] = string(event.Liveness.TerminalProvenance)
		fields["output_state"] = string(event.Liveness.OutputState)
	}
	return fields
}
