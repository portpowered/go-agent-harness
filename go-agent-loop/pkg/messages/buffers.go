package messages

import "context"

// TypedBuffer is a generic, non-blocking, channel-based message buffer for participant communication.
type TypedBuffer[T any] struct {
	ch     chan T
	onDrop func()
}

func NewTypedBuffer[T any](capacity int) *TypedBuffer[T] {
	if capacity <= 0 {
		capacity = 64
	}
	return &TypedBuffer[T]{ch: make(chan T, capacity)}
}

// SetOnDrop registers a callback that fires when Write drops a message because
// the buffer is full. The callback is not invoked when Write returns false due
// to context cancellation. Only the buffer-full path calls fn, so there is no
// allocation overhead on the success path.
func (b *TypedBuffer[T]) SetOnDrop(fn func()) {
	b.onDrop = fn
}

// Write sends data into the buffer. Context-aware: returns false if ctx is cancelled or buffer is full. Non-blocking when buffer has space.
func (b *TypedBuffer[T]) Write(ctx context.Context, data T) bool {
	select {
	case <-ctx.Done():
		return false
	default:
	}
	select {
	case b.ch <- data:
		return true
	case <-ctx.Done():
		return false
	default:
		if b.onDrop != nil {
			b.onDrop()
		}
		return false
	}
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
