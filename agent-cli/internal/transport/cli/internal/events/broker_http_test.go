package events

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type sseReader struct{ scanner *bufio.Scanner }

func newSSEReader(body io.Reader) *sseReader { return &sseReader{scanner: bufio.NewScanner(body)} }

func (r *sseReader) next(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	for r.scanner.Scan() {
		line := r.scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload); err != nil {
			t.Fatalf("decode SSE payload %q: %v", line, err)
		}
		return payload
	}
	if err := r.scanner.Err(); err != nil {
		t.Fatalf("read SSE payload: %v", err)
	}
	t.Fatal("SSE stream ended before the next event")
	return nil
}

func sseString(t *testing.T, payload map[string]json.RawMessage, field string) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(payload[field], &value); err != nil {
		t.Fatalf("decode SSE %s: %v", field, err)
	}
	return value
}

func openSSE(t *testing.T, server *httptest.Server, path string) (*http.Response, *sseReader) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatalf("create SSE request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	if response.StatusCode != http.StatusOK {
		if err := response.Body.Close(); err != nil {
			t.Fatalf("close unsuccessful SSE response: %v", err)
		}
		t.Fatalf("GET %s status = %d, want %d", path, response.StatusCode, http.StatusOK)
	}
	return response, newSSEReader(response.Body)
}

func assertSSEContractHeaders(t *testing.T, response *http.Response) {
	t.Helper()
	if got := response.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content type = %q, want text/event-stream", got)
	}
	if got := response.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("access-control-allow-origin = %q, want wildcard", got)
	}
}

func assertDiagnosticEvent(t *testing.T, payload map[string]json.RawMessage, participant, stamp string) {
	t.Helper()
	if sseString(t, payload, "type") != EventDiagnostic || sseString(t, payload, "participant_id") != participant {
		t.Fatalf("diagnostic envelope = %v", payload)
	}
	var fields map[string]string
	if err := json.Unmarshal(payload["fields"], &fields); err != nil {
		t.Fatalf("diagnostic fields: %v", err)
	}
	if fields["turn"] != "3" || sseString(t, payload, "event") != "session.turn" || sseString(t, payload, "ts") != stamp {
		t.Fatalf("diagnostic payload = %v fields=%v", payload, fields)
	}
}

func assertTranscriptDelta(t *testing.T, payload map[string]json.RawMessage) {
	t.Helper()
	if sseString(t, payload, "type") != EventTranscriptDelta || sseString(t, payload, "text") != "so when can I expect" {
		t.Fatalf("transcript delta = %v", payload)
	}
	if _, containsAudio := payload["content"]; containsAudio {
		t.Fatalf("transcript delta exposed raw audio: %v", payload)
	}
}

func assertTranscriptEnd(t *testing.T, payload map[string]json.RawMessage) {
	t.Helper()
	if sseString(t, payload, "type") != EventTranscriptEnd || sseString(t, payload, "full_text") != "so when can I expect the refund to post?" {
		t.Fatalf("transcript end = %v", payload)
	}
}

func assertRoomTermination(t *testing.T, payload map[string]json.RawMessage) {
	t.Helper()
	if sseString(t, payload, "type") != EventRoom || sseString(t, payload, "event") != EventRunTerminated || sseString(t, payload, "participant_id") != RoomParticipantID || sseString(t, payload, "reason") != "stopped" {
		t.Fatalf("room event = %v", payload)
	}
}

func assertRoomLifecycleEvent(t *testing.T, payload map[string]json.RawMessage, index int, expected struct {
	event       string
	participant string
	reason      string
}) {
	t.Helper()
	if expected.event == "session.turn" {
		if got := sseString(t, payload, "type"); got != EventDiagnostic {
			t.Fatalf("event %d type = %q, want diagnostic", index, got)
		}
	} else if got := sseString(t, payload, "event"); got != expected.event {
		t.Fatalf("event %d name = %q, want %q", index, got, expected.event)
	}
	if got := sseString(t, payload, "participant_id"); got != expected.participant {
		t.Fatalf("event %d participant = %q, want %q", index, got, expected.participant)
	}
	if expected.reason != "" {
		if got := sseString(t, payload, "reason"); got != expected.reason {
			t.Fatalf("event %d reason = %q, want %q", index, got, expected.reason)
		}
	}
}

