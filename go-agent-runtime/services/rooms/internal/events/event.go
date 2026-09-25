package events

import (
	"encoding/json"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

// Event is the stable JSON projection carried by one stream frame.
type Event struct {
	Type          string            `json:"type"`
	ParticipantID string            `json:"participant_id"`
	Event         string            `json:"event"`
	Fields        map[string]string `json:"fields"`
	Text          string            `json:"text"`
	FullText      string            `json:"full_text"`
	Reason        string            `json:"reason"`
	TS            string            `json:"ts"`
}

// redacted returns a copy whose every projected string passed through redact.
// The caller's field map is never mutated.
func (e Event) redacted(redact func(string) string) Event {
	e.ParticipantID = redact(e.ParticipantID)
	e.Event = redact(e.Event)
	e.Text = redact(e.Text)
	e.FullText = redact(e.FullText)
	e.Reason = redact(e.Reason)
	if e.Fields != nil {
		fields := make(map[string]string, len(e.Fields))
		for key, value := range e.Fields {
			fields[redact(key)] = redact(value)
		}
		e.Fields = fields
	}
	return e
}

// MarshalJSON encodes only the fields that belong to the event's type.
func (e Event) MarshalJSON() ([]byte, error) {
	switch e.Type {
	case rooms.RoomStreamTypeDiagnostic:
		fields := e.Fields
		if fields == nil {
			fields = map[string]string{}
		}
		return json.Marshal(struct {
			Type          string            `json:"type"`
			ParticipantID string            `json:"participant_id"`
			Event         string            `json:"event"`
			Fields        map[string]string `json:"fields"`
			TS            string            `json:"ts"`
		}{e.Type, e.ParticipantID, e.Event, fields, e.TS})
	case rooms.RoomStreamTypeTranscriptDelta:
		return json.Marshal(struct {
			Type          string `json:"type"`
			ParticipantID string `json:"participant_id"`
			Text          string `json:"text"`
			TS            string `json:"ts"`
		}{e.Type, e.ParticipantID, e.Text, e.TS})
	case rooms.RoomStreamTypeTranscriptEnd:
		return json.Marshal(struct {
			Type          string `json:"type"`
			ParticipantID string `json:"participant_id"`
			FullText      string `json:"full_text"`
			TS            string `json:"ts"`
		}{e.Type, e.ParticipantID, e.FullText, e.TS})
	case rooms.RoomStreamTypeRoom:
		return json.Marshal(struct {
			Type          string `json:"type"`
			Event         string `json:"event"`
			ParticipantID string `json:"participant_id"`
			Reason        string `json:"reason,omitempty"`
			TS            string `json:"ts"`
		}{e.Type, e.Event, e.ParticipantID, e.Reason, e.TS})
	default:
		return nil, fmt.Errorf("unknown room stream event type %q", e.Type)
	}
}
