package audioinput

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

const readerFrameBytes = audio.FrameSize * 2

// ValidateInput rejects malformed sources before a session or provider side
// effect. Injected sources may omit Path because it is diagnostic metadata.
func ValidateInput(input Input) error {
	if input.DevicePresent {
		return &Error{Kind: KindConflict, Path: input.Path, Err: ErrConflict}
	}
	if input.SourceSampleRate < 0 || input.ProviderSampleRate < 0 {
		return &Error{Kind: KindFormat, Path: input.Path, Err: fmt.Errorf("sample rates must not be negative")}
	}
	count := 0
	if input.Source != nil {
		count++
	}
	if input.Reader != nil {
		count++
	}
	if input.Buffer != nil {
		count++
	}
	if count > 1 {
		return &Error{Kind: KindConflict, Path: input.Path, Err: fmt.Errorf("multiple audio sources supplied")}
	}
	if count == 0 {
		return &Error{Kind: KindEmpty, Path: input.Path, Err: ErrEmpty}
	}
	if input.Buffer != nil && len(input.Buffer) == 0 {
		return NewEmptyError(input.Path)
	}
	return nil
}

// NewBufferSource decodes a caller-owned little-endian mono PCM16 buffer into
// a finite frame source. The source is independent of later buffer mutation.
func NewBufferSource(pcm []byte) (audio.AudioSource, error) {
	if len(pcm) == 0 {
		return nil, NewEmptyError("")
	}
	if len(pcm)%2 != 0 {
		return nil, fmt.Errorf("%w: %d bytes", ErrPCM16Truncated, len(pcm))
	}
	samples, err := codec.DecodePCM16WithLimit(pcm, len(pcm))
	if err != nil {
		return nil, &Error{Kind: KindFormat, Err: err}
	}
	return audio.NewSliceSource(samples), nil
}

// NewReaderSource adapts a host-provided raw PCM16 reader. It never closes a
// caller-owned reader unless CloseOnCancel is explicitly true.
func NewReaderSource(reader io.Reader, closeOnCancel bool) (*ReaderSource, error) {
	if reader == nil {
		return nil, &Error{Kind: KindUnreadable, Err: audio.ErrNilStream}
	}
	return &ReaderSource{reader: reader, closeOnCancel: closeOnCancel}, nil
}

// NewWAVSource validates an already-open WAV stream without filesystem access.
// The caller transfers ownership of reader only after this succeeds.
func NewWAVSource(path string, reader ReadSeekCloser) (audio.AudioSource, error) {
	source, err := audio.NewWAVSource(path, reader)
	if err != nil {
		// Keep the legacy audio.FormatError identity at the public boundary.
		// CLI callers use errors.Is(err, audio.ErrUnsupportedFormat) for both
		// unsupported extensions and malformed .wav containers.
		formatErr := &audio.FormatError{Path: path, Extension: ".wav", Format: "wav", Reason: err.Error(), Err: err}
		return nil, &Error{Kind: KindFormat, Path: path, Err: errors.Join(ErrFormat, formatErr)}
	}
	return source, nil
}

// ReadPCM reads a source using the canonical audio frame contract and returns
// the source rate when it exposes one. It does not retain the source after the
// call and intentionally leaves ownership/Close to the caller.
func ReadPCM(ctx context.Context, source audio.AudioSource, fallbackRate int) ([]byte, int, error) {
	if source == nil {
		return nil, 0, &Error{Kind: KindUnreadable, Err: audio.ErrNilStream}
	}
	if fallbackRate <= 0 {
		fallbackRate = audio.SampleRate
	}
	rate := fallbackRate
	if rated, ok := source.(interface{ SampleRate() int }); ok && rated.SampleRate() > 0 {
		rate = rated.SampleRate()
	}
	if samples, ok := source.(audio.SampleSource); ok {
		return readSamplesPCM(ctx, samples, rate)
	}
	frame := make([]int16, audio.FrameSize)
	var encoded bytes.Buffer
	for {
		clear(frame)
		err := source.ReadFrame(ctx, frame)
		if errors.Is(err, io.EOF) {
			break
		}
		if errors.Is(err, audio.ErrEndOfTurn) {
			break
		}
		if err != nil {
			return nil, 0, &Error{Kind: KindRead, Err: err}
		}
		payload := make([]byte, readerFrameBytes)
		if err := codec.EncodePCM16Into(payload, frame); err != nil {
			return nil, 0, &Error{Kind: KindFormat, Err: err}
		}
		_, _ = encoded.Write(payload)
	}
	if encoded.Len() == 0 {
		return nil, rate, NewEmptyError("")
	}
	return encoded.Bytes(), rate, nil
}

func readSamplesPCM(ctx context.Context, source audio.SampleSource, rate int) ([]byte, int, error) {
	const sampleChunk = audio.FrameSize
	samples := make([]int16, sampleChunk)
	var encoded bytes.Buffer
	for {
		clear(samples)
		count, err := source.ReadSamples(ctx, samples)
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, 0, &Error{Kind: KindRead, Err: err}
		}
		if count > 0 {
			payload := make([]byte, count*2)
			if encodeErr := codec.EncodePCM16Into(payload, samples[:count]); encodeErr != nil {
				return nil, 0, &Error{Kind: KindFormat, Err: encodeErr}
			}
			_, _ = encoded.Write(payload)
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || count == 0 {
			break
		}
	}
	if encoded.Len() == 0 {
		return nil, rate, NewEmptyError("")
	}
	return encoded.Bytes(), rate, nil
}

// ReaderSource is a cancellation-aware raw PCM16 frame source for embedders.
type ReaderSource struct {
	reader        io.Reader
	closeOnCancel bool
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
		if r.closeOnCancel {
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
