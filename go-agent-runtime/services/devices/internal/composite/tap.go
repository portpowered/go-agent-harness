package composite

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const outputTapQueueCapacity = 64

type outputTapOverflowError struct{}

func (outputTapOverflowError) Error() string { return "device playback output tap queue is full" }

type outputTapFrame struct {
	rate    int
	samples []int16
}

// outputTap decouples physical playback admission from file I/O with a
// bounded queue. Its observer callback is synchronous from the device worker,
// while the sink itself is written by one ordered worker.
type outputTap struct {
	sink       audio.AudioSink
	workerCtx  context.Context
	workerStop context.CancelFunc
	frames     chan outputTapFrame
	stop       chan struct{}
	done       chan struct{}
	noSend     chan struct{}

	mu        sync.Mutex
	closed    bool
	senders   int
	workerErr error
	closeOnce sync.Once
	closeErr  error
}

func newOutputTap(ctx context.Context, sink audio.AudioSink) (*outputTap, error) {
	if ctx == nil {
		return nil, errors.New("device playback output tap context is required")
	}
	workerCtx, workerStop := context.WithCancel(context.WithoutCancel(ctx))
	tap := &outputTap{
		sink:       sink,
		workerCtx:  workerCtx,
		workerStop: workerStop,
		frames:     make(chan outputTapFrame, outputTapQueueCapacity),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
		noSend:     make(chan struct{}),
	}
	go tap.run()
	return tap, nil
}

func (t *outputTap) run() {
	defer close(t.done)
	for frame := range t.frames {
		if err := writeOutputSamples(t.workerCtx, t.sink, frame.rate, frame.samples); err != nil {
			t.mu.Lock()
			t.workerErr = err
			t.mu.Unlock()
			return
		}
	}
}

func (t *outputTap) Observe(ctx context.Context, rate int, samples []int16) error {
	if t == nil {
		return errors.New("device playback output tap is unavailable")
	}
	if ctx == nil {
		return errors.New("device playback output tap context is required")
	}
	if len(samples) == 0 {
		return nil
	}
	if err := t.workerFailure(); err != nil {
		// The file tap is an optional observation path. Once its worker has
		// failed, physical playback must continue while Close reports the
		// incomplete recording to the owning lifecycle.
		return nil
	}
	if err := t.beginSend(); err != nil {
		return err
	}
	defer t.endSend()
	frame := outputTapFrame{rate: rate, samples: append([]int16(nil), samples...)}
	select {
	case t.frames <- frame:
		return nil
	default:
		t.mu.Lock()
		if t.workerErr == nil {
			t.workerErr = outputTapOverflowError{}
		}
		t.mu.Unlock()
		// Overflow makes the optional recording incomplete, but must not stop
		// the physical playback worker. Close returns the latched error.
		return nil
	case <-t.stop:
		return errors.New("device playback output tap is closed")
	case <-t.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *outputTap) beginSend() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return errors.New("device playback output tap is closed")
	}
	t.senders++
	return nil
}

func (t *outputTap) endSend() {
	t.mu.Lock()
	t.senders--
	if t.closed && t.senders == 0 {
		close(t.noSend)
	}
	t.mu.Unlock()
}

func (t *outputTap) workerFailure() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.workerErr
}

func (t *outputTap) Close() error {
	if t == nil {
		return nil
	}
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.closed = true
		close(t.stop)
		waitSenders := t.senders > 0
		if !waitSenders {
			close(t.noSend)
		}
		t.mu.Unlock()
		if waitSenders {
			<-t.noSend
		}
		close(t.frames)
		<-t.done
		t.workerStop()
		t.mu.Lock()
		workerErr := t.workerErr
		t.mu.Unlock()
		t.closeErr = errors.Join(workerErr, t.sink.Close())
	})
	return t.closeErr
}

func writeOutputSamples(ctx context.Context, sink audio.AudioSink, rate int, samples []int16) error {
	if sink == nil {
		return fmt.Errorf("output tap sink is nil at %d Hz", rate)
	}
	if writer, ok := sink.(interface {
		WriteSamplesAtRate(context.Context, int, []int16) error
	}); ok {
		return writer.WriteSamplesAtRate(ctx, rate, samples)
	}
	if writer, ok := sink.(audio.SampleSink); ok {
		return writer.WriteSamples(ctx, samples)
	}
	if len(samples) != audio.FrameSize {
		return fmt.Errorf("output tap sink does not support %d-sample device chunk at %d Hz", len(samples), rate)
	}
	return sink.WriteFrame(ctx, samples)
}
