// Package events owns the CLI room's bounded SSE transport. Room execution
// only sees the transport-neutral callbacks installed by the adapter; this
// package keeps HTTP clients, filtering, and queue ownership out of the room
// lifecycle.
package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	RoomParticipantID             = "room"
	EventDiagnostic               = "diagnostic"
	EventTranscriptDelta          = "transcript_delta"
	EventTranscriptEnd            = "transcript_end"
	EventRoom                     = "room"
	EventParticipantJoined        = "participant_joined"
	EventParticipantReady         = "participant_ready"
	EventParticipantFailed        = "participant_failed"
	EventParticipantLivenessFault = "participant_liveness_fault"
	EventParticipantTerminated    = "participant_terminated"
	EventRunTerminated            = "run_terminated"
)

const defaultQueueSize = 128

var (
	ErrUnknownParticipant = errors.New("unknown room stream participant")
	ErrInvalidParticipant = errors.New("invalid room stream participant")
)

// Options controls the bounded client queues and timestamp source.
type Options struct {
	QueueSize int
	Now       func() time.Time
}

// Event is the stable JSON projection carried by one SSE data frame.
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

func (e Event) MarshalJSON() ([]byte, error) {
	switch e.Type {
	case EventDiagnostic:
		fields := cloneFields(e.Fields)
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
	case EventTranscriptDelta:
		return json.Marshal(struct {
			Type          string `json:"type"`
			ParticipantID string `json:"participant_id"`
			Text          string `json:"text"`
			TS            string `json:"ts"`
		}{e.Type, e.ParticipantID, e.Text, e.TS})
	case EventTranscriptEnd:
		return json.Marshal(struct {
			Type          string `json:"type"`
			ParticipantID string `json:"participant_id"`
			FullText      string `json:"full_text"`
			TS            string `json:"ts"`
		}{e.Type, e.ParticipantID, e.FullText, e.TS})
	case EventRoom:
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

type client struct {
	participant string
	events      chan []byte
}

// Broker is a forward-only, in-memory SSE fan-out. A slow client is removed
// when its bounded queue fills, so publishing never blocks room workers.
type Broker struct {
	participants map[string]struct{}
	serial       map[string]*sync.Mutex
	queueSize    int
	now          func() time.Time

	mu      sync.Mutex
	clients map[*client]struct{}
	closed  bool
}

func New(participantIDs []string, options Options) (*Broker, error) {
	participants := make(map[string]struct{}, len(participantIDs))
	serial := make(map[string]*sync.Mutex, len(participantIDs)+1)
	for _, id := range participantIDs {
		if strings.TrimSpace(id) == "" || id == RoomParticipantID {
			return nil, fmt.Errorf("%w: %q", ErrInvalidParticipant, id)
		}
		if _, exists := participants[id]; exists {
			return nil, fmt.Errorf("%w: duplicate %q", ErrInvalidParticipant, id)
		}
		participants[id] = struct{}{}
		serial[id] = &sync.Mutex{}
	}
	serial[RoomParticipantID] = &sync.Mutex{}
	queueSize := options.QueueSize
	if queueSize <= 0 {
		queueSize = defaultQueueSize
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Broker{participants: participants, serial: serial, queueSize: queueSize, now: now, clients: make(map[*client]struct{})}, nil
}

func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	stream, flusher, ok := b.prepareStream(w, r)
	if !ok {
		return
	}
	defer b.remove(stream)
	b.writeStream(w, r, flusher, stream)
}

func (b *Broker) prepareStream(w http.ResponseWriter, r *http.Request) (*client, http.Flusher, bool) {
	if !b.validRoute(w, r) {
		return nil, nil, false
	}
	participant, ok := b.streamParticipant(w, r)
	if !ok {
		return nil, nil, false
	}
	if b.isClosed() {
		http.Error(w, "room event stream is closed", http.StatusGone)
		return nil, nil, false
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "room event stream requires an HTTP flusher", http.StatusInternalServerError)
		return nil, nil, false
	}
	setStreamHeaders(w)
	stream := &client{participant: participant, events: make(chan []byte, b.queueSize)}
	if !b.add(stream) {
		http.Error(w, "room event stream is closed", http.StatusGone)
		return nil, nil, false
	}
	return stream, flusher, true
}

func (b *Broker) validRoute(w http.ResponseWriter, r *http.Request) bool {
	if b == nil || r.URL.Path != "/events" {
		http.NotFound(w, r)
		return false
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "room event stream requires GET /events", http.StatusMethodNotAllowed)
		return false
	}
	return true
}

