package wire

import (
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomaudiodiagnostics"
)

const alicePeer = "alice"

type recordSink struct {
	mu      sync.Mutex
	records []roomaudiodiagnostics.Record
}

func (s *recordSink) Record(record roomaudiodiagnostics.Record) {
	if s == nil {
		return
	}
	fields := make(map[string]string, len(record.Fields))
	for key, value := range record.Fields {
		fields[key] = value
	}
	s.mu.Lock()
	s.records = append(s.records, roomaudiodiagnostics.Record{Event: record.Event, Fields: fields})
	s.mu.Unlock()
}

func (s *recordSink) all() []roomaudiodiagnostics.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]roomaudiodiagnostics.Record(nil), s.records...)
}

func recordsFor(records []roomaudiodiagnostics.Record, event string) []roomaudiodiagnostics.Record {
	filtered := make([]roomaudiodiagnostics.Record, 0)
	for _, record := range records {
		if record.Event == event {
			filtered = append(filtered, record)
		}
	}
	return filtered
}

func TestWireCreatesIndependentServicesAndCopiesRecords(t *testing.T) {
	leftSink, rightSink := &recordSink{}, &recordSink{}
	left := NewService(roomaudiodiagnostics.Options{ParticipantID: "left", RoomID: "room-a", Sink: leftSink})
	right := NewService(roomaudiodiagnostics.Options{ParticipantID: "right", RoomID: "room-b", Sink: rightSink})
	if left == nil || right == nil {
		t.Fatal("Wire returned nil service")
	}
	if err := left.Admit(alicePeer, roomaudiodiagnostics.Delivered, "mixer_admitted", 4, true); err != nil {
		t.Fatal(err)
	}
	left.ResolveFrame([]string{alicePeer}, 4, "")
	left.Finish()
	right.Finish()

	leftRecords := leftSink.all()
	rightRecords := rightSink.all()
	leftSummary := recordsFor(leftRecords, roomaudiodiagnostics.EventRoomAudioIngressSummary)
	rightSummary := recordsFor(rightRecords, roomaudiodiagnostics.EventRoomAudioIngressSummary)
	if len(leftSummary) != 1 || leftSummary[0].Fields[roomaudiodiagnostics.FieldParticipantID] != "left" || leftSummary[0].Fields[roomaudiodiagnostics.FieldContentfulBytes] != "4" {
		t.Fatalf("left summary = %v", leftSummary)
	}
	if len(rightSummary) != 1 || rightSummary[0].Fields[roomaudiodiagnostics.FieldReason] != roomaudiodiagnostics.ReasonNoContentfulPeerAudio {
		t.Fatalf("right summary = %v", rightSummary)
	}
	leftRecords[0].Fields[roomaudiodiagnostics.FieldSourcePeer] = "mutated"
	if leftSummary[0].Fields[roomaudiodiagnostics.FieldSourcePeer] == "mutated" {
		t.Fatal("summary aliases a different emitted record")
	}
}

