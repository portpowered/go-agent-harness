package participants

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ErrSessionDeltaOverflow ends a session whose delta consumer stayed stalled
// until the session outbox queue reached its bound. Match it with errors.Is
// on the error Run returns or on the terminal ERROR delta's cause.
var ErrSessionDeltaOverflow = errors.New("session delta consumer stalled: outbox queue full")

// maxPendingSessionDeltas bounds the session outbox queue. A response's
// must-deliver deltas (boundaries, tool-call chunks, errors) stay far below
// it; reaching it means the consumer has stopped reading.
const maxPendingSessionDeltas = 4096

// sessionDeltaOverflowClassification is the stream classification of the
// terminal ERROR published when the session outbox queue overflows.
const sessionDeltaOverflowClassification = "session_delta_overflow"

// sessionOutbox is the session goroutine's ordered path to DeltaOutbox.
//
// A delta that [messages.MustDeliver] waits for outbox capacity. Waiting on
// the session goroutine would stall barge-in, which is decided on that same
// goroutine, for as long as the delta consumer is stalled. So a must-deliver
// delta that meets a full outbox is queued and a pump goroutine waits for
// capacity instead. While the queue is non-empty every later delta joins it,
// so deltas reach DeltaOutbox in the order the session produced them. A
// terminal record takes the same path, so it never overtakes or evicts a
// queued must-deliver delta while the session context is live.
//
// Ordinary deltas are shed while the queue already holds as many deltas as
// the outbox capacity, counted in DeltaOutbox's drops like a direct write to
// a full outbox. Must-deliver deltas are never shed; the queue as a whole is
// bounded by maxPendingSessionDeltas. Reaching that bound means the consumer
// stopped reading: the outbox abandons the queue, and the session ends with
// ErrSessionDeltaOverflow (see overflowErr).
//
// Once the session context ends, the pump writes no further ordinary or
// must-deliver delta; a queued terminal record is still delivered with
// TypedBuffer.WriteTerminal, as before the queue existed.
type sessionOutbox struct {
	out   *messages.TypedBuffer[messages.StreamMessage]
	limit int

	mu sync.Mutex
	// pending holds deltas not yet written; while pumping, its head is the
	// delta the pump is writing.
	pending []sessionOutboxEntry
	// drained is non-nil while the pump runs and is closed when it exits.
	drained chan struct{}
	// abandon is closed on overflow to release a pump waiting for capacity.
	abandon  chan struct{}
	overflow bool
	// writing reports that the pump has taken pending[0] and is writing it.
	writing bool
}

type sessionOutboxEntry struct {
	msg      messages.StreamMessage
	terminal bool
}

// sessionDeltas returns the session goroutine's outbox writer, binding it to
// DeltaOutbox on first use. Only the session goroutine calls it.
func (r *ModelRunner) sessionDeltas() *sessionOutbox {
	if r.sessionOut == nil || r.sessionOut.out != r.DeltaOutbox {
		r.sessionOut = &sessionOutbox{out: r.DeltaOutbox, limit: maxPendingSessionDeltas, abandon: make(chan struct{})}
	}
	return r.sessionOut
}

// writeSessionDelta writes one session delta to DeltaOutbox without blocking
// the session goroutine on a stalled consumer.
func (r *ModelRunner) writeSessionDelta(ctx context.Context, msg messages.StreamMessage) {
	r.sessionDeltas().write(ctx, sessionOutboxEntry{msg: msg})
}

// writeSessionTerminal writes a terminal record behind every queued delta.
func (r *ModelRunner) writeSessionTerminal(ctx context.Context, msg messages.StreamMessage) {
	r.sessionDeltas().write(ctx, sessionOutboxEntry{msg: msg, terminal: true})
}

