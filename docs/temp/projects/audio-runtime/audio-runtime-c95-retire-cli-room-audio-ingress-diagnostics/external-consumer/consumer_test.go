package consumer_test

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomaudiodiagnostics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomaudiodiagnostics/wire"
)

type sink struct {
	records []roomaudiodiagnostics.Record
}

func (s *sink) Record(record roomaudiodiagnostics.Record) {
	fields := make(map[string]string, len(record.Fields))
	for key, value := range record.Fields {
		fields[key] = value
	}
	s.records = append(s.records, roomaudiodiagnostics.Record{Event: record.Event, Fields: fields})
}

func summary(records []roomaudiodiagnostics.Record) roomaudiodiagnostics.Record {
	for _, record := range records {
		if record.Event == roomaudiodiagnostics.EventRoomAudioIngressSummary {
			return record
		}
	}
	return roomaudiodiagnostics.Record{}
}

func TestTwoPublicWireServicesRemainIsolated(t *testing.T) {
	leftSink, rightSink := &sink{}, &sink{}
	left := wire.NewService(roomaudiodiagnostics.Options{ParticipantID: "left", RoomID: "room-left", Sink: leftSink})
	right := wire.NewService(roomaudiodiagnostics.Options{ParticipantID: "right", RoomID: "room-right", Sink: rightSink})

	if err := left.Admit("alice", roomaudiodiagnostics.Delivered, "mixer_admitted", 4, true); err != nil {
		t.Fatal(err)
	}
	if err := left.Admit("bob", roomaudiodiagnostics.Backpressured, "mixer_admitted_after_backpressure", 2, true); err != nil {
		t.Fatal(err)
	}
	left.ResolveFrame([]string{"bob", "alice"}, 4, "")
	left.Finish()

	if err := right.Record("carol", roomaudiodiagnostics.Rejected, "provider_input_rejected", 3); err != nil {
		t.Fatal(err)
	}
	right.Finish()

	leftSummary := summary(leftSink.records)
	rightSummary := summary(rightSink.records)
	if leftSummary.Fields[roomaudiodiagnostics.FieldParticipantID] != "left" || leftSummary.Fields[roomaudiodiagnostics.FieldRoomID] != "room-left" || leftSummary.Fields[roomaudiodiagnostics.FieldAcceptedBytes] != "6" {
		t.Fatalf("left summary = %v", leftSummary.Fields)
	}
	if rightSummary.Fields[roomaudiodiagnostics.FieldParticipantID] != "right" || rightSummary.Fields[roomaudiodiagnostics.FieldRoomID] != "room-right" || rightSummary.Fields[roomaudiodiagnostics.FieldRejectedBytes] != "3" {
		t.Fatalf("right summary = %v", rightSummary.Fields)
	}
	if leftSummary.Fields[roomaudiodiagnostics.FieldSourcePeers] != "alice,bob" || rightSummary.Fields[roomaudiodiagnostics.FieldSourcePeers] != "carol" {
		t.Fatalf("source attribution leaked across services: left=%v right=%v", leftSummary.Fields, rightSummary.Fields)
	}
}
