package service

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type completeMessageSender interface {
	SendMessage(context.Context, messages.Message) bool
}

type completeMessageSenderWithoutResponse interface {
	SendMessageWithoutResponse(context.Context, messages.Message) bool
}

func completeMessageCapabilities(session messages.Session) (complete, withoutResponse bool) {
	if capabilities, ok := session.(interface {
		SupportsCompleteMessages() bool
		SupportsCompleteMessagesWithoutResponse() bool
	}); ok {
		return capabilities.SupportsCompleteMessages(), capabilities.SupportsCompleteMessagesWithoutResponse()
	}
	_, complete = session.(completeMessageSender)
	_, withoutResponse = session.(completeMessageSenderWithoutResponse)
	return complete, withoutResponse
}

func isTerminalErrorMessage(msg messages.StreamMessage) bool {
	if msg.Type != messages.StreamTypeError {
		return false
	}
	value, ok := msg.Value.(*messages.ErrorValue)
	return !ok || value.IsTerminal()
}

var _ messages.SessionInferencer = (*AdmissionInferencer)(nil)
var _ messages.Session = (*AdmissionSession)(nil)
