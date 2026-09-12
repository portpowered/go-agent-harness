package service

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomaudiodiagnostics"
)

type testSink struct {
	mu       sync.Mutex
	records  []roomaudiodiagnostics.Record
	onRecord func(roomaudiodiagnostics.Record)
}

func (s *testSink) Record(record roomaudiodiagnostics.Record) {
	fields := make(map[string]string, len(record.Fields))
	for key, value := range record.Fields {
		fields[key] = value
	}
	s.mu.Lock()
	s.records = append(s.records, roomaudiodiagnostics.Record{Event: record.Event, Fields: fields})
	onRecord := s.onRecord
	s.mu.Unlock()
	if onRecord != nil {
		onRecord(record)
	}
}

func (s *testSink) recordsFor(event string) []roomaudiodiagnostics.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]roomaudiodiagnostics.Record, 0)
	for _, record := range s.records {
		if record.Event == event {
			result = append(result, record)
		}
	}
	return result
}

func TestAdmissionDefaultsAndPublicRejection(t *testing.T) {
	sink := &testSink{}
	service := New(roomaudiodiagnostics.Options{ParticipantID: "target", Sink: sink})
	if err := service.Admit(" ", roomaudiodiagnostics.Delivered, "", 4, true); err != nil {
		t.Fatal(err)
	}
	if err := service.Admit("alice", roomaudiodiagnostics.Backpressured, "waited", 2, true); err != nil {
		t.Fatal(err)
	}
	if err := service.Admit("alice", roomaudiodiagnostics.Rejected, "bad", 2, true); !errors.Is(err, roomaudiodiagnostics.ErrInvalidDisposition) {
		t.Fatalf("invalid admission error = %v", err)
	}
	service.ResolveFrame([]string{"alice", "room-mix"}, 4, "")
	service.Finish()
	summary := serviceSummary(t, sink)
	if summary.Fields[roomaudiodiagnostics.FieldRoomID] != "room" || summary.Fields[roomaudiodiagnostics.FieldSourcePeers] != "alice,room-mix" {
		t.Fatalf("summary identity = %v", summary.Fields)
	}
}

func TestResolveFrameConsumesPartialFIFOAndIgnoresSilentRanges(t *testing.T) {
	sink := &testSink{}
	service := New(roomaudiodiagnostics.Options{ParticipantID: "target", RoomID: "room-1", Sink: sink})
	for _, item := range []struct {
		source      string
		disposition roomaudiodiagnostics.Disposition
		reason      string
		bytes       int
		contentful  bool
	}{
		{"alice", roomaudiodiagnostics.Delivered, "first", 5, true},
		{"alice", roomaudiodiagnostics.Backpressured, "second", 4, true},
		{"alice", roomaudiodiagnostics.Delivered, "silent", 3, false},
		{"bob", roomaudiodiagnostics.Delivered, "bob", 7, true},
	} {
		if err := service.Admit(item.source, item.disposition, item.reason, item.bytes, item.contentful); err != nil {
			t.Fatal(err)
		}
	}
	service.ResolveFrame([]string{"bob", "alice", "alice"}, 4, "")
	service.ResolveFrame([]string{"alice", "bob"}, 4, "")
	service.ResolveFrame([]string{"alice"}, 4, "")
	service.Finish()
	summary := serviceSummary(t, sink)
	if summary.Fields[roomaudiodiagnostics.FieldContentfulBytes] != "16" || summary.Fields[roomaudiodiagnostics.FieldDeliveredBytes] != "12" || summary.Fields[roomaudiodiagnostics.FieldBackpressuredBytes] != "4" {
		t.Fatalf("FIFO totals = %v", summary.Fields)
	}
	if summary.Fields[roomaudiodiagnostics.FieldRejectedBytes] != "0" || summary.Fields[roomaudiodiagnostics.FieldContentLoss] != "false" {
		t.Fatalf("silent range changed rejection totals = %v", summary.Fields)
	}
}

func TestDownstreamRejectionAndPendingLossPreserveSources(t *testing.T) {
	sink := &testSink{}
	service := New(roomaudiodiagnostics.Options{ParticipantID: "target", Sink: sink})
	if err := service.Admit("bob", roomaudiodiagnostics.Delivered, "delivered", 5, true); err != nil {
		t.Fatal(err)
	}
	if err := service.Admit("alice", roomaudiodiagnostics.Backpressured, "waited", 6, true); err != nil {
		t.Fatal(err)
	}
	service.ResolveFrame([]string{"bob", "alice"}, 3, roomaudiodiagnostics.ReasonParticipantOutputRejected)
	service.Finish()
	records := sink.recordsFor(roomaudiodiagnostics.EventRoomAudioIngress)
	if len(records) != 4 {
		t.Fatalf("first records = %v", records)
	}
	if records[0].Fields[roomaudiodiagnostics.FieldSourcePeer] != "alice" || records[1].Fields[roomaudiodiagnostics.FieldSourcePeer] != "bob" {
		t.Fatalf("sorted source records = %v", records)
	}
	for _, record := range records[:2] {
		if record.Fields[roomaudiodiagnostics.FieldDisposition] != "rejected" || record.Fields[roomaudiodiagnostics.FieldReason] != roomaudiodiagnostics.ReasonParticipantOutputRejected {
			t.Fatalf("downstream record = %v", record)
		}
	}
	for _, record := range records[2:] {
		if record.Fields[roomaudiodiagnostics.FieldDisposition] != "rejected" || record.Fields[roomaudiodiagnostics.FieldReason] != roomaudiodiagnostics.ReasonParticipantTerminated {
			t.Fatalf("pending record = %v", record)
		}
	}
	summary := serviceSummary(t, sink)
	if summary.Fields[roomaudiodiagnostics.FieldRejectedBytes] != "11" || summary.Fields[roomaudiodiagnostics.FieldContentfulBytes] != "11" || summary.Fields[roomaudiodiagnostics.FieldAcceptedBytes] != "0" {
		t.Fatalf("rejection totals = %v", summary.Fields)
	}
}

