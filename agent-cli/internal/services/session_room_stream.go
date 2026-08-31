package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// RoomStreamRoomParticipantID is the participant identity used for room-owned
// lifecycle events. It is deliberately distinct from every configured agent.
const RoomStreamRoomParticipantID = "room"

// Room stream payload type names. These are the values carried in the JSON
// object's type field; the SSE event itself is intentionally just a data frame
// so browsers can consume it with EventSource.onmessage.
const (
	RoomStreamEventTypeDiagnostic      = "diagnostic"
	RoomStreamEventTypeTranscriptDelta = "transcript_delta"
	RoomStreamEventTypeTranscriptEnd   = "transcript_end"
	RoomStreamEventTypeRoom            = "room"
)

// Room stream lifecycle event names.
const (
	RoomStreamEventParticipantJoined        = "participant_joined"
	RoomStreamEventParticipantReady         = "participant_ready"
	RoomStreamEventParticipantFailed        = "participant_failed"
	RoomStreamEventParticipantLivenessFault = "participant_liveness_fault"
	RoomStreamEventParticipantTerminated    = "participant_terminated"
	RoomStreamEventRunTerminated            = "run_terminated"
)

const defaultRoomStreamQueueSize = 128

var (
	// ErrRoomStreamUnknownParticipant is returned to an HTTP client that asks
	// for a participant filter not present in the manifest used to construct the
	// broker.
	ErrRoomStreamUnknownParticipant = errors.New("unknown room stream participant")
	// ErrRoomStreamInvalidParticipant identifies an empty or reserved participant
	// ID supplied while constructing a broker.
	ErrRoomStreamInvalidParticipant = errors.New("invalid room stream participant")
)

// RoomStreamEvent is the JSON object carried in one SSE data frame. The
// broker's typed publishing methods populate only the fields valid for the
// selected Type, keeping diagnostic and transcript projections separate.
type RoomStreamEvent struct {
	Type          string            `json:"type"`
	ParticipantID string            `json:"participant_id"`
	Event         string            `json:"event"`
	Fields        map[string]string `json:"fields"`
	Text          string            `json:"text"`
	FullText      string            `json:"full_text"`
	Reason        string            `json:"reason"`
	TS            string            `json:"ts"`
}

// MarshalJSON pins each projection to the section-10 wire shape. In
// particular, a transcript event can never accidentally acquire diagnostic
// fields or raw stream/audio data through a shared envelope.
func (e RoomStreamEvent) MarshalJSON() ([]byte, error) {
	switch e.Type {
	case RoomStreamEventTypeDiagnostic:
		fields := cloneRoomStreamFields(e.Fields)
		if fields == nil {
			fields = map[string]string{}
		}
		return json.Marshal(struct {
			Type          string            `json:"type"`
			ParticipantID string            `json:"participant_id"`
			Event         string            `json:"event"`
			Fields        map[string]string `json:"fields"`
			TS            string            `json:"ts"`
		}{
			Type:          e.Type,
			ParticipantID: e.ParticipantID,
			Event:         e.Event,
			Fields:        fields,
			TS:            e.TS,
		})
	case RoomStreamEventTypeTranscriptDelta:
		return json.Marshal(struct {
			Type          string `json:"type"`
			ParticipantID string `json:"participant_id"`
			Text          string `json:"text"`
			TS            string `json:"ts"`
		}{
			Type:          e.Type,
			ParticipantID: e.ParticipantID,
			Text:          e.Text,
			TS:            e.TS,
		})
	case RoomStreamEventTypeTranscriptEnd:
		return json.Marshal(struct {
			Type          string `json:"type"`
			ParticipantID string `json:"participant_id"`
			FullText      string `json:"full_text"`
			TS            string `json:"ts"`
		}{
			Type:          e.Type,
			ParticipantID: e.ParticipantID,
			FullText:      e.FullText,
			TS:            e.TS,
		})
	case RoomStreamEventTypeRoom:
		return json.Marshal(struct {
			Type          string `json:"type"`
			Event         string `json:"event"`
			ParticipantID string `json:"participant_id"`
			Reason        string `json:"reason,omitempty"`
			TS            string `json:"ts"`
		}{
			Type:          e.Type,
			Event:         e.Event,
			ParticipantID: e.ParticipantID,
			Reason:        e.Reason,
			TS:            e.TS,
		})
	default:
		return nil, fmt.Errorf("unknown room stream event type %q", e.Type)
	}
}

