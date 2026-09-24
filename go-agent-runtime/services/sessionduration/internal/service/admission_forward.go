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

func writeAdmittedMessage(receive *messages.TypedBuffer[messages.StreamMessage], ctx context.Context, msg messages.StreamMessage) bool {
	if IsDurationShutdownMessage(msg) {
		return receive.WriteTerminal(msg)
	}
	return receive.Write(ctx, msg)
}

var _ messages.SessionInferencer = (*AdmissionInferencer)(nil)
var _ messages.Session = (*AdmissionSession)(nil)

func (s *AdmissionSession) forward(ctx context.Context) {
	source := s.inner.Receive()
	admissionDone := s.admission.done
	admissionOpen := true
	for {
		select {
		case <-s.inner.Done():
			s.drainSource(ctx, source, admissionOpen)
			s.closeDone()
			return
		case <-ctx.Done():
			s.closeDone()
			return
		case <-admissionDone:
			admissionOpen, admissionDone = false, nil
		case msg, ok := <-source.Chan():
			if !ok {
				s.closeDone()
				return
			}
			s.forwardMessage(ctx, msg, &admissionOpen)
		}
	}
}

func (s *AdmissionSession) drainSourceAfterClose() {
	if s == nil || s.inner == nil || s.inner.Receive() == nil {
		return
	}
	source := s.inner.Receive()
	for {
		msg, ok := source.Read()
		if !ok {
			return
		}
		s.observeProviderMessage(msg)
		if IsDurationForwardMessage(msg) {
			writeAdmittedMessage(s.receive, context.Background(), msg)
		}
	}
}

func (s *AdmissionSession) drainSource(ctx context.Context, source *messages.TypedBuffer[messages.StreamMessage], admissionOpen bool) {
	for {
		msg, ok := source.Read()
		if !ok {
			return
		}
		s.observeProviderMessage(msg)
		if admissionOpen {
			if s.admission.admit(ctx, s.receive, msg) {
				continue
			}
			admissionOpen = false
		}
		// The message was removed from the provider source before the admission
		// close became visible to this forwarding worker. Retain it for the
		// service controller to classify during the bounded drain; that boundary
		// rejects late nonterminal output without losing an already-read delta.
		writeAdmittedMessage(s.receive, ctx, msg)
	}
}

func (s *AdmissionSession) forwardMessage(ctx context.Context, msg messages.StreamMessage, admissionOpen *bool) {
	s.observeProviderMessage(msg)
	if *admissionOpen {
		if s.admission.admit(ctx, s.receive, msg) {
			return
		}
		*admissionOpen = false
	}
	// Preserve a provider message already removed by this worker so the
	// service controller can classify it during its bounded drain.
	writeAdmittedMessage(s.receive, ctx, msg)
}
