// Package events owns the room's bounded live event fan-out. Room execution
// only sees the transport-neutral sink; this package keeps subscriber
// filtering, redaction, and queue ownership out of the room lifecycle and out
// of any host transport.
package events

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

const defaultQueueSize = 128

// Options controls the bounded subscriber queues, timestamp source, and the
// redaction applied to every projected field.
type Options struct {
	QueueSize int
	Now       func() time.Time
	Redact    func(string) string
}

// Broker is a forward-only, in-memory fan-out. A slow subscriber is removed
// when its bounded queue fills, so publishing never blocks room workers.
type Broker struct {
	participants map[string]struct{}
	serial       map[string]*sync.Mutex
	queueSize    int
	now          func() time.Time
	redact       func(string) string

	mu      sync.Mutex
	clients map[*Subscription]struct{}
	closed  bool
}

// Subscription is one subscriber's bounded frame queue.
type Subscription struct {
	broker      *Broker
	participant string
	frames      chan []byte
}

// New validates the room's participant identities and returns an open broker.
func New(participantIDs []string, options Options) (*Broker, error) {
	participants := make(map[string]struct{}, len(participantIDs))
	serial := make(map[string]*sync.Mutex, len(participantIDs)+1)
	for _, id := range participantIDs {
		if strings.TrimSpace(id) == "" || id == rooms.RoomStreamParticipantID {
			return nil, fmt.Errorf("%w: %q", rooms.ErrInvalidRoomStreamParticipant, id)
		}
		if _, exists := participants[id]; exists {
			return nil, fmt.Errorf("%w: duplicate %q", rooms.ErrInvalidRoomStreamParticipant, id)
		}
		participants[id] = struct{}{}
		serial[id] = &sync.Mutex{}
	}
	serial[rooms.RoomStreamParticipantID] = &sync.Mutex{}
	queueSize := options.QueueSize
	if queueSize <= 0 {
		queueSize = defaultQueueSize
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	redact := options.Redact
	if redact == nil {
		redact = func(value string) string { return value }
	}
	return &Broker{participants: participants, serial: serial, queueSize: queueSize, now: now, redact: redact, clients: make(map[*Subscription]struct{})}, nil
}

// Subscribe registers a forward-only subscriber. An empty participant
// receives every event.
func (b *Broker) Subscribe(participant string) (*Subscription, error) {
	if participant != "" && !b.known(participant) {
		return nil, fmt.Errorf("%w: %q", rooms.ErrUnknownRoomStreamParticipant, participant)
	}
	stream := &Subscription{broker: b, participant: participant, frames: make(chan []byte, b.queueSize)}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, rooms.ErrRoomEventStreamClosed
	}
	b.clients[stream] = struct{}{}
	return stream, nil
}

// Frames yields JSON-encoded events until the subscriber is removed.
func (s *Subscription) Frames() <-chan []byte { return s.frames }

// Close unregisters the subscriber; repeated calls are harmless.
func (s *Subscription) Close() {
	if s == nil || s.broker == nil {
		return
	}
	s.broker.remove(s)
}

// Close closes every subscriber queue. Later publishes are dropped.
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
		close(stream.frames)
	}
	return nil
}

// Diagnostic projects one participant diagnostic record.
func (b *Broker) Diagnostic(participant, event string, fields map[string]string) {
	b.withSerial(participant, func() {
		b.publish(Event{Type: rooms.RoomStreamTypeDiagnostic, ParticipantID: participant, Event: event, Fields: fields, TS: b.timestamp()})
	})
}

// TranscriptDelta projects one provider transcript fragment without exposing
// the provider's raw event envelope.
func (b *Broker) TranscriptDelta(participant, text string) {
	b.withSerial(participant, func() {
		b.publish(Event{Type: rooms.RoomStreamTypeTranscriptDelta, ParticipantID: participant, Text: text, TS: b.timestamp()})
	})
}

// TranscriptEnd projects the completed provider transcript.
func (b *Broker) TranscriptEnd(participant, text string) {
	b.withSerial(participant, func() {
		b.publish(Event{Type: rooms.RoomStreamTypeTranscriptEnd, ParticipantID: participant, FullText: text, TS: b.timestamp()})
	})
}

// PublishRoomEvent projects one lifecycle event. Transcript events use their
// dedicated methods so the stream cannot lose text payloads by routing them
// through a lifecycle event. An empty participant names the room.
func (b *Broker) PublishRoomEvent(event, participant, reason string) {
	if participant == "" {
		participant = rooms.RoomStreamParticipantID
	}
	b.withSerial(participant, func() {
		b.publish(Event{Type: rooms.RoomStreamTypeRoom, Event: event, ParticipantID: participant, Reason: reason, TS: b.timestamp()})
	})
}

func (b *Broker) publish(event Event) {
	if b == nil {
		return
	}
	payload, err := json.Marshal(event.redacted(b.redact))
	if err != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	broadcast := event.Type == rooms.RoomStreamTypeRoom && event.Event == rooms.RoomStreamEventParticipantLivenessFault
	for stream := range b.clients {
		if !broadcast && stream.participant != "" && stream.participant != event.ParticipantID {
			continue
		}
		select {
		case stream.frames <- payload:
		default:
			delete(b.clients, stream)
			close(stream.frames)
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
	return b.now().UTC().Format(time.RFC3339Nano)
}

func (b *Broker) known(participant string) bool {
	if participant == rooms.RoomStreamParticipantID {
		return true
	}
	_, ok := b.participants[participant]
	return ok
}

func (b *Broker) remove(stream *Subscription) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.clients[stream]; ok {
		delete(b.clients, stream)
		close(stream.frames)
	}
}
