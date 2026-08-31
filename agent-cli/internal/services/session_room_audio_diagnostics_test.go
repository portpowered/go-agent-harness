package services

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestRunRoom_ProviderInputRejectionPreservesPeerAttributionAndArtifact(t *testing.T) {
	const (
		sourceID = "alice"
		targetID = "bob"
	)
	pcm := roomPCM16(1200, 10)
	releaseAudio := make(chan struct{})
	inferencers := map[string]*roomTestInferencer{
		sourceID: {events: []messages.StreamMessage{
			roomTestSessionOpen(sourceID),
			roomTestMessageStart(),
			roomTestAudioEvent(1200, 10),
		}, eventWait: func(index int) <-chan struct{} {
			if index == 2 {
				return releaseAudio
			}
			return nil
		}},
		targetID: {events: []messages.StreamMessage{
			roomTestSessionOpen(targetID),
			roomTestMessageStart(),
		}},
	}
	options, _ := newRoomTestRunOptions([]string{sourceID, targetID}, inferencers)
	options.OutputDir = t.TempDir()
	sink := &diagnosticRecordSink{}
	options.OnDiagnostic = func(_ string, record SessionDiagnosticRecord) {
		sink.RecordSessionDiagnostic(record)
	}
	started := make(chan string, 2)
	options.onParticipantStream = func(participantID string, msg messages.StreamMessage) {
		if msg.Type == messages.StreamTypeMessageStart {
			started <- participantID
		}
	}
	rejected := make(chan struct{})
	var rejectOnce sync.Once
	rejectionErr := errors.New("provider input rejected")
	options.onParticipantAudioInput = func(participantID string, got []byte) error {
		if participantID == targetID && bytes.Equal(got, pcm) {
			rejectOnce.Do(func() { close(rejected) })
			return rejectionErr
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	outcome := make(chan roomTestRunOutcome, 1)
	go func() {
		result, err := RunRoomWithResult(ctx, io.Discard, options)
		outcome <- roomTestRunOutcome{result: result, err: err}
	}()
	seenStarted := make(map[string]struct{}, 2)
	for len(seenStarted) < 2 {
		select {
		case participantID := <-started:
			seenStarted[participantID] = struct{}{}
		case <-time.After(2 * time.Second):
			t.Fatalf("response-start observations = %v, want both participants", seenStarted)
		}
	}
	close(releaseAudio)
	select {
	case <-rejected:
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatal("provider-input rejection was not observed")
	}
	var got roomTestRunOutcome
	select {
	case got = <-outcome:
	case <-time.After(2 * time.Second):
		t.Fatal("room did not finish after provider-input rejection")
	}
	if got.err != nil {
		t.Fatalf("room cancellation after provider-input rejection: %v", got.err)
	}
	if got.result.Reason != RoomTerminationStopped {
		t.Fatalf("room reason = %q, want %q", got.result.Reason, RoomTerminationStopped)
	}

	receivedPath := filepath.Join(options.OutputDir, "participants", targetID, "received.pcm")
	received, err := os.ReadFile(receivedPath)
	if err != nil {
		t.Fatalf("read rejected participant received.pcm: %v", err)
	}
	if len(received)%len(pcm) != 0 {
		t.Fatalf("rejected participant received.pcm has partial frame: %d bytes", len(received))
	}
	if bytes.Contains(received, pcm) {
		t.Fatalf("rejected participant received.pcm contains rejected provider frame: %v", received)
	}
	for _, value := range received {
		if value != 0 {
			t.Fatalf("rejected participant received.pcm contains non-silent provider-bound bytes: %v", received)
		}
	}

	var rejection, summary *SessionDiagnosticRecord
	for _, record := range sink.all() {
		if record.Fields[SessionDiagnosticFieldParticipantID] != targetID {
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
	if rejection == nil || summary == nil {
		t.Fatalf("provider-input rejection diagnostics missing: %v", sink.all())
	}
	if got := rejection.Fields[SessionDiagnosticFieldSourcePeer]; got != sourceID {
		t.Fatalf("rejection source_peer=%q, want %q", got, sourceID)
	}
	if got := rejection.Fields[SessionDiagnosticFieldDisposition]; got != string(RoomAudioIngressRejected) {
		t.Fatalf("rejection disposition=%q, want rejected", got)
	}
	if got := rejection.Fields[SessionDiagnosticFieldReason]; got != roomAudioIngressReasonProviderInputRejected {
		t.Fatalf("rejection reason=%q, want %q", got, roomAudioIngressReasonProviderInputRejected)
	}
	if got := rejection.Fields[SessionDiagnosticFieldByteCount]; got != "20" {
		t.Fatalf("rejection byte_count=%q, want 20", got)
	}
	if got := rejection.Fields[SessionDiagnosticFieldFrameCount]; got != "1" {
		t.Fatalf("rejection frame_count=%q, want 1", got)
	}
	if got := summary.Fields[SessionDiagnosticFieldSourcePeers]; got != sourceID {
		t.Fatalf("summary source_peers=%q, want %q", got, sourceID)
	}
	if got := summary.Fields[SessionDiagnosticFieldContentfulBytes]; got != "20" {
		t.Fatalf("summary contentful_bytes=%q, want 20", got)
	}
	if got := summary.Fields[SessionDiagnosticFieldRejectedBytes]; got != "20" {
		t.Fatalf("summary rejected_bytes=%q, want 20", got)
	}
	if got := summary.Fields[SessionDiagnosticFieldRejectedFrames]; got != "1" {
		t.Fatalf("summary rejected_frames=%q, want 1", got)
	}
	if got := summary.Fields[SessionDiagnosticFieldDeliveredBytes]; got != "0" {
		t.Fatalf("summary delivered_bytes=%q, want 0", got)
	}
	if got := summary.Fields[SessionDiagnosticFieldContentLoss]; got != "true" {
		t.Fatalf("summary content_loss=%q, want true", got)
	}
}

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
	bobResult, bobPresent := result.Participants["bob"]
	if runErr != nil || result.Reason != RoomTerminationStopped || !bobPresent || bobResult.TerminationReason != ParticipantTerminationError || bobResult.Error == "" {
		t.Fatalf("closed-target room result=%+v err=%v, want participant-scoped rejection with a clean room stop", result, runErr)
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
