package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestProviderCaptureSpoolMatchesCanonicalCapture(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "provider.json")
	sink, err := NewProviderCapture(destination)
	if err != nil {
		t.Fatal(err)
	}
	events := providerSpoolEvents()
	for _, event := range events {
		if err := sink.Append(event); err != nil {
			t.Fatalf("Append(%d): %v", event.Sequence, err)
		}
		if err := sink.Commit(event.Sequence); err != nil {
			t.Fatalf("Commit(%d): %v", event.Sequence, err)
		}
	}
	capture := gatewaytesting.SessionCapture{
		Version:  gatewaytesting.SessionCaptureVersion,
		Provider: gatewaytesting.SessionProviderMetadata{Name: "provider", Model: "model"},
		Session:  gatewaytesting.SessionMetadata{ID: "session-1", StartedAtUTC: "2026-09-05T12:00:00Z"},
		Records:  events,
	}
	if err := sink.FlushToFile(destination, capture); err != nil {
		t.Fatal(err)
	}
	loaded, err := gatewaytesting.LoadSessionCapture(destination)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := gatewaytesting.SealSessionCapture(capture)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Integrity != sealed.Integrity || !reflect.DeepEqual(loaded.Records, events) {
		t.Fatalf("loaded capture = %#v, want %#v", loaded, sealed)
	}
	if info, err := os.Stat(destination); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("capture mode = %v, want 0600", err)
	}
}

func TestProviderCaptureSpoolDiscardsFailedReservationWithoutRetainingTombstone(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "provider.json")
	sink, err := NewProviderCapture(destination)
	if err != nil {
		t.Fatal(err)
	}
	events := providerSpoolEvents()
	if err := sink.Append(events[0]); err != nil {
		t.Fatal(err)
	}
	if err := sink.Discard(events[0].Sequence); err != nil {
		t.Fatal(err)
	}
	if err := sink.Append(events[1]); err != nil {
		t.Fatal(err)
	}
	if err := sink.Commit(events[1].Sequence); err != nil {
		t.Fatal(err)
	}
	capture := gatewaytesting.SessionCapture{Version: gatewaytesting.SessionCaptureVersion}
	if err := sink.FlushToFile(destination, capture); err != nil {
		t.Fatal(err)
	}
	loaded, err := gatewaytesting.LoadSessionCapture(destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Records) != 1 || loaded.Records[0].Sequence != events[1].Sequence {
		t.Fatalf("records = %#v, want only sequence %d", loaded.Records, events[1].Sequence)
	}
}

func TestProviderCaptureSpoolRejectsUnsettledOrOversizeEvents(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "provider.json")
	sink, err := NewProviderCapture(destination)
	if err != nil {
		t.Fatal(err)
	}
	event := providerSpoolEvents()[0]
	if err := sink.Append(event); err != nil {
		t.Fatal(err)
	}
	if err := sink.FlushToFile(destination, gatewaytesting.SessionCapture{Version: gatewaytesting.SessionCaptureVersion}); !errors.Is(err, errProviderCaptureUnresolved) {
		t.Fatalf("unsettled flush error = %v, want unresolved reservation", err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsettled capture destination = %v, want absent", err)
	}

	oversizeDestination := filepath.Join(t.TempDir(), "oversize.json")
	oversize, err := NewProviderCapture(oversizeDestination)
	if err != nil {
		t.Fatal(err)
	}
	event.Payload = json.RawMessage(`{"payload":"` + string(make([]byte, providerCaptureMaxEventBytes)) + `"}`)
	if err := oversize.Append(event); !errors.Is(err, errProviderCaptureEventTooLarge) {
		t.Fatalf("oversize append error = %v, want event-too-large", err)
	}
	if err := oversize.Abort(); err != nil {
		t.Fatal(err)
	}
}

func TestProviderCaptureSpoolRejectsContradictorySettlement(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "provider.json")
	sink, err := NewProviderCapture(destination)
	if err != nil {
		t.Fatal(err)
	}
	event := providerSpoolEvents()[0]
	if err := sink.Append(event); err != nil {
		t.Fatal(err)
	}
	if err := sink.Commit(event.Sequence); err != nil {
		t.Fatal(err)
	}
	if err := sink.Discard(event.Sequence); err != nil {
		t.Fatal(err)
	}
	if err := sink.FlushToFile(destination, gatewaytesting.SessionCapture{Version: gatewaytesting.SessionCaptureVersion}); !errors.Is(err, errProviderCaptureUnresolved) {
		t.Fatalf("contradictory settlement error = %v, want unresolved reservation", err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("contradictory capture destination = %v, want absent", err)
	}
}

