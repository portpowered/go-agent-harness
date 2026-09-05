package audio

import (
	"context"
	"errors"
	"io"
	"sync"
)

var (
	ErrBufferFull    = errors.New("audio buffer capacity exhausted")
	ErrBufferClosed  = errors.New("audio buffer producer closed")
	ErrStaleEpoch    = errors.New("audio frame belongs to an invalidated epoch")
	ErrInvalidBuffer = errors.New("audio buffer capacity must be positive")
	ErrFrameTooLarge = errors.New("audio frame exceeds buffer sample capacity")
)

// BufferStats distinguishes admission, consumption and explicit invalidation.
// Samples are interleaved values, not frames per channel. No implicit dropping
// is permitted: a rejected submission returns a classified error to its owner.
type BufferStats struct {
	CapacityFrames, CapacitySamples                    int
	QueuedFrames, QueuedSamples                        int
	AdmittedSamples, ConsumedSamples, DiscardedSamples uint64
	RejectedFrames                                     uint64
	Epoch                                              uint64
	Closed                                             bool
}

// FrameProducer can only publish owned PCM into memory. It cannot invoke a
// device, provider, observer or application callback. Submit copies Samples;
// ownership of the caller's frame is never transferred implicitly.
type FrameProducer struct{ q *frameBuffer }

// FrameConsumer is a directional buffer capability. Receive transfers the
// queued sample storage to its caller; there is exactly one logical consumer.
type FrameConsumer struct{ q *frameBuffer }

// BufferControl is retained by the audio runtime, never by device callbacks.
// Invalidation is independent of media capacity and wakes blocked producers.
type BufferControl struct{ q *frameBuffer }

type frameBuffer struct {
	mu                              sync.Mutex
	frames                          []PCMFrame
	head, size, samples, maxSamples int
	changed                         chan struct{}
	stats                           BufferStats
}

// NewFrameBuffer creates bounded memory-only endpoints. Both frame and sample
// ceilings apply, so zero-sample response boundaries cannot grow without bound.
func NewFrameBuffer(maxFrames, maxSamples int) (FrameProducer, FrameConsumer, BufferControl, error) {
	if maxFrames <= 0 || maxSamples <= 0 {
		return FrameProducer{}, FrameConsumer{}, BufferControl{}, ErrInvalidBuffer
	}
	q := &frameBuffer{frames: make([]PCMFrame, maxFrames), maxSamples: maxSamples, changed: make(chan struct{}), stats: BufferStats{CapacityFrames: maxFrames, CapacitySamples: maxSamples}}
	return FrameProducer{q}, FrameConsumer{q}, BufferControl{q}, nil
}

func (p FrameProducer) TrySubmit(frame PCMFrame) error {
	if p.q == nil {
		return ErrBufferClosed
	}
	p.q.mu.Lock()
	defer p.q.mu.Unlock()
	return p.q.submitLocked(frame)
}

// Submit waits for capacity or cancellation. Each wake revalidates the epoch,
// preventing a producer stalled before interruption from reintroducing audio.
func (p FrameProducer) Submit(ctx context.Context, frame PCMFrame) error {
	if p.q == nil {
		return ErrBufferClosed
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		p.q.mu.Lock()
		err := p.q.canSubmitLocked(frame)
		if err != ErrBufferFull {
			if err == nil {
				err = p.q.submitLocked(frame)
			} else {
				p.q.stats.RejectedFrames++
			}
			p.q.mu.Unlock()
			return err
		}
		changed := p.q.changed
		p.q.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (q *frameBuffer) canSubmitLocked(frame PCMFrame) error {
	if q.stats.Closed {
		return ErrBufferClosed
	}
	if frame.Epoch != q.stats.Epoch {
		return ErrStaleEpoch
	}
	if len(frame.Samples) > q.maxSamples {
		return ErrFrameTooLarge
	}
	if q.size == len(q.frames) || len(frame.Samples) > q.maxSamples-q.samples {
		return ErrBufferFull
	}
	return nil
}

func (q *frameBuffer) submitLocked(frame PCMFrame) error {
	if err := q.canSubmitLocked(frame); err != nil {
		q.stats.RejectedFrames++
		return err
	}
	frame.Samples = append([]int16(nil), frame.Samples...)
	q.frames[(q.head+q.size)%len(q.frames)] = frame
	q.size++
	q.samples += len(frame.Samples)
	q.stats.AdmittedSamples += uint64(len(frame.Samples))
	q.notifyLocked()
	return nil
}

// TryReceive never blocks a loop tick. EOF means the producer is closed and
// drained; (zero,false,nil) means temporarily empty.
func (c FrameConsumer) TryReceive() (PCMFrame, bool, error) {
	if c.q == nil {
		return PCMFrame{}, false, io.EOF
	}
	c.q.mu.Lock()
	defer c.q.mu.Unlock()
	return c.q.receiveLocked()
}

func (c FrameConsumer) Receive(ctx context.Context) (PCMFrame, error) {
	if c.q == nil {
		return PCMFrame{}, io.EOF
	}
	for {
		if err := ctx.Err(); err != nil {
			return PCMFrame{}, err
		}
		c.q.mu.Lock()
		frame, ok, err := c.q.receiveLocked()
		changed := c.q.changed
		c.q.mu.Unlock()
		if ok || err != nil {
			return frame, err
		}
		select {
		case <-ctx.Done():
			return PCMFrame{}, ctx.Err()
		case <-changed:
		}
	}
}

func (q *frameBuffer) receiveLocked() (PCMFrame, bool, error) {
	if q.size == 0 {
		if q.stats.Closed {
			return PCMFrame{}, false, io.EOF
		}
		return PCMFrame{}, false, nil
	}
	frame := q.frames[q.head]
	q.frames[q.head] = PCMFrame{}
	q.head = (q.head + 1) % len(q.frames)
	q.size--
	q.samples -= len(frame.Samples)
	q.stats.ConsumedSamples += uint64(len(frame.Samples))
	q.notifyLocked()
	return frame, true, nil
}

// Close stops admission and preserves queued frames, including partial tails.
func (p FrameProducer) Close() {
	if p.q == nil {
		return
	}
	p.q.mu.Lock()
	defer p.q.mu.Unlock()
	p.q.stats.Closed = true
	p.q.notifyLocked()
}

// Invalidate advances monotonically to epoch and discards queued old samples.
// A repeated/older command is idempotent. Samples already consumed belong to
// the downstream worker, which must also validate epochs before device writes.
func (c BufferControl) Invalidate(epoch uint64) int {
	if c.q == nil {
		return 0
	}
	c.q.mu.Lock()
	defer c.q.mu.Unlock()
	if epoch <= c.q.stats.Epoch {
		return 0
	}
	discarded := c.q.samples
	clear(c.q.frames)
	c.q.head, c.q.size, c.q.samples = 0, 0, 0
	c.q.stats.DiscardedSamples += uint64(discarded)
	c.q.stats.Epoch = epoch
	c.q.notifyLocked()
	return discarded
}

func (c BufferControl) Snapshot() BufferStats {
	if c.q == nil {
		return BufferStats{Closed: true}
	}
	c.q.mu.Lock()
	defer c.q.mu.Unlock()
	s := c.q.stats
	s.QueuedFrames, s.QueuedSamples = c.q.size, c.q.samples
	return s
}

func (q *frameBuffer) notifyLocked() { close(q.changed); q.changed = make(chan struct{}) }