func (b *Broker) streamParticipant(w http.ResponseWriter, r *http.Request) (string, bool) {
	participant := r.URL.Query().Get("participant")
	if participant != "" && !b.known(participant) {
		http.Error(w, fmt.Sprintf("%v: %q", ErrUnknownParticipant, participant), http.StatusBadRequest)
		return "", false
	}
	return participant, true
}

func setStreamHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Access-Control-Allow-Origin", "*")
}

func (b *Broker) writeStream(w http.ResponseWriter, r *http.Request, flusher http.Flusher, stream *client) {
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case payload, open := <-stream.events:
			if !open {
				return
			}
			if _, err := w.Write(payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (b *Broker) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	for stream := range b.clients {
		delete(b.clients, stream)
		close(stream.events)
	}
	return nil
}

func (b *Broker) RecordDiagnostic(participant, event string, fields map[string]string) {
	b.withSerial(participant, func() {
		b.publish(Event{Type: EventDiagnostic, ParticipantID: participant, Event: event, Fields: fields, TS: b.timestamp()})
	})
}

// Diagnostic is the small projection interface consumed by the room adapter.
// Keep the transport-neutral name at the boundary while retaining the more
// explicit method for callers that publish directly to this package.
func (b *Broker) Diagnostic(participant, event string, fields map[string]string) {
	b.RecordDiagnostic(participant, event, fields)
}

func (b *Broker) PublishTranscriptDelta(participant, text string) {
	b.withSerial(participant, func() {
		b.publish(Event{Type: EventTranscriptDelta, ParticipantID: participant, Text: text, TS: b.timestamp()})
	})
}

// TranscriptDelta projects one provider transcript fragment to the CLI event
// stream without exposing the provider's raw event envelope.
func (b *Broker) TranscriptDelta(participant, text string) {
	b.PublishTranscriptDelta(participant, text)
}

func (b *Broker) PublishTranscriptEnd(participant, text string) {
	b.withSerial(participant, func() {
		b.publish(Event{Type: EventTranscriptEnd, ParticipantID: participant, FullText: text, TS: b.timestamp()})
	})
}

// TranscriptEnd projects the completed provider transcript to the CLI stream.
func (b *Broker) TranscriptEnd(participant, text string) {
	b.PublishTranscriptEnd(participant, text)
}

func (b *Broker) PublishRoomEvent(event, participant string, reason ...string) {
	if participant == "" {
		participant = RoomParticipantID
	}
	value := ""
	if len(reason) > 0 {
		value = reason[0]
	}
	b.withSerial(participant, func() {
		b.publish(Event{Type: EventRoom, Event: event, ParticipantID: participant, Reason: value, TS: b.timestamp()})
	})
}

// Publish is the room adapter's lifecycle projection. Transcript events use
// their dedicated methods above so the stream cannot accidentally lose text
// payloads by routing them through a lifecycle event.
func (b *Broker) Publish(event, participant, reason string) {
	b.PublishRoomEvent(event, participant, reason)
}

func (b *Broker) publish(event Event) {
	if b == nil {
		return
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	frame := append(append([]byte("data: "), payload...), '\n', '\n')
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	broadcast := event.Type == EventRoom && event.Event == EventParticipantLivenessFault
	for stream := range b.clients {
		if !broadcast && stream.participant != "" && stream.participant != event.ParticipantID {
			continue
		}
		select {
		case stream.events <- frame:
		default:
			delete(b.clients, stream)
			close(stream.events)
		}
	}
}

func (b *Broker) withSerial(participant string, fn func()) {
	if b == nil || fn == nil {
		return
	}
	serial, ok := b.serial[participant]
	if !ok {
		return
	}
	serial.Lock()
	defer serial.Unlock()
	fn()
}

func (b *Broker) timestamp() string {
	now := time.Now()
	if b != nil && b.now != nil {
		now = b.now()
	}
	return now.UTC().Format(time.RFC3339Nano)
}

func (b *Broker) known(participant string) bool {
	if participant == RoomParticipantID {
		return true
	}
	_, ok := b.participants[participant]
	return ok
}

func (b *Broker) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

func (b *Broker) add(stream *client) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false
	}
	b.clients[stream] = struct{}{}
	return true
}

func (b *Broker) remove(stream *client) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.clients[stream]; ok {
		delete(b.clients, stream)
		close(stream.events)
	}
}

func cloneFields(fields map[string]string) map[string]string {
	if fields == nil {
		return nil
	}
	result := make(map[string]string, len(fields))
	for key, value := range fields {
		result[key] = value
	}
	return result
}

var _ http.Handler = (*Broker)(nil)
