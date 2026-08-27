package services

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type roomSSEReader struct {
	scanner *bufio.Scanner
}

func newRoomSSEReader(body io.Reader) *roomSSEReader {
	return &roomSSEReader{scanner: bufio.NewScanner(body)}
}

func (r *roomSSEReader) next(t *testing.T) map[string]json.RawMessage {
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

func roomSSEString(t *testing.T, payload map[string]json.RawMessage, field string) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(payload[field], &value); err != nil {
		t.Fatalf("decode SSE %s: %v", field, err)
	}
	return value
}

func TestRoomEventBroker_ServesContractAndFiltersAudio(t *testing.T) {
	const (
		participant = "assistant"
		stamp       = "2026-08-26T23:00:00Z"
	)
	broker, err := NewRoomEventBrokerWithOptions([]string{"customer", participant}, RoomStreamBrokerOptions{
		Now: func() time.Time { return time.Date(2026, 8, 26, 23, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewRoomEventBroker: %v", err)
	}
	server := httptest.NewServer(broker)
	defer server.Close()
	defer broker.Close()

	response, err := http.Get(server.URL + "/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /events status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if got := response.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content type = %q, want text/event-stream", got)
	}
	reader := newRoomSSEReader(response.Body)

	broker.RecordDiagnostic(participant, SessionDiagnosticRecord{
		Event:  SessionDiagnosticEventTurn,
		Fields: map[string]string{fieldTurnIndex: "3", fieldOutputAudioBytes: "48200"},
	})
	broker.ObserveStream(participant, messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Value: messages.NewAudioDeltaValue([]byte{1, 2, 3, 4}),
	})
	broker.ObserveStream(participant, messages.StreamMessage{
		Type:  messages.StreamTypeTranscriptDelta,
		Value: messages.NewTranscriptDeltaValue("so when can I expect"),
	})
	broker.ObserveStream(participant, messages.StreamMessage{
		Type:  messages.StreamTypeTranscriptEnd,
		Value: messages.NewTranscriptEndValue("so when can I expect the refund to post?"),
	})
	broker.PublishRoomEvent(RoomStreamEventRunTerminated, "", string(RoomTerminationStopped))

	diagnostic := reader.next(t)
	if roomSSEString(t, diagnostic, "type") != RoomStreamEventTypeDiagnostic || roomSSEString(t, diagnostic, "participant_id") != participant {
		t.Fatalf("diagnostic envelope = %v", diagnostic)
	}
	var fields map[string]string
	if err := json.Unmarshal(diagnostic["fields"], &fields); err != nil {
		t.Fatalf("diagnostic fields: %v", err)
	}
	if fields[fieldTurnIndex] != "3" || roomSSEString(t, diagnostic, "event") != SessionDiagnosticEventTurn {
		t.Fatalf("diagnostic payload = %v fields=%v", diagnostic, fields)
	}
	if roomSSEString(t, diagnostic, "ts") != stamp {
		t.Fatalf("diagnostic timestamp = %q, want %q", roomSSEString(t, diagnostic, "ts"), stamp)
	}

	delta := reader.next(t)
	if roomSSEString(t, delta, "type") != RoomStreamEventTypeTranscriptDelta || roomSSEString(t, delta, "text") != "so when can I expect" {
		t.Fatalf("transcript delta = %v", delta)
	}
	if _, containsAudio := delta["content"]; containsAudio {
		t.Fatalf("transcript delta exposed raw audio: %v", delta)
	}

	transcriptEnd := reader.next(t)
	if roomSSEString(t, transcriptEnd, "type") != RoomStreamEventTypeTranscriptEnd || roomSSEString(t, transcriptEnd, "full_text") != "so when can I expect the refund to post?" {
		t.Fatalf("transcript end = %v", transcriptEnd)
	}

	roomEvent := reader.next(t)
	if roomSSEString(t, roomEvent, "type") != RoomStreamEventTypeRoom || roomSSEString(t, roomEvent, "event") != RoomStreamEventRunTerminated || roomSSEString(t, roomEvent, "participant_id") != RoomStreamRoomParticipantID || roomSSEString(t, roomEvent, "reason") != string(RoomTerminationStopped) {
		t.Fatalf("room event = %v", roomEvent)
	}
}

func TestRoomEventBroker_UnknownFilterFailsBeforeStreaming(t *testing.T) {
	broker, err := NewRoomEventBroker([]string{"a", "b"})
	if err != nil {
		t.Fatalf("NewRoomEventBroker: %v", err)
	}
	server := httptest.NewServer(broker)
	defer server.Close()
	defer broker.Close()

	response, err := http.Get(server.URL + "/events?participant=missing")
	if err != nil {
		t.Fatalf("GET unknown participant: %v", err)
	}
	defer response.Body.Close()
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

func TestRoomEventBroker_IsForwardOnly(t *testing.T) {
	broker, err := NewRoomEventBroker([]string{"a", "b"})
	if err != nil {
		t.Fatalf("NewRoomEventBroker: %v", err)
	}
	server := httptest.NewServer(broker)
	defer server.Close()
	defer broker.Close()

	broker.RecordDiagnostic("a", SessionDiagnosticRecord{Event: "before_connect", Fields: map[string]string{"sequence": "0"}})
	response, err := http.Get(server.URL + "/events?participant=a")
	if err != nil {
		t.Fatalf("GET filtered events: %v", err)
	}
	defer response.Body.Close()
	reader := newRoomSSEReader(response.Body)
	broker.RecordDiagnostic("b", SessionDiagnosticRecord{Event: "wrong_participant"})
	broker.RecordDiagnostic("a", SessionDiagnosticRecord{Event: "after_connect", Fields: map[string]string{"sequence": "1"}})
	payload := reader.next(t)
	if roomSSEString(t, payload, "event") != "after_connect" {
		t.Fatalf("replayed event payload = %v", payload)
	}
}

type blockingRoomResponseWriter struct {
	header    http.Header
	ready     chan struct{}
	started   chan struct{}
	release   chan struct{}
	readyOnce sync.Once
	startOnce sync.Once
}

func newBlockingRoomResponseWriter() *blockingRoomResponseWriter {
	return &blockingRoomResponseWriter{
		header:  make(http.Header),
		ready:   make(chan struct{}),
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (w *blockingRoomResponseWriter) Header() http.Header { return w.header }

func (w *blockingRoomResponseWriter) WriteHeader(int) {}

func (w *blockingRoomResponseWriter) Write(payload []byte) (int, error) {
	w.startOnce.Do(func() { close(w.started) })
	<-w.release
	return len(payload), nil
}

func (w *blockingRoomResponseWriter) Flush() {
	w.readyOnce.Do(func() { close(w.ready) })
}

func TestRoomEventBroker_DropsSlowClientWithoutBlockingPublishers(t *testing.T) {
	broker, err := NewRoomEventBrokerWithOptions([]string{"a"}, RoomStreamBrokerOptions{QueueSize: 1})
	if err != nil {
		t.Fatalf("NewRoomEventBroker: %v", err)
	}
	defer broker.Close()

	requestContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "http://room.test/events", nil).WithContext(requestContext)
	writer := newBlockingRoomResponseWriter()
	handlerDone := make(chan struct{})
	go func() {
		broker.ServeHTTP(writer, request)
		close(handlerDone)
	}()

	select {
	case <-writer.ready:
	case <-time.After(time.Second):
		t.Fatal("slow client handler did not register")
	}
	broker.RecordDiagnostic("a", SessionDiagnosticRecord{Event: "first"})
	select {
	case <-writer.started:
	case <-time.After(time.Second):
		t.Fatal("slow client did not begin its blocked write")
	}

	broker.RecordDiagnostic("a", SessionDiagnosticRecord{Event: "queued"})
	publishDone := make(chan struct{})
	go func() {
		broker.RecordDiagnostic("a", SessionDiagnosticRecord{Event: "overflow"})
		close(publishDone)
	}()
	select {
	case <-publishDone:
	case <-time.After(time.Second):
		t.Fatal("publishing to a slow client blocked the room")
	}

	close(writer.release)
	cancel()
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("dropped slow client handler did not settle")
	}
}

func TestRoomEventBroker_ConcurrentParticipantStreamsPreserveOrder(t *testing.T) {
	const count = 64
	broker, err := NewRoomEventBrokerWithOptions([]string{"a", "b"}, RoomStreamBrokerOptions{QueueSize: count * 2})
	if err != nil {
		t.Fatalf("NewRoomEventBroker: %v", err)
	}
	server := httptest.NewServer(broker)
	defer server.Close()
	defer broker.Close()

	response, err := http.Get(server.URL + "/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer response.Body.Close()
	reader := newRoomSSEReader(response.Body)

	var publishWG sync.WaitGroup
	for _, participantID := range []string{"a", "b"} {
		participantID := participantID
		publishWG.Add(1)
		go func() {
			defer publishWG.Done()
			for sequence := 0; sequence < count; sequence++ {
				broker.RecordDiagnostic(participantID, SessionDiagnosticRecord{
					Event:  "ordered",
					Fields: map[string]string{"sequence": strconv.Itoa(sequence)},
				})
			}
		}()
	}
	publishWG.Wait()

	sequences := map[string][]int{"a": {}, "b": {}}
	for index := 0; index < count*2; index++ {
		payload := reader.next(t)
		participantID := roomSSEString(t, payload, "participant_id")
		var fields map[string]string
		if err := json.Unmarshal(payload["fields"], &fields); err != nil {
			t.Fatalf("ordered fields: %v", err)
		}
		sequence, err := strconv.Atoi(fields["sequence"])
		if err != nil {
			t.Fatalf("ordered sequence = %v: %v", fields, err)
		}
		sequences[participantID] = append(sequences[participantID], sequence)
	}
	for participantID, got := range sequences {
		if len(got) != count {
			t.Fatalf("participant %s event count = %d, want %d", participantID, len(got), count)
		}
		for index, sequence := range got {
			if sequence != index {
				t.Fatalf("participant %s sequence at %d = %d, want %d; all=%v", participantID, index, sequence, index, got)
			}
		}
	}
}

func TestRunRoom_PublishesParticipantAndTerminalRoomEvents(t *testing.T) {
	ids := []string{"a", "b"}
	inferencers := map[string]*roomTestInferencer{
		"a": {events: []messages.StreamMessage{roomTestSessionOpen("a"), roomTestMessageEnd()}},
		"b": {events: []messages.StreamMessage{roomTestSessionOpen("b"), roomTestMessageEnd()}},
	}
	broker, err := NewRoomEventBroker(ids)
	if err != nil {
		t.Fatalf("NewRoomEventBroker: %v", err)
	}
	server := httptest.NewServer(broker)
	defer server.Close()
	defer broker.Close()
	response, err := http.Get(server.URL + "/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer response.Body.Close()
	reader := newRoomSSEReader(response.Body)

	opts, _ := newRoomTestRunOptions(ids, inferencers)
	opts.Manifest.Room.MaxTurns = 1
	opts.Stream = broker
	outcome := make(chan roomTestRunOutcome, 1)
	go func() {
		result, runErr := RunRoomWithResult(context.Background(), io.Discard, opts)
		outcome <- roomTestRunOutcome{result: result, err: runErr}
	}()

	joined := make(map[string]bool)
	terminated := make(map[string]bool)
	diagnostics := make(map[string]bool)
	var terminal map[string]json.RawMessage
	for terminal == nil {
		payload := reader.next(t)
		switch roomSSEString(t, payload, "type") {
		case RoomStreamEventTypeRoom:
			event := roomSSEString(t, payload, "event")
			participantID := roomSSEString(t, payload, "participant_id")
			switch event {
			case RoomStreamEventParticipantJoined:
				joined[participantID] = true
			case RoomStreamEventParticipantTerminated:
				terminated[participantID] = true
			case RoomStreamEventRunTerminated:
				terminal = payload
			}
		case RoomStreamEventTypeDiagnostic:
			if roomSSEString(t, payload, "event") == SessionDiagnosticEventTurn {
				diagnostics[roomSSEString(t, payload, "participant_id")] = true
			}
		}
	}

	if roomSSEString(t, terminal, "participant_id") != RoomStreamRoomParticipantID || roomSSEString(t, terminal, "reason") != string(RoomTerminationMaxTurnsReached) {
		t.Fatalf("terminal room event = %v", terminal)
	}
	for _, participantID := range ids {
		if !joined[participantID] || !terminated[participantID] || !diagnostics[participantID] {
			t.Fatalf("participant %s events joined=%v terminated=%v diagnostic=%v", participantID, joined[participantID], terminated[participantID], diagnostics[participantID])
		}
	}
	select {
	case result := <-outcome:
		if result.err != nil {
			t.Fatalf("RunRoomWithResult: %v", result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("room did not finish after terminal stream event")
	}
}

func TestRoomEventBroker_RejectsReservedRoomParticipant(t *testing.T) {
	_, err := NewRoomEventBroker([]string{"room", "participant"})
	if err == nil || !strings.Contains(err.Error(), RoomStreamRoomParticipantID) {
		t.Fatalf("reserved participant error = %v", err)
	}
}

func TestRoomEventBroker_DropsOnlySlowClientWhenQueueIsFull(t *testing.T) {
	broker, err := NewRoomEventBrokerWithOptions([]string{"a"}, RoomStreamBrokerOptions{QueueSize: 1})
	if err != nil {
		t.Fatalf("NewRoomEventBroker: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "http://room.test/events", nil)
	requestContext, cancel := context.WithCancel(request.Context())
	defer cancel()
	request = request.WithContext(requestContext)
	writer := newBlockingRoomSSEWriter()
	handlerDone := make(chan struct{})
	go func() {
		broker.ServeHTTP(writer, request)
		close(handlerDone)
	}()

	select {
	case <-writer.ready:
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not register the client")
	}
	broker.RecordDiagnostic("a", SessionDiagnosticRecord{Event: "first"})
	select {
	case <-writer.writeStarted:
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not begin writing the first event")
	}

	// The handler is blocked in its writer. One event can wait in its bounded
	// queue; the next one disconnects only this slow client and returns to the
	// publisher immediately.
	broker.RecordDiagnostic("a", SessionDiagnosticRecord{Event: "queued"})
	broker.RecordDiagnostic("a", SessionDiagnosticRecord{Event: "overflow"})

	deadline := time.Now().Add(time.Second)
	for {
		broker.mu.Lock()
		clients := len(broker.clients)
		broker.mu.Unlock()
		if clients == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slow client remained registered after queue overflow")
		}
		time.Sleep(time.Millisecond)
	}
	close(writer.unblock)
	cancel()
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("slow SSE handler did not terminate")
	}
}

type blockingRoomSSEWriter struct {
	header       http.Header
	ready        chan struct{}
	writeStarted chan struct{}
	unblock      chan struct{}
	readyOnce    sync.Once
	writeOnce    sync.Once
}

func newBlockingRoomSSEWriter() *blockingRoomSSEWriter {
	return &blockingRoomSSEWriter{
		header:       make(http.Header),
		ready:        make(chan struct{}),
		writeStarted: make(chan struct{}, 1),
		unblock:      make(chan struct{}),
	}
}

func (w *blockingRoomSSEWriter) Header() http.Header { return w.header }

func (w *blockingRoomSSEWriter) WriteHeader(_ int) {}

func (w *blockingRoomSSEWriter) Write(payload []byte) (int, error) {
	w.writeOnce.Do(func() { w.writeStarted <- struct{}{} })
	<-w.unblock
	return len(payload), nil
}

func (w *blockingRoomSSEWriter) Flush() {
	w.readyOnce.Do(func() { close(w.ready) })
}

var _ SessionDiagnosticSink = RoomParticipantEventSink{}