func (q *sessionOutbox) write(ctx context.Context, entry sessionOutboxEntry) {
	q.mu.Lock()
	if q.overflow {
		drained := q.drained
		q.mu.Unlock()
		q.writeAbandoned(entry, drained)
		return
	}
	defer q.mu.Unlock()
	mustDeliver := entry.terminal || messages.MustDeliver(entry.msg)
	if q.drained == nil {
		// The session goroutine is the outbox's only writer, so spare
		// capacity cannot be taken before this write lands.
		if q.out.Len() < q.out.Cap() || !mustDeliver {
			messages.WriteStreamDelta(ctx, q.out, entry.msg)
			return
		}
		q.pending = append(q.pending, entry)
		q.drained = make(chan struct{})
		go q.pump(ctx, q.drained)
		return
	}
	switch {
	case !mustDeliver && len(q.pending) >= q.out.Cap():
		q.out.Shed(entry.msg)
	case !entry.terminal && len(q.pending) >= q.limit:
		// A terminal record is admitted past the bound: the session publishes
		// at most one per failure, and it must follow what is queued.
		q.abandonLocked()
		q.out.Shed(entry.msg)
	default:
		q.pending = append(q.pending, entry)
	}
}

// abandonLocked drops the queue and releases the pump: the consumer stopped
// reading, so the session is about to end with ErrSessionDeltaOverflow.
func (q *sessionOutbox) abandonLocked() {
	q.overflow = true
	pending := q.pending
	if q.writing && len(pending) > 0 {
		// The pump owns the head it is writing: that write may still land,
		// and the pump accounts for it when released (see pumpEntry).
		pending = pending[1:]
	}
	for _, entry := range pending {
		q.out.Shed(entry.msg)
	}
	q.pending = nil
	close(q.abandon)
}

// writeAbandoned handles a write after overflow: only a terminal record is
// still delivered, evicting the oldest queued delta when the outbox is full.
// It first waits for a released pump to finish its last write, so that
// write never lands behind the terminal record. Called without q.mu.
func (q *sessionOutbox) writeAbandoned(entry sessionOutboxEntry, drained <-chan struct{}) {
	if !entry.terminal {
		q.out.Shed(entry.msg)
		return
	}
	if drained != nil {
		<-drained // the pump was released on overflow and exits without waiting
	}
	q.out.WriteTerminal(entry.msg)
}

// overflowErr reports ErrSessionDeltaOverflow once the queue overflowed.
func (q *sessionOutbox) overflowErr() error {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.overflow {
		return nil
	}
	return fmt.Errorf("%w (%d deltas queued)", ErrSessionDeltaOverflow, q.limit)
}

// pump writes queued deltas in order until the queue is empty. Every queued
// delta waits for capacity: ordinary deltas were already admitted within the
// queue bound, so overload is shed at admission, not here. Once ctx ends
// only terminal records are still written.
func (q *sessionOutbox) pump(ctx context.Context, drained chan struct{}) {
	defer close(drained)
	for {
		q.mu.Lock()
		if len(q.pending) == 0 {
			q.pending = nil
			q.drained = nil
			q.mu.Unlock()
			return
		}
		entry := q.pending[0]
		q.writing = true
		q.mu.Unlock()

		q.pumpEntry(ctx, entry)

		q.mu.Lock()
		q.writing = false
		if len(q.pending) > 0 {
			q.pending[0] = sessionOutboxEntry{}
			q.pending = q.pending[1:]
		}
		q.mu.Unlock()
	}
}

func (q *sessionOutbox) pumpEntry(ctx context.Context, entry sessionOutboxEntry) {
	if ctx.Err() == nil {
		outcome := q.out.WriteWaitContextOrDone(ctx, q.abandon, entry.msg)
		if outcome.OK() {
			return
		}
		if outcome.Status == messages.BufferWriteStopped && !entry.terminal {
			q.out.Shed(entry.msg) // abandoned on overflow before it was written
			return
		}
	}
	if entry.terminal {
		q.out.WriteTerminal(entry.msg)
	}
}

// flush waits until every queued delta has been written or ctx ends. Once ctx
// has ended the pump no longer waits for capacity, so flush also waits for
// it to exit: nothing the session produced is written after flush returns.
// The session calls it before Run returns.
func (q *sessionOutbox) flush(ctx context.Context) {
	if q == nil {
		return
	}
	q.mu.Lock()
	drained := q.drained
	q.mu.Unlock()
	if drained == nil {
		return
	}
	select {
	case <-drained:
	case <-ctx.Done():
		<-drained
	}
}
