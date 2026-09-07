package composite

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const playbackQueueCapacity = 64

// frameInbox is a bounded, close-aware queue used by one playback child. It
// keeps provider frames FIFO while allowing a slow physical sink to apply
// backpressure to the single fan-out reader.
type frameInbox struct {
	frames chan audio.PCMFrame
	done   chan struct{}

	mu       sync.Mutex
	closed   bool
	terminal error
}

func newFrameInbox() *frameInbox {
	return &frameInbox{
		frames: make(chan audio.PCMFrame, playbackQueueCapacity),
		done:   make(chan struct{}),
	}
}

func (q *frameInbox) send(ctx context.Context, frame audio.PCMFrame) error {
	if q == nil {
		return io.ErrClosedPipe
	}
	if ctx == nil {
		return errors.New("frame inbox send context is required")
	}
	if err := q.closedError(); err != nil {
		return err
	}
	select {
	case q.frames <- frame:
		return nil
	case <-q.done:
		return q.closedError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *frameInbox) ReadFrame(ctx context.Context) (audio.PCMFrame, error) {
	if q == nil {
		return audio.PCMFrame{}, io.ErrClosedPipe
	}
	if ctx == nil {
		return audio.PCMFrame{}, errors.New("frame inbox read context is required")
	}
	for {
		select {
		case frame := <-q.frames:
			return frame, nil
		default:
		}
		if err := q.closedError(); err != nil {
			select {
			case frame := <-q.frames:
				return frame, nil
			default:
				return audio.PCMFrame{}, err
			}
		}
		select {
		case frame := <-q.frames:
			return frame, nil
		case <-q.done:
			// Drain any frames admitted immediately before closure before
			// returning the terminal error.
		case <-ctx.Done():
			return audio.PCMFrame{}, ctx.Err()
		}
	}
}

func (q *frameInbox) closeWithError(terminal error) {
	if q == nil {
		return
	}
	if terminal == nil {
		terminal = io.EOF
	}
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		q.terminal = terminal
		close(q.done)
	}
	q.mu.Unlock()
}

func (q *frameInbox) Close() error {
	q.closeWithError(audio.ErrSessionMediaClosed)
	return nil
}

func (q *frameInbox) closedError() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.closed {
		return nil
	}
	return q.terminal
}

func cleanPlaybackError(err error) bool {
	return err == nil || errors.Is(err, io.EOF) || errors.Is(err, audio.ErrSessionMediaClosed)
}

type playbackFailures struct {
	mu  sync.Mutex
	err error
}

func (f *playbackFailures) record(err error) bool {
	if f == nil || cleanPlaybackError(err) {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return false
	}
	f.err = err
	return true
}

func (f *playbackFailures) get() error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

func cloneFrame(frame audio.PCMFrame) audio.PCMFrame {
	copyOf := frame
	if frame.Samples != nil {
		copyOf.Samples = append([]int16(nil), frame.Samples...)
	}
	return copyOf
}
