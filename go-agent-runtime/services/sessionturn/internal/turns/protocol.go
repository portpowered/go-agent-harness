package turns

import (
	"context"
	"errors"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

const (
	errInputRejected  sessionturn.Error = "send turn input: session rejected message"
	errCommitRejected sessionturn.Error = "commit turn input: session rejected message"
)

// sendInput sends text as one delta, or audio followed by the MESSAGE.END
// boundary that commits it.
func sendInput(ctx context.Context, session messages.Session, input sessionturn.TurnInput) error {
	msg := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(input.Text)}
	if len(input.Audio) != 0 {
		msg = messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValueWithMediaType(input.Audio, input.MediaType)}
	}
	if !session.Send(ctx, msg) {
		return errors.Join(ctx.Err(), errInputRejected)
	}
	if len(input.Audio) != 0 && !session.Send(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}) {
		return errors.Join(ctx.Err(), errCommitRejected)
	}
	return nil
}

// readResponse collects deltas until MESSAGE.END. Non-terminal provider errors
// are skipped; terminal ones end the turn with their cause.
func readResponse(ctx context.Context, session messages.Session) (messages.Message, error) {
	buffer := session.Receive()
	var deltas []messages.StreamMessage
	for {
		var msg messages.StreamMessage
		select {
		case msg = <-buffer.Chan():
		case <-ctx.Done():
			return messages.Message{}, ctx.Err()
		case <-session.Done():
			return messages.Message{}, transitionError(opRead, sessionturn.ErrSessionClosed)
		}
		if msg.Type == messages.StreamTypeError {
			var value *messages.ErrorValue
			if typed, ok := msg.Value.(*messages.ErrorValue); ok {
				value = typed
			}
			if value != nil && value.IsNonTerminal() {
				continue
			}
			return messages.Message{}, responseError(value)
		}
		deltas = append(deltas, msg)
		if msg.Type == messages.StreamTypeMessageEnd {
			return messages.ReconstructModelMessageFromDeltas(deltas), nil
		}
	}
}

func responseError(value *messages.ErrorValue) error {
	if value != nil && value.Err != nil {
		return value.Err
	}
	if value == nil || strings.TrimSpace(value.Message) == "" {
		return sessionturn.ErrSessionResponse
	}
	return errors.New(value.Message)
}