// RoomStreamBrokerOptions controls bounded subscriber queues and timestamp
// generation. Now is primarily a deterministic offline-test seam; production
// callers leave it nil and receive UTC wall-clock timestamps.
type RoomStreamBrokerOptions struct {
	QueueSize int
	Now       func() time.Time
}

// RoomEventBroker is a forward-only, in-memory SSE fan-out for one room. It
// retains no event history. Each HTTP client owns one bounded queue; a client
// whose queue fills is disconnected so a slow observer can never block a live
// room or any other observer.
type RoomEventBroker struct {
	participants      map[string]struct{}
	participantSerial map[string]*sync.Mutex
	queueSize         int
	now               func() time.Time

	mu      sync.Mutex
	clients map[*roomEventClient]struct{}
	closed  bool
}

type roomEventClient struct {
	participant string
	events      chan []byte
}

// NewRoomEventBroker constructs a broker with the exact participant IDs that
// may be used by /events?participant=. The reserved "room" identity is always
// accepted as the filter for room-owned events.
func NewRoomEventBroker(participantIDs []string) (*RoomEventBroker, error) {
	return NewRoomEventBrokerWithOptions(participantIDs, RoomStreamBrokerOptions{})
}

// NewRoomEventBrokerWithOptions is the configurable constructor used by
// deterministic tests and by the room composition root.
func NewRoomEventBrokerWithOptions(participantIDs []string, options RoomStreamBrokerOptions) (*RoomEventBroker, error) {
	participants := make(map[string]struct{}, len(participantIDs))
	serial := make(map[string]*sync.Mutex, len(participantIDs)+1)
	for _, participantID := range participantIDs {
		if strings.TrimSpace(participantID) == "" || participantID == RoomStreamRoomParticipantID {
			return nil, fmt.Errorf("%w: %q", ErrRoomStreamInvalidParticipant, participantID)
		}
		if _, exists := participants[participantID]; exists {
			return nil, fmt.Errorf("%w: duplicate %q", ErrRoomStreamInvalidParticipant, participantID)
		}
		participants[participantID] = struct{}{}
		serial[participantID] = &sync.Mutex{}
	}
	serial[RoomStreamRoomParticipantID] = &sync.Mutex{}

	queueSize := options.QueueSize
	if queueSize <= 0 {
		queueSize = defaultRoomStreamQueueSize
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &RoomEventBroker{
		participants:      participants,
		participantSerial: serial,
		queueSize:         queueSize,
		now:               now,
		clients:           make(map[*roomEventClient]struct{}),
	}, nil
}

// NewRoomStreamBroker is a descriptive alias for NewRoomEventBroker.
func NewRoomStreamBroker(participantIDs []string) (*RoomEventBroker, error) {
	return NewRoomEventBroker(participantIDs)
}

// ParticipantIDs returns a sorted-independent snapshot of configured IDs. The
// caller must not rely on map iteration order; the snapshot is intended for
// validating composition and building UI metadata.
func (b *RoomEventBroker) ParticipantIDs() []string {
	if b == nil {
		return nil
	}
	ids := make([]string, 0, len(b.participants))
	for participantID := range b.participants {
		ids = append(ids, participantID)
	}
	return ids
}

// ValidateParticipants confirms that the broker was created for exactly the
// room participant set. It lets RunRoom fail before live session construction
// when a stream broker was composed from the wrong manifest.
func (b *RoomEventBroker) ValidateParticipants(participantIDs []string) error {
	if b == nil {
		return nil
	}
	if len(participantIDs) != len(b.participants) {
		return fmt.Errorf("room stream participant set has %d IDs, room manifest has %d", len(b.participants), len(participantIDs))
	}
	seen := make(map[string]struct{}, len(participantIDs))
	for _, participantID := range participantIDs {
		if _, duplicate := seen[participantID]; duplicate {
			return fmt.Errorf("room stream participant set contains duplicate %q", participantID)
		}
		seen[participantID] = struct{}{}
		if _, known := b.participants[participantID]; !known {
			return fmt.Errorf("room stream is missing participant %q", participantID)
		}
	}
	return nil
}

// ParticipantSink returns the paired diagnostic and raw-stream adapters for a
// participant. The adapters only emit the diagnostic projection and the two
// transcript projections; audio and all other raw deltas are ignored.
func (b *RoomEventBroker) ParticipantSink(participantID string) RoomParticipantEventSink {
	return RoomParticipantEventSink{broker: b, ParticipantID: participantID}
}

// RoomParticipantEventSink adapts one broker to the existing per-session
// observer interfaces. It is safe to use as both SessionDiagnosticSink and a
// SessionStreamObserver callback.
type RoomParticipantEventSink struct {
	broker        *RoomEventBroker
	ParticipantID string
}

type roomDiagnosticSinkFanout []SessionDiagnosticSink

func combineRoomDiagnosticSinks(sinks ...SessionDiagnosticSink) SessionDiagnosticSink {
	filtered := make(roomDiagnosticSinkFanout, 0, len(sinks))
	for _, sink := range sinks {
		if sink != nil {
			filtered = append(filtered, sink)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	if len(filtered) == 1 {
		return filtered[0]
	}
	return filtered
}

func (f roomDiagnosticSinkFanout) RecordSessionDiagnostic(record SessionDiagnosticRecord) {
	for _, sink := range f {
		if sink != nil {
			sink.RecordSessionDiagnostic(record)
		}
	}
}

// RecordSessionDiagnostic implements SessionDiagnosticSink.
func (s RoomParticipantEventSink) RecordSessionDiagnostic(record SessionDiagnosticRecord) {
	if s.broker != nil {
		s.broker.RecordDiagnostic(s.ParticipantID, record)
	}
}

// ObserveStream is the callback form suitable for SessionRunOptions.
func (s RoomParticipantEventSink) ObserveStream(message messages.StreamMessage) {
	if s.broker != nil {
		s.broker.ObserveStream(s.ParticipantID, message)
	}
}

// StreamObserver returns the callback form without requiring callers to wrap
// the method themselves.
func (s RoomParticipantEventSink) StreamObserver() SessionStreamObserver {
	return s.ObserveStream
}

// RecordDiagnostic publishes one exact SessionDiagnosticRecord projection.
// Invalid participant IDs are ignored because this method is an observational
// sink and cannot return an error through the SessionDiagnosticSink interface.
func (b *RoomEventBroker) RecordDiagnostic(participantID string, record SessionDiagnosticRecord) {
	b.withParticipantSerial(participantID, func() {
		b.publish(RoomStreamEvent{
			Type:          RoomStreamEventTypeDiagnostic,
			ParticipantID: participantID,
			Event:         record.Event,
			Fields:        record.Fields,
			TS:            b.timestamp(),
		})
	})
}

// ObserveStream filters a session delta stream down to the user-facing
// transcript projection. In particular, AUDIO.DELTA is never serialized into
// an SSE frame.
func (b *RoomEventBroker) ObserveStream(participantID string, message messages.StreamMessage) {
	b.withParticipantSerial(participantID, func() {
		switch message.Type {
		case messages.StreamTypeTranscriptDelta:
			value, ok := message.Value.(*messages.TranscriptDeltaValue)
			if !ok || value == nil {
				return
			}
			b.publish(RoomStreamEvent{
				Type:          RoomStreamEventTypeTranscriptDelta,
				ParticipantID: participantID,
				Text:          value.Text,
				TS:            b.timestamp(),
			})
		case messages.StreamTypeTranscriptEnd:
			value, ok := message.Value.(*messages.TranscriptEndValue)
			if !ok || value == nil {
				return
			}
			b.publish(RoomStreamEvent{
				Type:          RoomStreamEventTypeTranscriptEnd,
				ParticipantID: participantID,
				FullText:      value.FullText,
				TS:            b.timestamp(),
			})
		}
	})
}

// PublishRoomEvent publishes a room lifecycle event. An omitted participant
// ID is normalized to "room"; an optional reason is emitted only when present.
func (b *RoomEventBroker) PublishRoomEvent(event, participantID string, reason ...string) {
	if participantID == "" {
		participantID = RoomStreamRoomParticipantID
	}
	roomReason := ""
	if len(reason) > 0 {
		roomReason = reason[0]
	}
	b.withParticipantSerial(participantID, func() {
		b.publish(RoomStreamEvent{
			Type:          RoomStreamEventTypeRoom,
			ParticipantID: participantID,
			Event:         event,
			Reason:        roomReason,
			TS:            b.timestamp(),
		})
	})
}

// PublishParticipantLivenessFault publishes the room-owned explanation for a
// positively classified provider liveness failure. The failed participant ID
// remains in the payload for attribution, while the broker broadcasts this
// event to every current subscriber so a peer-filtered observer is not left
// waiting on a participant that has gone dark.
func (b *RoomEventBroker) PublishParticipantLivenessFault(participantID, classification string) {
	participantID = strings.TrimSpace(participantID)
	classification = strings.TrimSpace(classification)
	if participantID == "" || classification == "" {
		return
	}
	b.PublishRoomEvent(RoomStreamEventParticipantLivenessFault, participantID, classification)
}

// ServeHTTP serves GET /events as a forward-only JSON SSE stream.
func (b *RoomEventBroker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/events" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "room event stream requires GET /events", http.StatusMethodNotAllowed)
		return
	}

	participantID := r.URL.Query().Get("participant")
	if participantID != "" && !b.knownParticipant(participantID) {
		http.Error(w, fmt.Sprintf("%v: %q", ErrRoomStreamUnknownParticipant, participantID), http.StatusBadRequest)
		return
	}
	if b.isClosed() {
		http.Error(w, "room event stream is closed", http.StatusGone)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "room event stream requires an HTTP flusher", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	// The CLI documents serving the static visualizer from a second loopback
	// port. Room streams are local metadata endpoints, so allow that browser
	// EventSource connection without requiring a proxy or disabling security.
	w.Header().Set("Access-Control-Allow-Origin", "*")

	client := &roomEventClient{
		participant: participantID,
		events:      make(chan []byte, b.queueSize),
	}
	if !b.addClient(client) {
		http.Error(w, "room event stream is closed", http.StatusGone)
		return
	}
	defer b.removeClient(client)

	// Flush headers immediately so the caller knows the stream is connected even
	// when the room has not emitted its first event yet.
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case payload, open := <-client.events:
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

// Close disconnects every subscriber and prevents new subscriptions. Buffered
// frames remain readable by existing handlers, so a terminal room event queued
// immediately before Close is still delivered in order. Close is idempotent.
func (b *RoomEventBroker) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	for client := range b.clients {
		delete(b.clients, client)
		close(client.events)
	}
	b.mu.Unlock()
	return nil
}

func (b *RoomEventBroker) publish(event RoomStreamEvent) {
	if b == nil {
		return
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	frame := make([]byte, 0, len(payload)+8)
	frame = append(frame, "data: "...)
	frame = append(frame, payload...)
	frame = append(frame, '\n', '\n')

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	broadcastToAll := event.Type == RoomStreamEventTypeRoom && event.Event == RoomStreamEventParticipantLivenessFault
	for client := range b.clients {
		if !broadcastToAll && client.participant != "" && client.participant != event.ParticipantID {
			continue
		}
		select {
		case client.events <- frame:
		default:
			// A full queue means this client is slower than the bounded observer
			// contract permits. Drop only that client, never the publisher or
			// another subscriber.
			delete(b.clients, client)
			close(client.events)
		}
	}
}

func (b *RoomEventBroker) withParticipantSerial(participantID string, fn func()) {
	if b == nil || fn == nil {
		return
	}
	serial, known := b.participantSerial[participantID]
	if !known {
		return
	}
	serial.Lock()
	defer serial.Unlock()
	fn()
}

func (b *RoomEventBroker) timestamp() string {
	now := time.Now()
	if b != nil && b.now != nil {
		now = b.now()
	}
	return now.UTC().Format(time.RFC3339Nano)
}

func (b *RoomEventBroker) knownParticipant(participantID string) bool {
	if b == nil {
		return false
	}
	if participantID == RoomStreamRoomParticipantID {
		return true
	}
	_, ok := b.participants[participantID]
	return ok
}

func (b *RoomEventBroker) isClosed() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	return closed
}

func (b *RoomEventBroker) addClient(client *roomEventClient) bool {
	if b == nil || client == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false
	}
	b.clients[client] = struct{}{}
	return true
}

func (b *RoomEventBroker) removeClient(client *roomEventClient) {
	if b == nil || client == nil {
		return
	}
	b.mu.Lock()
	if _, exists := b.clients[client]; exists {
		delete(b.clients, client)
		close(client.events)
	}
	b.mu.Unlock()
}

func cloneRoomStreamFields(fields map[string]string) map[string]string {
	if fields == nil {
		return nil
	}
	copyFields := make(map[string]string, len(fields))
	for key, value := range fields {
		copyFields[key] = value
	}
	return copyFields
}

var _ http.Handler = (*RoomEventBroker)(nil)
