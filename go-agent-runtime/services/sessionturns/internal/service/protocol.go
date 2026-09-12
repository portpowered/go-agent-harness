package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturns"
)

func sendTurnInput(ctx context.Context, connection messages.Session, input sessionturns.TurnInput) error {
	var value messages.StreamMessageValue = messages.NewTextDeltaValue(input.Text)
	if len(input.Audio) != 0 {
		value = messages.NewAudioDeltaValueWithMediaType(append([]byte(nil), input.Audio...), input.MediaType)
	}
	if err := sendMessage(ctx, connection, messages.StreamMessage{Type: inputMessageType(input), Value: value}, sessionturns.ErrTurnInputRejected, "send turn input"); err != nil {
		return err
	}
	if len(input.Audio) == 0 {
		return nil
	}
	commit := messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	}
	return sendMessage(ctx, connection, commit, sessionturns.ErrTurnInputCommitRejected, "commit turn input")
}

func inputMessageType(input sessionturns.TurnInput) messages.StreamMessageType {
	if len(input.Audio) != 0 {
		return messages.StreamTypeAudioDelta
	}
	return messages.StreamTypeTextDelta
}

func sendMessage(ctx context.Context, connection messages.Session, message messages.StreamMessage, fallback error, operation string) error {
	outcome := messages.SendSessionWithOutcome(ctx, connection, message)
	if outcome.OK() {
		return nil
	}
	cause := outcome.Err
	if cause == nil {
		switch outcome.Status {
		case messages.SessionSendCancelled:
			cause = context.Canceled
		case messages.SessionSendTimedOut:
			cause = context.DeadlineExceeded
		default:
			cause = fallback
		}
	}
	return fmt.Errorf("%s: %w", operation, cause)
}

func readTurnResponse(ctx context.Context, connection messages.Session) (messages.Message, error) {
	buffer := connection.Receive()
	if buffer == nil {
		return messages.Message{}, fmt.Errorf("read turn: %w", sessionturns.ErrMissingTurnSession)
	}
	var deltas []messages.StreamMessage
	for {
		if err := ctx.Err(); err != nil {
			return messages.Message{}, err
		}
		var message messages.StreamMessage
		select {
		case message = <-buffer.Chan():
		case <-ctx.Done():
			return messages.Message{}, ctx.Err()
		case <-connection.Done():
			if err := ctx.Err(); err != nil {
				return messages.Message{}, err
			}
			return messages.Message{}, transitionError("read", sessionturns.ErrSessionClosed)
		}
		if message.Type == messages.StreamTypeError {
			if err := responseError(message); err != nil {
				return messages.Message{}, err
			}
			continue
		}
		deltas = append(deltas, message)
		if message.Type == messages.StreamTypeMessageEnd {
			return messages.ReconstructModelMessageFromDeltas(deltas), nil
		}
	}
}

func responseError(message messages.StreamMessage) error {
	value, _ := message.Value.(*messages.ErrorValue)
	if value != nil && value.IsNonTerminal() {
		return nil
	}
	if value != nil && value.Err != nil {
		return value.Err
	}
	if value == nil || strings.TrimSpace(value.Message) == "" {
		return sessionturns.ErrSessionResponse
	}
	return errors.New(value.Message)
}
