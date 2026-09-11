package stream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

// ReaderSource is a cancellation-aware raw PCM16 frame source for embedders.
type ReaderSource struct {
	reader        io.Reader
	closeOnCancel bool
	CloseOnCancel bool
	mu            sync.RWMutex
	ctx           context.Context
	closeOnce     sync.Once
	closeErr      error
	closed        bool
}

func (r *ReaderSource) BindContext(ctx context.Context) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.ctx = ctx
	r.mu.Unlock()
}

// Read exposes the cancellation-aware reader port for hosts that need to
// hand the same reader to another canonical audio adapter.
func (r *ReaderSource) Read(destination []byte) (int, error) {
	if r == nil {
		return 0, io.EOF
	}
	r.mu.RLock()
	ctx := r.ctx
	r.mu.RUnlock()
	if ctx == nil {
		ctx = context.Background()
	}
	return readOnceContext(ctx, r.reader, r, destination)
}

func (r *ReaderSource) ReadFrame(ctx context.Context, destination []int16) error {
	if len(destination) != audio.FrameSize {
		return &audio.FrameSizeError{Operation: "read", Got: len(destination), Want: audio.FrameSize}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.RLock()
	bound := r.ctx
	closed := r.closed
	r.mu.RUnlock()
	if closed {
		return &Error{Kind: KindRead, Err: errors.New("reader is closed")}
	}
	if bound != nil {
		ctx = bound
	}
	encoded := make([]byte, readerFrameBytes)
	count, err := readFullContext(ctx, r.reader, r, encoded)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return err
	}
	if count == 0 {
		return io.EOF
	}
	if count%2 != 0 {
		return fmt.Errorf("%w: %d bytes", ErrPCM16Truncated, count)
	}
	clear(destination)
	if err := codec.DecodePCM16Into(destination[:count/2], encoded[:count]); err != nil {
		return err
	}
	return nil
}

// ReadSamples preserves a finite reader's short tail without the zero padding
// required by the frame-oriented AudioSource contract.
func (r *ReaderSource) ReadSamples(ctx context.Context, destination []int16) (int, error) {
	if r == nil {
		return 0, io.EOF
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.RLock()
	bound, closed := r.ctx, r.closed
	r.mu.RUnlock()
	if closed {
		return 0, &Error{Kind: KindRead, Err: errors.New("reader is closed")}
	}
	if bound != nil {
		ctx = bound
	}
	if len(destination) == 0 {
		return 0, nil
	}
	encoded := make([]byte, len(destination)*2)
	count, err := readFullContext(ctx, r.reader, r, encoded)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return 0, err
	}
	if count == 0 {
		return 0, io.EOF
	}
	if count%2 != 0 {
		return 0, fmt.Errorf("%w: %d bytes", ErrPCM16Truncated, count)
	}
	clear(destination)
	if decodeErr := codec.DecodePCM16Into(destination[:count/2], encoded[:count]); decodeErr != nil {
		return 0, decodeErr
	}
	return count / 2, nil
}

func (r *ReaderSource) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		r.mu.Unlock()
		if r.closeOnCancel || r.CloseOnCancel {
			if closer, ok := r.reader.(io.Closer); ok {
				r.closeErr = closer.Close()
			}
		}
	})
	return r.closeErr
}

type contextReader interface {
	ReadContext(context.Context, []byte) (int, error)
}

type deadlineReader interface {
	SetReadDeadline(time.Time) error
}

type closeReader interface{ Close() error }

func readFullContext(ctx context.Context, reader io.Reader, owner *ReaderSource, destination []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if contextual, ok := reader.(contextReader); ok {
		return readFullWith(ctx, func(p []byte) (int, error) { return contextual.ReadContext(ctx, p) }, destination)
	}
	switch reader.(type) {
	case *bytes.Reader, *bytes.Buffer, *strings.Reader:
		return io.ReadFull(reader, destination)
	}
	if deadliner, ok := reader.(deadlineReader); ok {
		return readFullWithDeadline(ctx, deadliner, reader, destination, owner.closeOnCancel)
	}
	if owner.closeOnCancel {
		if closer, ok := reader.(closeReader); ok {
			result := make(chan readResult, 1)
			go func() { n, err := io.ReadFull(reader, destination); result <- readResult{n, err} }()
			select {
			case got := <-result:
				return got.n, got.err
			case <-ctx.Done():
				_ = closer.Close()
				got := <-result
				return got.n, ctx.Err()
			}
		}
	}
	return 0, fmt.Errorf("%w: reader must implement ReadContext, SetReadDeadline, or be close-on-cancel", ErrUninterruptible)
}

