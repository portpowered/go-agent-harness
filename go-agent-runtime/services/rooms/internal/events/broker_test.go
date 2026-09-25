package events

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

func newTestBroker(t *testing.T, participants []string, options Options) *Broker {
	t.Helper()
	broker, err := New(participants, options)
	if err != nil {
		t.Fatalf("New broker: %v", err)
	}
	t.Cleanup(func() {
		if err := broker.Close(); err != nil {
			t.Errorf("broker.Close(): %v", err)
		}
	})
	return broker
}

func subscribe(t *testing.T, broker *Broker, participant string) *Subscription {
	t.Helper()
	subscription, err := broker.Subscribe(participant)
	if err != nil {
		t.Fatalf("Subscribe(%q): %v", participant, err)
	}
	t.Cleanup(subscription.Close)
	return subscription
}

func nextFrame(t *testing.T, subscription *Subscription) map[string]json.RawMessage {
	t.Helper()
	select {
	case frame, open := <-subscription.Frames():
		if !open {
			t.Fatal("subscription closed before the next event")
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(frame, &payload); err != nil {
			t.Fatalf("decode frame %q: %v", frame, err)
		}
		return payload
	case <-time.After(2 * time.Second):
		t.Fatal("no event before timeout")
		return nil
	}
}

func frameString(t *testing.T, payload map[string]json.RawMessage, field string) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(payload[field], &value); err != nil {
		t.Fatalf("decode %s: %v", field, err)
	}
	return value
}

func TestBrokerProjectsStableEventContractWithoutRawAudio(t *testing.T) {
	const participant = "assistant"
	const stamp = "2026-08-26T23:00:00Z"
	broker := newTestBroker(t, []string{"customer", participant}, Options{
		Now: func() time.Time { return time.Date(2026, 8, 26, 23, 0, 0, 0, time.UTC) },
	})
	all := subscribe(t, broker, "")

	broker.Diagnostic(participant, "session.turn", map[string]string{"turn": "3", "output_audio_bytes": "48200"})
	broker.TranscriptDelta(participant, "so when can I expect")
	broker.TranscriptEnd(participant, "so when can I expect the refund to post?")
	broker.PublishRoomEvent(rooms.RoomStreamEventRunTerminated, rooms.RoomStreamParticipantID, "stopped")

	diagnostic := nextFrame(t, all)
	var fields map[string]string
	if err := json.Unmarshal(diagnostic["fields"], &fields); err != nil {
		t.Fatalf("diagnostic fields: %v", err)
	}
	if frameString(t, diagnostic, "type") != rooms.RoomStreamTypeDiagnostic || frameString(t, diagnostic, "participant_id") != participant ||
		frameString(t, diagnostic, "event") != "session.turn" || frameString(t, diagnostic, "ts") != stamp || fields["turn"] != "3" {
		t.Fatalf("diagnostic = %v fields=%v", diagnostic, fields)
	}
	delta := nextFrame(t, all)
	if frameString(t, delta, "type") != rooms.RoomStreamTypeTranscriptDelta || frameString(t, delta, "text") != "so when can I expect" {
		t.Fatalf("transcript delta = %v", delta)
	}
	if _, containsAudio := delta["content"]; containsAudio {
		t.Fatalf("transcript delta exposed raw audio: %v", delta)
	}
	end := nextFrame(t, all)
	if frameString(t, end, "type") != rooms.RoomStreamTypeTranscriptEnd || frameString(t, end, "full_text") != "so when can I expect the refund to post?" {
		t.Fatalf("transcript end = %v", end)
	}
	room := nextFrame(t, all)
	if frameString(t, room, "type") != rooms.RoomStreamTypeRoom || frameString(t, room, "event") != rooms.RoomStreamEventRunTerminated ||
		frameString(t, room, "participant_id") != rooms.RoomStreamParticipantID || frameString(t, room, "reason") != "stopped" {
		t.Fatalf("room event = %v", room)
	}
}

func TestBrokerBroadcastsLivenessFaultToFilteredPeer(t *testing.T) {
	broker := newTestBroker(t, []string{"silent", "peer"}, Options{})
	all := subscribe(t, broker, "")
	peer := subscribe(t, broker, "peer")

	broker.PublishRoomEvent(rooms.RoomStreamEventParticipantLivenessFault, "silent", "silent_provider_timeout")
	for name, payload := range map[string]map[string]json.RawMessage{"unfiltered": nextFrame(t, all), "peer-filtered": nextFrame(t, peer)} {
		if frameString(t, payload, "event") != rooms.RoomStreamEventParticipantLivenessFault || frameString(t, payload, "participant_id") != "silent" || frameString(t, payload, "reason") != "silent_provider_timeout" {
			t.Fatalf("%s liveness event = %v", name, payload)
		}
	}
}