func TestFIFOPartialFramesKeepSortedSourceAndDisposition(t *testing.T) {
	sink := &recordSink{}
	service := NewService(roomaudiodiagnostics.Options{ParticipantID: "target", Sink: sink})
	for _, admission := range []struct {
		source      string
		disposition roomaudiodiagnostics.Disposition
		reason      string
		bytes       int
		contentful  bool
	}{
		{alicePeer, roomaudiodiagnostics.Delivered, "alice-delivered", 4, true},
		{alicePeer, roomaudiodiagnostics.Backpressured, "alice-waited", 6, true},
		{alicePeer, roomaudiodiagnostics.Delivered, "alice-silent", 3, false},
		{"bob", roomaudiodiagnostics.Delivered, "bob-delivered", 5, true},
	} {
		if err := service.Admit(admission.source, admission.disposition, admission.reason, admission.bytes, admission.contentful); err != nil {
			t.Fatal(err)
		}
	}
	service.ResolveFrame([]string{"bob", alicePeer}, 4, "")
	service.ResolveFrame([]string{alicePeer, "bob"}, 4, "")
	service.ResolveFrame([]string{alicePeer}, 5, "")
	service.Finish()

	records := recordsFor(sink.all(), roomaudiodiagnostics.EventRoomAudioIngress)
	if len(records) != 3 {
		t.Fatalf("first observations = %d, want three keys: %v", len(records), records)
	}
	if records[0].Fields[roomaudiodiagnostics.FieldSourcePeer] != alicePeer || records[0].Fields[roomaudiodiagnostics.FieldDisposition] != "delivered" || records[0].Fields[roomaudiodiagnostics.FieldByteCount] != "4" {
		t.Fatalf("first alice observation = %v", records[0])
	}
	if records[1].Fields[roomaudiodiagnostics.FieldSourcePeer] != "bob" || records[1].Fields[roomaudiodiagnostics.FieldByteCount] != "4" {
		t.Fatalf("first bob observation = %v", records[1])
	}
	if records[2].Fields[roomaudiodiagnostics.FieldSourcePeer] != alicePeer || records[2].Fields[roomaudiodiagnostics.FieldDisposition] != "backpressured" || records[2].Fields[roomaudiodiagnostics.FieldByteCount] != "4" {
		t.Fatalf("first backpressured observation = %v", records[2])
	}
	summaries := recordsFor(sink.all(), roomaudiodiagnostics.EventRoomAudioIngressSummary)
	if len(summaries) != 1 {
		t.Fatalf("summary count = %d", len(summaries))
	}
	fields := summaries[0].Fields
	for key, want := range map[string]string{
		roomaudiodiagnostics.FieldSourcePeers:        "alice,bob",
		roomaudiodiagnostics.FieldContentfulBytes:    "15",
		roomaudiodiagnostics.FieldAcceptedBytes:      "15",
		roomaudiodiagnostics.FieldDeliveredBytes:     "9",
		roomaudiodiagnostics.FieldBackpressuredBytes: "6",
		roomaudiodiagnostics.FieldRejectedBytes:      "0",
		roomaudiodiagnostics.FieldContentLoss:        "false",
	} {
		if fields[key] != want {
			t.Fatalf("summary[%q] = %q, want %q; fields=%v", key, fields[key], want, fields)
		}
	}
}

func TestDownstreamRejectionPreservesEachSource(t *testing.T) {
	sink := &recordSink{}
	service := NewService(roomaudiodiagnostics.Options{ParticipantID: "target", Sink: sink})
	if err := service.Admit(alicePeer, roomaudiodiagnostics.Delivered, "mixer_admitted", 3, true); err != nil {
		t.Fatal(err)
	}
	if err := service.Admit("bob", roomaudiodiagnostics.Backpressured, "mixer_waited", 3, true); err != nil {
		t.Fatal(err)
	}
	service.ResolveFrame([]string{"bob", alicePeer}, 3, roomaudiodiagnostics.ReasonProviderInputRejected)
	service.Finish()
	records := recordsFor(sink.all(), roomaudiodiagnostics.EventRoomAudioIngress)
	if len(records) != 2 {
		t.Fatalf("rejection observations = %v", records)
	}
	sources := []string{records[0].Fields[roomaudiodiagnostics.FieldSourcePeer], records[1].Fields[roomaudiodiagnostics.FieldSourcePeer]}
	sort.Strings(sources)
	if sources[0] != alicePeer || sources[1] != "bob" {
		t.Fatalf("rejection sources = %v", sources)
	}
	for _, record := range records {
		if record.Fields[roomaudiodiagnostics.FieldDisposition] != "rejected" || record.Fields[roomaudiodiagnostics.FieldReason] != roomaudiodiagnostics.ReasonProviderInputRejected {
			t.Fatalf("rejection record = %v", record)
		}
	}
	if got := recordsFor(sink.all(), roomaudiodiagnostics.EventRoomAudioIngressSummary)[0].Fields[roomaudiodiagnostics.FieldAcceptedBytes]; got != "0" {
		t.Fatalf("accepted bytes = %q, want zero", got)
	}
}