func TestRejectPendingIsBoundedAndFinishIsIdempotent(t *testing.T) {
	sink := &testSink{}
	service := New(roomaudiodiagnostics.Options{ParticipantID: "target", Sink: sink})
	if err := service.Admit("alice", roomaudiodiagnostics.Delivered, "delivered", 4, true); err != nil {
		t.Fatal(err)
	}
	if err := service.Admit("bob", roomaudiodiagnostics.Backpressured, "waited", 5, true); err != nil {
		t.Fatal(err)
	}
	if err := service.Admit("silent", roomaudiodiagnostics.Delivered, "silent", 8, false); err != nil {
		t.Fatal(err)
	}
	service.RejectPending("")
	service.RejectPending(roomaudiodiagnostics.ReasonMixerClosed)
	service.Finish()
	service.Finish()
	summary := serviceSummary(t, sink)
	if summary.Fields[roomaudiodiagnostics.FieldRejectedBytes] != "9" || summary.Fields[roomaudiodiagnostics.FieldContentfulBytes] != "9" || summary.Fields[roomaudiodiagnostics.FieldContentLoss] != "true" {
		t.Fatalf("pending summary = %v", summary.Fields)
	}
	if len(sink.recordsFor(roomaudiodiagnostics.EventRoomAudioIngressSummary)) != 1 {
		t.Fatal("duplicate summary emitted")
	}
}

func TestInvalidDispositionRecordsFailClosedAndPreservesTypedError(t *testing.T) {
	sink := &testSink{}
	service := New(roomaudiodiagnostics.Options{ParticipantID: "target", Sink: sink})
	err := service.Record("alice", roomaudiodiagnostics.Disposition("unknown"), "caller-reason", 3)
	var typed roomaudiodiagnostics.InvalidDispositionError
	if !errors.As(err, &typed) || !errors.Is(err, roomaudiodiagnostics.ErrInvalidDisposition) || typed.Value != "unknown" {
		t.Fatalf("invalid error = %T %v", err, err)
	}
	service.Finish()
	records := sink.recordsFor(roomaudiodiagnostics.EventRoomAudioIngress)
	if len(records) != 1 || records[0].Fields[roomaudiodiagnostics.FieldDisposition] != "rejected" || records[0].Fields[roomaudiodiagnostics.FieldReason] != roomaudiodiagnostics.ReasonInvalidDisposition {
		t.Fatalf("invalid record = %v", records)
	}
}

func TestConcurrentRecordsAndFinishAreRaceSafe(t *testing.T) {
	sink := &testSink{}
	service := New(roomaudiodiagnostics.Options{ParticipantID: "target", Sink: sink})
	var group sync.WaitGroup
	for index := 0; index < 64; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			_ = service.Record(fmt.Sprintf("peer-%03d", index), roomaudiodiagnostics.Delivered, "concurrent", 2)
		}(index)
	}
	group.Wait()
	service.Finish()
	if got := len(sink.recordsFor(roomaudiodiagnostics.EventRoomAudioIngress)); got != roomaudiodiagnostics.MaxFirstEvents {
		t.Fatalf("bounded records = %d, want %d", got, roomaudiodiagnostics.MaxFirstEvents)
	}
}

func TestReentrantSinkDoesNotHoldLedgerLock(t *testing.T) {
	finished := make(chan struct{})
	var service *Service
	sink := &testSink{}
	sink.onRecord = func(record roomaudiodiagnostics.Record) {
		if record.Event == roomaudiodiagnostics.EventRoomAudioIngress {
			service.Finish()
			close(finished)
		}
	}
	service = New(roomaudiodiagnostics.Options{ParticipantID: "target", Sink: sink})
	if err := service.Record("alice", roomaudiodiagnostics.Delivered, "reentrant", 1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("sink callback deadlocked on ledger state")
	}
}

func TestFinishedServiceRejectsLateWrites(t *testing.T) {
	service := New(roomaudiodiagnostics.Options{})
	service.Finish()
	if err := service.Admit("alice", roomaudiodiagnostics.Delivered, "late", 1, true); !errors.Is(err, roomaudiodiagnostics.ErrFinished) {
		t.Fatalf("late admit = %v", err)
	}
	if err := service.Record("alice", roomaudiodiagnostics.Delivered, "late", 1); !errors.Is(err, roomaudiodiagnostics.ErrFinished) {
		t.Fatalf("late record = %v", err)
	}
	service.ResolveFrame([]string{"alice"}, 1, "")
	service.RejectPending("late")
}

func serviceSummary(t *testing.T, sink *testSink) roomaudiodiagnostics.Record {
	t.Helper()
	summaries := sink.recordsFor(roomaudiodiagnostics.EventRoomAudioIngressSummary)
	if len(summaries) != 1 {
		t.Fatalf("summary records = %v", summaries)
	}
	return summaries[0]
}