func TestBrokerRejectsUnknownFilterAndClosedSubscriptions(t *testing.T) {
	broker := newTestBroker(t, []string{"a", "b"}, Options{})
	if _, err := broker.Subscribe("missing"); !errors.Is(err, rooms.ErrUnknownRoomStreamParticipant) || !strings.Contains(err.Error(), `unknown room stream participant: "missing"`) {
		t.Fatalf("unknown participant error = %v", err)
	}
	if err := broker.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := broker.Subscribe("a"); !errors.Is(err, rooms.ErrRoomEventStreamClosed) {
		t.Fatalf("closed subscribe error = %v", err)
	}
}

func TestBrokerIsForwardOnlyAndFiltersParticipants(t *testing.T) {
	broker := newTestBroker(t, []string{"a", "b"}, Options{})
	broker.Diagnostic("a", "before_connect", map[string]string{"sequence": "0"})
	onlyA := subscribe(t, broker, "a")
	broker.Diagnostic("b", "wrong_participant", nil)
	broker.Diagnostic("a", "after_connect", map[string]string{"sequence": "1"})
	if payload := nextFrame(t, onlyA); frameString(t, payload, "event") != "after_connect" {
		t.Fatalf("replayed or unfiltered event payload = %v", payload)
	}
}

func TestBrokerRejectsReservedAndDuplicateParticipants(t *testing.T) {
	for _, participants := range [][]string{{rooms.RoomStreamParticipantID, "participant"}, {"a", "a"}, {" "}} {
		if _, err := New(participants, Options{}); !errors.Is(err, rooms.ErrInvalidRoomStreamParticipant) {
			t.Fatalf("New(%q) error = %v", participants, err)
		}
	}
}

func TestBrokerPublishesRoomLifecycleEventsInOrder(t *testing.T) {
	broker := newTestBroker(t, []string{"alice", "bob"}, Options{})
	all := subscribe(t, broker, "")

	broker.PublishRoomEvent(rooms.RoomStreamEventParticipantJoined, "alice", "")
	broker.PublishRoomEvent(rooms.RoomStreamEventParticipantReady, "alice", "")
	broker.Diagnostic("alice", "session.turn", map[string]string{"turn": "1"})
	broker.PublishRoomEvent(rooms.RoomStreamEventParticipantTerminated, "alice", "ended")
	broker.PublishRoomEvent(rooms.RoomStreamEventRunTerminated, "", "max_turns_reached")

	want := []struct{ event, participant, reason string }{
		{rooms.RoomStreamEventParticipantJoined, "alice", ""},
		{rooms.RoomStreamEventParticipantReady, "alice", ""},
		{"session.turn", "alice", ""},
		{rooms.RoomStreamEventParticipantTerminated, "alice", "ended"},
		{rooms.RoomStreamEventRunTerminated, rooms.RoomStreamParticipantID, "max_turns_reached"},
	}
	for index, expected := range want {
		payload := nextFrame(t, all)
		if frameString(t, payload, "event") != expected.event || frameString(t, payload, "participant_id") != expected.participant {
			t.Fatalf("event %d = %v, want %+v", index, payload, expected)
		}
		if expected.reason != "" && frameString(t, payload, "reason") != expected.reason {
			t.Fatalf("event %d reason = %v, want %q", index, payload, expected.reason)
		}
	}
}

func TestBrokerRedactsSecretsFromEveryProjectedField(t *testing.T) {
	const secret = "sk-room-secret-123"
	broker := newTestBroker(t, []string{"alice"}, Options{Redact: NewRedactor([]string{"", secret}).Redact})
	all := subscribe(t, broker, "")
	fields := map[string]string{"error": "auth " + secret + " rejected"}

	broker.Diagnostic("alice", "failure "+secret, fields)
	broker.TranscriptDelta("alice", "my key is "+secret)
	broker.TranscriptEnd("alice", "full "+secret)
	broker.PublishRoomEvent(rooms.RoomStreamEventParticipantFailed, "alice", "dial "+secret)

	for index := 0; index < 4; index++ {
		select {
		case frame := <-all.Frames():
			if strings.Contains(string(frame), secret) || !strings.Contains(string(frame), RedactedMarker) {
				t.Fatalf("frame %d = %s, want the secret redacted", index, frame)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("frame %d not delivered", index)
		}
	}
	if fields["error"] != "auth "+secret+" rejected" {
		t.Fatalf("publisher fields were mutated: %v", fields)
	}
}