func TestProviderCaptureSpoolCopiesPayloadAndAbortRemovesTemporaryState(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "provider.json")
	sink, err := NewProviderCapture(destination)
	if err != nil {
		t.Fatal(err)
	}
	event := providerSpoolEvents()[0]
	original := append([]byte(nil), event.Payload...)
	if err := sink.Append(event); err != nil {
		t.Fatal(err)
	}
	event.Payload[0] = 'x'
	if err := sink.Commit(event.Sequence); err != nil {
		t.Fatal(err)
	}
	if err := sink.FlushToFile(destination, gatewaytesting.SessionCapture{Version: gatewaytesting.SessionCaptureVersion}); err != nil {
		t.Fatal(err)
	}
	loaded, err := gatewaytesting.LoadSessionCapture(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loaded.Records[0].Payload, original) {
		t.Fatalf("payload = %q, want %q", loaded.Records[0].Payload, original)
	}

	abortDestination := filepath.Join(t.TempDir(), "aborted.json")
	aborted, err := NewProviderCapture(abortDestination)
	if err != nil {
		t.Fatal(err)
	}
	if err := aborted.Abort(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(abortDestination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("aborted destination = %v, want absent", err)
	}
}

func TestProviderCaptureSpoolBoundsActiveReservationsWhenEarliestWriteBlocks(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "provider.json")
	sink, err := NewProviderCapture(destination)
	if err != nil {
		t.Fatal(err)
	}
	spool, ok := sink.(*providerCaptureSpool)
	if !ok {
		t.Fatalf("sink type = %T, want providerCaptureSpool", sink)
	}
	oldFile := spool.file
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := oldFile.Close(); err != nil {
		t.Fatal(err)
	}
	spool.file = writeEnd
	largePayload := json.RawMessage(`{"data":"` + strings.Repeat("x", providerCaptureMaxEventBytes-512) + `"}`)
	first := gatewaytesting.CapturedSessionEvent{
		Sequence: 1, Direction: gatewaytesting.DirectionClientToServer,
		Type: "large", PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage, Payload: largePayload,
	}
	if err := sink.Append(first); err != nil {
		t.Fatal(err)
	}
	if err := sink.Commit(first.Sequence); err != nil {
		t.Fatal(err)
	}
	var admissionErr error
	for sequence := 2; sequence < providerCaptureQueueMaxItems*2; sequence++ {
		event := gatewaytesting.CapturedSessionEvent{
			Sequence: sequence, Direction: gatewaytesting.DirectionClientToServer,
			Type: "small", PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage, Payload: json.RawMessage(`{"type":"small"}`),
		}
		admissionErr = sink.Append(event)
		if admissionErr != nil {
			break
		}
		admissionErr = sink.Commit(sequence)
		if admissionErr != nil {
			break
		}
	}
	if admissionErr == nil {
		t.Fatal("unbounded reservations were accepted while the earliest spool write was blocked")
	}
	spool.mu.Lock()
	items, bytes := spool.queuedItems, spool.queuedBytes
	spool.mu.Unlock()
	if items > providerCaptureQueueMaxItems || bytes > providerCaptureQueueMaxBytes {
		t.Fatalf("active reservation budget = items %d bytes %d, want <= %d/%d", items, bytes, providerCaptureQueueMaxItems, providerCaptureQueueMaxBytes)
	}
	if err := readEnd.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sink.Abort(); err != nil {
		t.Fatal(err)
	}
}

func providerSpoolEvents() []gatewaytesting.CapturedSessionEvent {
	return []gatewaytesting.CapturedSessionEvent{
		{Sequence: 1, Direction: gatewaytesting.DirectionClientToServer, Type: "session.update", PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage, Payload: json.RawMessage(`{"type":"session.update","nested":{"line":"a\n\"b"}}`)},
		{Sequence: 2, Direction: gatewaytesting.DirectionServerToClient, TimestampMs: 12, Type: "session.created", PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage, Payload: json.RawMessage(`{"type":"session.created","value":[1,true]}`)},
	}
}