func TestBrokerServesContractAndFiltersRawAudio(t *testing.T) {
	const participant = "assistant"
	const stamp = "2026-08-26T23:00:00Z"
	broker, err := New([]string{"customer", participant}, Options{
		Now: func() time.Time { return time.Date(2026, 8, 26, 23, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("New broker: %v", err)
	}
	server := httptest.NewServer(broker)
	defer server.Close()
	t.Cleanup(func() {
		if err := broker.Close(); err != nil {
			t.Errorf("broker.Close(): %v", err)
		}
	})
	response, reader := openSSE(t, server, "/events")
	t.Cleanup(func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("SSE response Close(): %v", err)
		}
	})
	assertSSEContractHeaders(t, response)

	broker.RecordDiagnostic(participant, "session.turn", map[string]string{"turn": "3", "output_audio_bytes": "48200"})
	broker.PublishTranscriptDelta(participant, "so when can I expect")
	broker.PublishTranscriptEnd(participant, "so when can I expect the refund to post?")
	broker.PublishRoomEvent(EventRunTerminated, RoomParticipantID, "stopped")

	assertDiagnosticEvent(t, reader.next(t), participant, stamp)
	assertTranscriptDelta(t, reader.next(t))
	assertTranscriptEnd(t, reader.next(t))
	assertRoomTermination(t, reader.next(t))
}

func TestBrokerBroadcastsLivenessFaultToFilteredPeer(t *testing.T) {
	broker, err := New([]string{"silent", "peer"}, Options{})
	if err != nil {
		t.Fatalf("New broker: %v", err)
	}
	t.Cleanup(func() {
		if err := broker.Close(); err != nil {
			t.Errorf("broker.Close(): %v", err)
		}
	})
	server := httptest.NewServer(broker)
	defer server.Close()
	allResponse, allReader := openSSE(t, server, "/events")
	t.Cleanup(func() {
		if err := allResponse.Body.Close(); err != nil {
			t.Errorf("all-events response Close(): %v", err)
		}
	})
	peerResponse, peerReader := openSSE(t, server, "/events?participant=peer")
	t.Cleanup(func() {
		if err := peerResponse.Body.Close(); err != nil {
			t.Errorf("peer-events response Close(): %v", err)
		}
	})

	broker.PublishRoomEvent(EventParticipantLivenessFault, "silent", "silent_provider_timeout")
	for name, payload := range map[string]map[string]json.RawMessage{
		"unfiltered":    allReader.next(t),
		"peer-filtered": peerReader.next(t),
	} {
		if sseString(t, payload, "type") != EventRoom || sseString(t, payload, "event") != EventParticipantLivenessFault || sseString(t, payload, "participant_id") != "silent" || sseString(t, payload, "reason") != "silent_provider_timeout" {
			t.Fatalf("%s liveness event = %v", name, payload)
		}
	}
}

func TestBrokerRejectsUnknownFilterBeforeStreaming(t *testing.T) {
	broker, err := New([]string{"a", "b"}, Options{})
	if err != nil {
		t.Fatalf("New broker: %v", err)
	}
	t.Cleanup(func() {
		if err := broker.Close(); err != nil {
			t.Errorf("broker.Close(): %v", err)
		}
	})
	server := httptest.NewServer(broker)
	defer server.Close()
	response, err := http.Get(server.URL + "/events?participant=missing")
	if err != nil {
		t.Fatalf("GET unknown participant: %v", err)
	}
	t.Cleanup(func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("unknown-filter response Close(): %v", err)
		}
	})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown participant status = %d, want %d", response.StatusCode, http.StatusBadRequest)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read unknown participant response: %v", err)
	}
	if !strings.Contains(string(body), `unknown room stream participant: "missing"`) {
		t.Fatalf("unknown participant response = %q", body)
	}
}

func TestBrokerIsForwardOnly(t *testing.T) {
	broker, err := New([]string{"a", "b"}, Options{})
	if err != nil {
		t.Fatalf("New broker: %v", err)
	}
	t.Cleanup(func() {
		if err := broker.Close(); err != nil {
			t.Errorf("broker.Close(): %v", err)
		}
	})
	server := httptest.NewServer(broker)
	defer server.Close()
	broker.RecordDiagnostic("a", "before_connect", map[string]string{"sequence": "0"})
	response, reader := openSSE(t, server, "/events?participant=a")
	t.Cleanup(func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("forward-only response Close(): %v", err)
		}
	})
	broker.RecordDiagnostic("b", "wrong_participant", nil)
	broker.RecordDiagnostic("a", "after_connect", map[string]string{"sequence": "1"})
	if payload := reader.next(t); sseString(t, payload, "event") != "after_connect" {
		t.Fatalf("replayed event payload = %v", payload)
	}
}

func TestBrokerRejectsReservedRoomParticipant(t *testing.T) {
	if _, err := New([]string{RoomParticipantID, "participant"}, Options{}); err == nil || !strings.Contains(err.Error(), RoomParticipantID) {
		t.Fatalf("reserved participant error = %v", err)
	}
}

func TestBrokerPublishesRoomLifecycleEventsInOrder(t *testing.T) {
	broker, err := New([]string{"alice", "bob"}, Options{})
	if err != nil {
		t.Fatalf("New broker: %v", err)
	}
	t.Cleanup(func() {
		if err := broker.Close(); err != nil {
			t.Errorf("broker.Close(): %v", err)
		}
	})
	server := httptest.NewServer(broker)
	defer server.Close()
	response, reader := openSSE(t, server, "/events")
	t.Cleanup(func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("lifecycle response Close(): %v", err)
		}
	})

	broker.PublishRoomEvent(EventParticipantJoined, "alice")
	broker.PublishRoomEvent(EventParticipantReady, "alice")
	broker.RecordDiagnostic("alice", "session.turn", map[string]string{"turn": "1"})
	broker.PublishRoomEvent(EventParticipantTerminated, "alice", "ended")
	broker.PublishRoomEvent(EventRunTerminated, RoomParticipantID, "max_turns_reached")

	want := []struct {
		event       string
		participant string
		reason      string
	}{
		{event: EventParticipantJoined, participant: "alice"},
		{event: EventParticipantReady, participant: "alice"},
		{event: "session.turn", participant: "alice"},
		{event: EventParticipantTerminated, participant: "alice", reason: "ended"},
		{event: EventRunTerminated, participant: RoomParticipantID, reason: "max_turns_reached"},
	}
	for index, expected := range want {
		assertRoomLifecycleEvent(t, reader.next(t), index, expected)
	}
}
