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
			s.drainSource(source, admissionOpen)
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
			s.forwardMessage(msg, &admissionOpen)
		}
	}
}

func (s *AdmissionSession) forwardMessage(msg messages.StreamMessage, admissionOpen *bool) {
	s.observeProviderMessage(msg)
	if *admissionOpen {
		if s.admission.admit(s.receive, msg) {
			return
		}
		*admissionOpen = false
	}
	// Preserve a provider message already removed by this worker so the
	// service controller can classify it during its bounded drain.
	s.receive.Write(context.Background(), msg)
}
