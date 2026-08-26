package messages

import (
	"context"
	"sync/atomic"
)

// BufferWriteStatus identifies the observable outcome of a TypedBuffer write.
type BufferWriteStatus string

const (
	BufferWriteSucceeded  BufferWriteStatus = "succeeded"
	BufferWriteCancelled  BufferWriteStatus = "cancelled"
	BufferWriteTimedOut   BufferWriteStatus = "timed_out"
	BufferWriteBufferFull BufferWriteStatus = "buffer_full"
)

// BufferWriteOutcome is the typed result returned by WriteContext.
type BufferWriteOutcome struct {
	Status BufferWriteStatus
	Err    error
}

// OK reports whether the write was delivered to the buffer.
func (o BufferWriteOutcome) OK() bool {
	return o.Status == BufferWriteSucceeded
}

// TypedBuffer is a generic, non-blocking, channel-based message buffer for participant communication.
//
// Every TypedBuffer tracks a cumulative count of buffer-full drops internally,
// so dropped writes are always observable even when no OnDrop callback was
// registered. The counter is durable for the lifetime of the buffer and safe
// for concurrent use.
type TypedBuffer[T any] struct {
	ch     chan T
	onDrop func(T)
	drops  atomic.Int64
}

func NewTypedBuffer[T any](capacity int) *TypedBuffer[T] {
	if capacity <= 0 {
		capacity = 64
	}
	return &TypedBuffer[T]{ch: make(chan T, capacity)}
}

// SetOnDrop registers a callback that fires when Write drops a message because
// the buffer is full. The callback receives the dropped message so observers
// can log its kind. It is not invoked when Write returns false due to context
// cancellation. Only the buffer-full path calls fn, so there is no allocation
// overhead on the success path.
func (b *TypedBuffer[T]) SetOnDrop(fn func(T)) {
	b.onDrop = fn
}

// Drops returns the cumulative number of messages dropped because the buffer
// was full, counted since construction. The count only grows: succeeded,
// cancelled, and timed-out writes never increment it.
func (b *TypedBuffer[T]) Drops() int64 {
	return b.drops.Load()
}

// Write sends data into the buffer. Context-aware: returns false if ctx is cancelled or buffer is full. Non-blocking when buffer has space.
// It is retained for compatibility with existing bool-based callers. New
// callers that need to distinguish cancellation, timeout, and buffer-full
// outcomes should use WriteContext.
func (b *TypedBuffer[T]) Write(ctx context.Context, data T) bool {
	return b.WriteContext(ctx, data).OK()
}

// WriteContext sends data into the buffer and returns a typed write outcome.
// It is non-blocking when the buffer is full.
func (b *TypedBuffer[T]) WriteContext(ctx context.Context, data T) BufferWriteOutcome {
	select {
	case <-ctx.Done():
		return bufferWriteContextOutcome(ctx)
	default:
	}
	select {
	case b.ch <- data:
		return BufferWriteOutcome{Status: BufferWriteSucceeded}
	case <-ctx.Done():
		return bufferWriteContextOutcome(ctx)
	default:
		// Count the drop before invoking the observer so the callback
		// reports the cumulative count including this drop: the counter is
		// the durable evidence, the callback is optional.
		b.drops.Add(1)
		if b.onDrop != nil {
			b.onDrop(data)
		}
		return BufferWriteOutcome{Status: BufferWriteBufferFull}
	}
}

func bufferWriteContextOutcome(ctx context.Context) BufferWriteOutcome {
	err := ctx.Err()
	if err == context.DeadlineExceeded {
		return BufferWriteOutcome{Status: BufferWriteTimedOut, Err: err}
	}
	return BufferWriteOutcome{Status: BufferWriteCancelled, Err: err}
}

// Read polls the buffer for data. Non-blocking: returns zero value, false if empty.
func (b *TypedBuffer[T]) Read() (T, bool) {
	select {
	case data := <-b.ch:
		return data, true
	default:
		var zero T
		return zero, false
	}
}

// ReadBlocking waits until data is available or the done channel is closed.
func (b *TypedBuffer[T]) ReadBlocking(done <-chan struct{}) (T, bool) {
	select {
	case data := <-b.ch:
		return data, true
	case <-done:
		var zero T
		return zero, false
	}
}

// ReadBlockingContext waits until data is available or ctx is cancelled.
// It is retained for compatibility with existing bool-based callers. New
// callers that need to inspect cancellation or timeout causes should use
// ReadContext.
func (b *TypedBuffer[T]) ReadBlockingContext(ctx context.Context) (T, bool) {
	return b.ReadBlocking(ctx.Done())
}

// ReadContext waits until data is available or ctx is cancelled.
// The caller owns ctx and therefore controls cancellation and timeout behavior.
// When ctx is cancelled before data is available, ReadContext returns the zero
// value for T and an error matching ctx.Err().
func (b *TypedBuffer[T]) ReadContext(ctx context.Context) (T, error) {
	select {
	case data := <-b.ch:
		return data, nil
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

// HasData returns true if the buffer has pending data.
func (b *TypedBuffer[T]) HasData() bool {
	return len(b.ch) > 0
}

// Len returns the number of items currently in the buffer.
func (b *TypedBuffer[T]) Len() int {
	return len(b.ch)
}

// Cap returns the buffer capacity.
func (b *TypedBuffer[T]) Cap() int {
	return cap(b.ch)
}

// Chan returns the underlying channel for use with select statements.
func (b *TypedBuffer[T]) Chan() <-chan T {
	return b.ch
}
