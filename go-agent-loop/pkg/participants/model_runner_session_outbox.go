package participants

import (
	"context"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// sessionOutbox is the session goroutine's ordered path to DeltaOutbox.
//
// A delta that [messages.MustDeliver] waits for outbox capacity. Waiting on
// the session goroutine would stall barge-in, which is decided on that same
// goroutine, for as long as the delta consumer is stalled. So a must-deliver
// delta that meets a full outbox is queued and a pump goroutine waits for
// capacity instead. While the queue is non-empty every later delta joins it,
// so deltas reach DeltaOutbox in the order the session produced them.
//
// The queue is bounded for ordinary deltas: while it already holds as many
// deltas as the outbox capacity, a new ordinary delta is shed and counted in
// DeltaOutbox's drops, the same overload policy as a direct write to a full
// outbox. Must-deliver deltas are never shed; they are lifecycle boundaries,
// tool calls and errors, so their count is bounded by the response itself,
// and the pump gives up on them when the session context ends.
type sessionOutbox struct {
	out *messages.TypedBuffer[messages.StreamMessage]

	mu sync.Mutex
	// pending holds deltas not yet written; while pumping, its head is the
	// delta the pump is writing.
	pending []messages.StreamMessage
	// drained is non-nil while the pump runs and is closed when it exits.
	drained chan struct{}
}

// sessionDeltas returns the session goroutine's outbox writer, binding it to
// DeltaOutbox on first use. Only the session goroutine calls it.
func (r *ModelRunner) sessionDeltas() *sessionOutbox {
	if r.sessionOut == nil || r.sessionOut.out != r.DeltaOutbox {
		r.sessionOut = &sessionOutbox{out: r.DeltaOutbox}
	}
	return r.sessionOut
}

// writeSessionDelta writes one session delta to DeltaOutbox without blocking
// the session goroutine on a stalled consumer.
func (r *ModelRunner) writeSessionDelta(ctx context.Context, msg messages.StreamMessage) {
	r.sessionDeltas().write(ctx, msg)
}

func (q *sessionOutbox) write(ctx context.Context, msg messages.StreamMessage) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.drained == nil {
		// The session goroutine is the outbox's only writer, so spare
		// capacity cannot be taken before this write lands.
		if q.out.Len() < q.out.Cap() || !messages.MustDeliver(msg) {
			messages.WriteStreamDelta(ctx, q.out, msg)
			return
		}
		q.pending = append(q.pending, msg)
		q.drained = make(chan struct{})
		go q.pump(ctx, q.drained)
		return
	}
	if !messages.MustDeliver(msg) && len(q.pending) >= q.out.Cap() {
		q.out.Shed(msg)
		return
	}
	q.pending = append(q.pending, msg)
}

// pump writes queued deltas in order until the queue is empty. Every queued
// delta waits for capacity until ctx ends: ordinary deltas were already
// admitted within the queue bound, so overload is shed at admission, not here.
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
		msg := q.pending[0]
		q.mu.Unlock()

		q.out.WriteWaitContext(ctx, msg)

		q.mu.Lock()
		q.pending[0] = messages.StreamMessage{}
		q.pending = q.pending[1:]
		q.mu.Unlock()
	}
}

// flush waits until every queued delta has been written or ctx ends. The
// session calls it before a terminal write and before it returns, so nothing
// it produced is reordered behind that write or outlives the session.
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
	}
}
