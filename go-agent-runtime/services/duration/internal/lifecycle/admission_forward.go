package lifecycle

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func (s *admittedSession) forward(ctx context.Context) {
	source := s.inner.Receive()
	sourceCh := source.Chan()
	open := true
	gateDone := s.gate.done
	for {
		select {
		case <-s.inner.Done():
			s.finishForward(source, open, true)
			return
		case <-ctx.Done():
			s.finishForward(source, open, false)
			return
		case <-gateDone:
			open, gateDone = false, nil
		case msg, ok := <-sourceCh:
			if !ok {
				s.doneOnce.Do(func() { close(s.done) })
				return
			}
			open = s.forwardMessage(open, msg)
		}
	}
}

func (s *admittedSession) finishForward(source *messages.TypedBuffer[messages.StreamMessage], open, drain bool) {
	if drain {
		s.drainSource(source, open)
	}
	s.waitForCloseCompletion()
	s.doneOnce.Do(func() { close(s.done) })
}

func (s *admittedSession) forwardMessage(open bool, msg messages.StreamMessage) bool {
	s.observeProviderMessage(msg)
	if open && s.gate.admit(s.receive, msg) {
		return true
	}
	if s.isShutdownMessage(msg) {
		_ = s.receive.Write(context.Background(), msg)
	}
	return false
}

func (s *admittedSession) waitForCloseCompletion() {
	s.closeMu.Lock()
	started := s.closeStarted
	done := s.closeFinished
	finished := false
	if started {
		select {
		case <-done:
			finished = true
		default:
		}
	}
	s.closeMu.Unlock()
	if started && !finished {
		<-done
	}
}

func (s *admittedSession) drainSource(source *messages.TypedBuffer[messages.StreamMessage], open bool) {
	for {
		msg, ok := source.Read()
		if !ok {
			return
		}
		if open {
			open = s.forwardMessage(open, msg)
			continue
		}
		s.observeProviderMessage(msg)
		if s.isShutdownMessage(msg) {
			_ = s.receive.Write(context.Background(), msg)
		}
	}
}
