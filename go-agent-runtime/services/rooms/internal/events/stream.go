package events

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

// Stream is the room event stream: one broker plus its live projection.
type Stream struct {
	broker *Broker
	sink   *LiveSink
}

// NewStream builds a stream whose every frame passes through the redactor.
func NewStream(options rooms.RoomEventStreamOptions) (*Stream, error) {
	brokerOptions := Options{QueueSize: options.QueueSize, Now: options.Now}
	if options.Redactor != nil {
		brokerOptions.Redact = options.Redactor.Redact
	}
	broker, err := New(options.ParticipantIDs, brokerOptions)
	if err != nil {
		return nil, err
	}
	return &Stream{broker: broker, sink: NewLiveSink(broker)}, nil
}

// Publish projects one live observation for participantID.
func (s *Stream) Publish(ctx context.Context, participantID string, event session.LiveEvent) error {
	return s.sink.Publish(ctx, participantID, event)
}

// PublishRoomEvent projects one lifecycle event.
func (s *Stream) PublishRoomEvent(event, participantID, reason string) {
	s.broker.PublishRoomEvent(event, participantID, reason)
}

// Subscribe registers one forward-only subscriber.
func (s *Stream) Subscribe(participantID string) (rooms.RoomEventSubscription, error) {
	subscription, err := s.broker.Subscribe(participantID)
	if err != nil {
		return nil, err
	}
	return subscription, nil
}

// Close closes every subscriber.
func (s *Stream) Close() error { return s.broker.Close() }

var _ rooms.RoomEventStream = (*Stream)(nil)