func TestPendingContentfulLossAndSilentInputAreDistinct(t *testing.T) {
	sink := &recordSink{}
	service := NewService(roomaudiodiagnostics.Options{ParticipantID: "target", Sink: sink})
	if err := service.Admit(alicePeer, roomaudiodiagnostics.Delivered, "mixer_admitted", 10, true); err != nil {
		t.Fatal(err)
	}
	if err := service.Admit(alicePeer, roomaudiodiagnostics.Delivered, "mixer_admitted", 4, false); err != nil {
		t.Fatal(err)
	}
	service.ResolveFrame([]string{alicePeer}, 4, "")
	service.Finish()
	summary := recordsFor(sink.all(), roomaudiodiagnostics.EventRoomAudioIngressSummary)[0].Fields
	if summary[roomaudiodiagnostics.FieldContentfulBytes] != "10" || summary[roomaudiodiagnostics.FieldDeliveredBytes] != "4" || summary[roomaudiodiagnostics.FieldRejectedBytes] != "6" || summary[roomaudiodiagnostics.FieldContentLoss] != "true" {
		t.Fatalf("pending loss summary = %v", summary)
	}

	emptySink := &recordSink{}
	empty := NewService(roomaudiodiagnostics.Options{ParticipantID: "silent", Sink: emptySink})
	if err := empty.Admit(alicePeer, roomaudiodiagnostics.Delivered, "mixer_admitted", 4, false); err != nil {
		t.Fatal(err)
	}
	empty.Finish()
	emptySummary := recordsFor(emptySink.all(), roomaudiodiagnostics.EventRoomAudioIngressSummary)[0].Fields
	if emptySummary[roomaudiodiagnostics.FieldReason] != roomaudiodiagnostics.ReasonNoContentfulPeerAudio || emptySummary[roomaudiodiagnostics.FieldContentLoss] != "false" {
		t.Fatalf("silent summary = %v", emptySummary)
	}
}

func TestInvalidDispositionIsTypedAndRecordedRejected(t *testing.T) {
	sink := &recordSink{}
	service := NewService(roomaudiodiagnostics.Options{ParticipantID: "target", Sink: sink})
	err := service.Record(alicePeer, roomaudiodiagnostics.Disposition("future"), "ignored", 7)
	if !errors.Is(err, roomaudiodiagnostics.ErrInvalidDisposition) {
		t.Fatalf("invalid disposition error = %v", err)
	}
	service.Finish()
	summary := recordsFor(sink.all(), roomaudiodiagnostics.EventRoomAudioIngressSummary)[0].Fields
	if summary[roomaudiodiagnostics.FieldRejectedBytes] != "7" || summary[roomaudiodiagnostics.FieldReason] != roomaudiodiagnostics.ReasonParticipantTerminated {
		t.Fatalf("invalid summary = %v", summary)
	}
}

func TestFirstObservationsAreBoundedAndFinishIsIdempotent(t *testing.T) {
	sink := &recordSink{}
	service := NewService(roomaudiodiagnostics.Options{ParticipantID: "target", Sink: sink})
	for index := 0; index < roomaudiodiagnostics.MaxFirstEvents+8; index++ {
		if err := service.Record("peer-"+string(rune('a'+index)), roomaudiodiagnostics.Rejected, "reason", 1); err != nil {
			t.Fatal(err)
		}
	}
	service.Finish()
	service.Finish()
	if got := len(recordsFor(sink.all(), roomaudiodiagnostics.EventRoomAudioIngress)); got != roomaudiodiagnostics.MaxFirstEvents {
		t.Fatalf("first observation count = %d, want %d", got, roomaudiodiagnostics.MaxFirstEvents)
	}
	if got := len(recordsFor(sink.all(), roomaudiodiagnostics.EventRoomAudioIngressSummary)); got != 1 {
		t.Fatalf("summary count = %d, want one", got)
	}
}

func TestSinkCanReenterFinish(t *testing.T) {
	var service roomaudiodiagnostics.Service
	finished := make(chan struct{})
	service = NewService(roomaudiodiagnostics.Options{ParticipantID: "target", Sink: roomaudiodiagnostics.SinkFunc(func(record roomaudiodiagnostics.Record) {
		if record.Event == roomaudiodiagnostics.EventRoomAudioIngress {
			service.Finish()
			close(finished)
		}
	})})
	if err := service.Record(alicePeer, roomaudiodiagnostics.Delivered, "mixer_admitted", 1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("reentrant sink did not return")
	}
}
