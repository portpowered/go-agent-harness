package service

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

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
	s.receive.Write(ctx, msg)
}