func readOnceContext(ctx context.Context, reader io.Reader, owner *ReaderSource, destination []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if contextual, ok := reader.(contextReader); ok {
		return contextual.ReadContext(ctx, destination)
	}
	switch reader.(type) {
	case *bytes.Reader, *bytes.Buffer, *strings.Reader:
		return reader.Read(destination)
	}
	if deadliner, ok := reader.(deadlineReader); ok {
		return readWithDeadline(ctx, deadliner, reader, destination, owner.closeOnCancel)
	}
	if owner.closeOnCancel {
		if closer, ok := reader.(closeReader); ok {
			result := make(chan readResult, 1)
			go func() { n, err := reader.Read(destination); result <- readResult{n, err} }()
			select {
			case got := <-result:
				return got.n, got.err
			case <-ctx.Done():
				_ = closer.Close()
				<-result
				return 0, ctx.Err()
			}
		}
	}
	return 0, fmt.Errorf("%w: reader must implement ReadContext, SetReadDeadline, or be close-on-cancel", ErrUninterruptible)
}

type readResult struct {
	n   int
	err error
}

func readFullWith(ctx context.Context, read func([]byte) (int, error), destination []byte) (int, error) {
	count := 0
	for count < len(destination) {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		n, err := read(destination[count:])
		count += n
		if err != nil {
			return count, err
		}
		if n == 0 {
			return count, io.ErrNoProgress
		}
	}
	return count, nil
}

func readFullWithDeadline(ctx context.Context, deadliner deadlineReader, reader io.Reader, destination []byte, closeOnCancel bool) (int, error) {
	const deadline = 250 * time.Millisecond
	for {
		if err := deadliner.SetReadDeadline(time.Now().Add(deadline)); err != nil {
			if !closeOnCancel {
				return 0, errors.Join(ErrUninterruptible, fmt.Errorf("reader deadline setup failed: %w", err))
			}
			if _, ok := reader.(io.Closer); !ok {
				return 0, errors.Join(ErrUninterruptible, fmt.Errorf("reader deadline setup failed: %w", err))
			}
			return readFullWithClose(ctx, reader, destination)
		}
		count, err := io.ReadFull(reader, destination)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return count, ctxErr
		}
		if err != nil && isTimeout(err) && count == 0 {
			continue
		}
		_ = deadliner.SetReadDeadline(time.Time{})
		return count, err
	}
}

func readWithDeadline(ctx context.Context, deadliner deadlineReader, reader io.Reader, destination []byte, closeOnCancel bool) (int, error) {
	const deadline = 250 * time.Millisecond
	for {
		if err := deadliner.SetReadDeadline(time.Now().Add(deadline)); err != nil {
			if !closeOnCancel {
				return 0, errors.Join(ErrUninterruptible, fmt.Errorf("reader deadline setup failed: %w", err))
			}
			if _, ok := reader.(io.Closer); !ok {
				return 0, errors.Join(ErrUninterruptible, fmt.Errorf("reader deadline setup failed: %w", err))
			}
			return readOnceWithClose(ctx, reader, destination)
		}
		n, err := reader.Read(destination)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return n, ctxErr
		}
		if err != nil && isTimeout(err) && n == 0 {
			continue
		}
		_ = deadliner.SetReadDeadline(time.Time{})
		return n, err
	}
}

func readFullWithClose(ctx context.Context, reader io.Reader, destination []byte) (int, error) {
	closer, ok := reader.(io.Closer)
	if !ok {
		return 0, fmt.Errorf("%w: reader deadline setup failed", ErrUninterruptible)
	}
	result := make(chan readResult, 1)
	go func() { n, err := io.ReadFull(reader, destination); result <- readResult{n, err} }()
	select {
	case got := <-result:
		return got.n, got.err
	case <-ctx.Done():
		_ = closer.Close()
		got := <-result
		return got.n, ctx.Err()
	}
}

func readOnceWithClose(ctx context.Context, reader io.Reader, destination []byte) (int, error) {
	closer, ok := reader.(io.Closer)
	if !ok {
		return 0, fmt.Errorf("%w: reader deadline setup failed", ErrUninterruptible)
	}
	result := make(chan readResult, 1)
	go func() { n, err := reader.Read(destination); result <- readResult{n, err} }()
	select {
	case got := <-result:
		return got.n, got.err
	case <-ctx.Done():
		_ = closer.Close()
		got := <-result
		return got.n, ctx.Err()
	}
}

func isTimeout(err error) bool {
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}
