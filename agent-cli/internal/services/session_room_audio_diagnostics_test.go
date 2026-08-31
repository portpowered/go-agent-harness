package services

import (
	"context"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestRunRoom_ReportsClosedTargetAsRejectedPeerIngress(t *testing.T) {
	inferencers := map[string]*roomTestInferencer{
		"alice": {events: []messages.StreamMessage{
			roomTestSessionOpen("alice"),
			roomTestMessageStart(),
			roomTestAudioEvent(1200, 10),
		}},
		"bob": {events: []messages.StreamMessage{
			roomTestSessionOpen("bob"),
		}},
	}
	options, _ := newRoomTestRunOptions([]string{"alice", "bob"}, inferencers)
	options.onParticipantMixerReady = func(participantID string, mixer *room.PCM16Mixer) {
		if participantID == "bob" {
			if err := mixer.Close(); err != nil {
				t.Errorf("close target mixer: %v", err)
			}
		}
	}
	sink := &diagnosticRecordSink{}
	options.OnDiagnostic = func(_ string, record SessionDiagnosticRecord) {
		sink.RecordSessionDiagnostic(record)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, runErr := RunRoomWithResult(ctx, io.Discard, options)
	if runErr == nil || result.Reason != RoomTerminationFailed {
		t.Fatalf("closed-target room result=%+v err=%v, want failed rejection", result, runErr)
	}

	var rejection, summary *SessionDiagnosticRecord
	for _, record := range sink.all() {
		if record.Fields[SessionDiagnosticFieldParticipantID] != "bob" {
			continue
		}
		switch record.Event {
		case SessionDiagnosticEventRoomAudioIngress:
			copy := record
			rejection = &copy
		case SessionDiagnosticEventRoomAudioIngressSummary:
			copy := record
			summary = &copy
		}
	}
	if rejection == nil {
		t.Fatalf("missing bob rejection diagnostic: %v", sink.all())
	}
	if got := rejection.Fields[SessionDiagnosticFieldRoomID]; got != RoomStreamRoomParticipantID {
		t.Fatalf("rejection room_id=%q, want %q", got, RoomStreamRoomParticipantID)
	}
	if got := rejection.Fields[SessionDiagnosticFieldSourcePeer]; got != "alice" {
		t.Fatalf("rejection source_peer=%q, want alice", got)
	}
	if got := rejection.Fields[SessionDiagnosticFieldDisposition]; got != string(RoomAudioIngressRejected) {
		t.Fatalf("rejection disposition=%q, want rejected", got)
	}
	if got := rejection.Fields[SessionDiagnosticFieldReason]; got != "mixer_closed" {
		t.Fatalf("rejection reason=%q, want mixer_closed; records=%v", got, sink.all())
	}
	if got := rejection.Fields[SessionDiagnosticFieldByteCount]; got != "20" {
		t.Fatalf("rejection byte_count=%q, want 20", got)
	}
	if got := rejection.Fields[SessionDiagnosticFieldFrameCount]; got != "1" {
		t.Fatalf("rejection frame_count=%q, want 1", got)
	}
	if summary == nil {
		t.Fatalf("missing bob ingress summary: %v", sink.all())
	}
	if got := summary.Fields[SessionDiagnosticFieldRejectedBytes]; got != "20" {
		t.Fatalf("summary rejected_bytes=%q, want 20", got)
	}
	if got := summary.Fields[SessionDiagnosticFieldRejectedFrames]; got != "1" {
		t.Fatalf("summary rejected_frames=%q, want 1", got)
	}
	if got := summary.Fields[SessionDiagnosticFieldContentLoss]; got != "true" {
		t.Fatalf("summary content_loss=%q, want true", got)
	}
	if got := summary.Fields[SessionDiagnosticFieldContentfulBytes]; got != "20" {
		t.Fatalf("summary contentful_bytes=%q, want 20", got)
	}
	if got := summary.Fields[SessionDiagnosticFieldSourcePeers]; got != "alice" {
		t.Fatalf("summary source_peers=%q, want alice", got)
	}
	if _, hasPCMField := rejection.Fields["pcm"]; hasPCMField {
		t.Fatalf("rejection diagnostic unexpectedly contains a raw PCM field: %v", rejection.Fields)
	}
}

func TestRoomAudioIngressLedgerEmitsBoundedCumulativeSummary(t *testing.T) {
	sink := &diagnosticRecordSink{}
	ledger := newRoomAudioIngressLedger("bob", sink)
	for range 100 {
		ledger.record("alice", RoomAudioIngressRejected, "mixer_closed", 20)
	}
	ledger.finish()
	ledger.finish()

	records := sink.events(SessionDiagnosticEventRoomAudioIngress)
	if len(records) != 1 {
		t.Fatalf("first ingress records=%d, want one bounded record", len(records))
	}
	summaries := sink.events(SessionDiagnosticEventRoomAudioIngressSummary)
	if len(summaries) != 1 {
		t.Fatalf("summary records=%d, want one", len(summaries))
	}
	if got := summaries[0].Fields[SessionDiagnosticFieldRejectedBytes]; got != strconv.Itoa(100*20) {
		t.Fatalf("summary rejected_bytes=%q, want %d", got, 100*20)
	}
	if got := summaries[0].Fields[SessionDiagnosticFieldRejectedFrames]; got != "100" {
		t.Fatalf("summary rejected_frames=%q, want 100", got)
	}
}

func TestRoomAudioIngressLedgerDistinguishesNoPeerAudioFromContentLoss(t *testing.T) {
	sink := &diagnosticRecordSink{}
	ledger := newRoomAudioIngressLedger("bob", sink)
	ledger.finish()

	summaries := sink.events(SessionDiagnosticEventRoomAudioIngressSummary)
	if len(summaries) != 1 {
		t.Fatalf("summary records=%d, want one", len(summaries))
	}
	fields := summaries[0].Fields
	if fields[SessionDiagnosticFieldReason] != roomAudioIngressReasonNoContentfulPeerAudio {
		t.Fatalf("no-peer reason=%q, want %q", fields[SessionDiagnosticFieldReason], roomAudioIngressReasonNoContentfulPeerAudio)
	}
	if fields[SessionDiagnosticFieldContentfulFrames] != "0" || fields[SessionDiagnosticFieldRejectedFrames] != "0" {
		t.Fatalf("no-peer counts=%v, want zero content and rejection", fields)
	}
	if fields[SessionDiagnosticFieldContentLoss] != "false" {
		t.Fatalf("no-peer content_loss=%q, want false", fields[SessionDiagnosticFieldContentLoss])
	}
}
