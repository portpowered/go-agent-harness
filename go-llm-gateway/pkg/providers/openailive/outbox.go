package openailive

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// outboxHighWater bounds how far the read loop may run ahead of the reader of
// Receive before it waits: lossless backpressure, as the shared skeleton
// gives the other providers.
const outboxHighWater = 64

// outboxEntry is one queued stream message. A terminal entry is written with
// WriteTerminal, so it is never lost to a full receive buffer.
type outboxEntry struct {
	msg      messages.StreamMessage
	terminal bool
}

// The session's stream messages come from three producers: the read loop,
// the idle watcher and RESPONSE.CANCEL. Each maps its input under mu and
// appends the result to the outbox, in one order, without blocking. A single
// pump goroutine delivers the outbox to the receive buffer and mirrors segment
// audio onto RTC media. No producer holds mu while it waits for the reader.
//
// Once the close handshake starts (closing), the pump stops waiting for the
// reader: a message that does not fit is dropped and counted, so the read loop
// drains to session.closed even after the reader has stopped reading.

// emitLocked queues msgs. Callers hold mu.
func (s *liveSession) emitLocked(msgs ...messages.StreamMessage) {
	for _, msg := range msgs {
		s.enqueueLocked(outboxEntry{msg: msg})
	}
}

// emitTerminalLocked queues a terminal message after everything before it.
// Callers hold mu.
func (s *liveSession) emitTerminalLocked(msg messages.StreamMessage) {
	s.enqueueLocked(outboxEntry{msg: msg, terminal: true})
}

// enqueueLocked hands one entry to the pump. After the pump has drained and
// exited (the session ended), the entry is written at once without waiting,
// so a terminal record emitted late is never stranded. Callers hold mu.
func (s *liveSession) enqueueLocked(entry outboxEntry) {
	if s.pumpExited {
		s.deliverNow(entry)
		return
	}
	s.outbox = append(s.outbox, entry)
	s.backlog++
	s.wakePump()
}

func (s *liveSession) wakePump() {
	select {
	case s.pumpWake <- struct{}{}:
	default:
	}
}

// pump delivers the outbox in order until the session ends, then delivers
// whatever is still queued without waiting. closing ends when the close
// handshake starts.
func (s *liveSession) pump(closing context.Context) {
	for {
		s.mu.Lock()
		batch := s.outbox
		s.outbox = nil
		s.mu.Unlock()
		if len(batch) == 0 {
			select {
			case <-s.pumpWake:
				continue
			case <-s.base.Done():
				s.drainOutbox()
				return
			}
		}
		for _, entry := range batch {
			s.deliver(closing, entry)
			s.mu.Lock()
			s.backlog--
			s.mu.Unlock()
			s.signalDrained()
		}
	}
}

// deliver writes one entry: a terminal record always, an ordinary one with
// backpressure until closing ends, then only if it fits.
func (s *liveSession) deliver(closing context.Context, entry outboxEntry) {
	s.publishRTCMedia(entry.msg)
	if entry.terminal {
		s.base.WriteTerminal(entry.msg)
		return
	}
	if outcome := s.base.DeliverWait(closing, entry.msg); !outcome.OK() {
		// The close handshake started, or the session ended, while the reader
		// was behind: write only if it fits. A message that does not is
		// dropped and counted once, here.
		s.base.TryDeliver(entry.msg)
	}
}

// deliverNow writes one entry without waiting.
func (s *liveSession) deliverNow(entry outboxEntry) {
	s.publishRTCMedia(entry.msg)
	if entry.terminal {
		s.base.WriteTerminal(entry.msg)
		return
	}
	s.base.TryDeliver(entry.msg)
}

// drainOutbox delivers what is left after the session ended, without waiting.
func (s *liveSession) drainOutbox() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.outbox {
		s.deliverNow(entry)
	}
	s.outbox = nil
	s.backlog = 0
	s.pumpExited = true
	s.signalDrained()
}

func (s *liveSession) signalDrained() {
	select {
	case s.drained <- struct{}{}:
	default:
	}
}

// awaitBacklog waits until at most limit messages are queued, the close
// handshake starts (when stopOnClose is set) or the session ends.
func (s *liveSession) awaitBacklog(limit int, stopOnClose bool) {
	closing := s.closing
	if !stopOnClose {
		closing = nil
	}
	for {
		s.mu.Lock()
		n := s.backlog
		s.mu.Unlock()
		if n <= limit {
			return
		}
		select {
		case <-s.drained:
		case <-closing:
			return
		case <-s.base.Done():
			return
		}
	}
}
