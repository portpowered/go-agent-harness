package audio

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	// DefaultPlaybackLatencyTarget is the maximum amount of audio the local
	// playback queue intentionally retains ahead of the device callback. It is
	// a latency budget, not a backend-specific frame count; capacity is derived
	// from this target and the opened device format.
	DefaultPlaybackLatencyTarget = 250 * time.Millisecond
)

// ErrInvalidPlaybackQueue identifies an invalid playback format or latency target.
var ErrInvalidPlaybackQueue = errors.New("invalid audio playback queue")

// PlaybackQueueStats is a synchronized snapshot of one device playback
// queue. DroppedSamples and DiscardedSamples are cumulative for the lifetime
// of the queue; the former counts overflow loss and the latter counts an
// explicit cancellation/discard operation.
type PlaybackQueueStats struct {
	Format            DeviceFormat
	LatencyTarget     time.Duration
	CapacitySamples   int
	QueuedSamples     int
	PeakQueuedSamples int
	DroppedSamples    uint64
	OverflowEvents    uint64
	DiscardedSamples  uint64
	DiscardEvents     uint64
}

// PlaybackQueue is a bounded, drop-oldest PCM16 queue for a device callback.
// All queue mutation and counters share one synchronization boundary so a
// producer, callback, cancellation, and close can safely race.
type PlaybackQueue struct {
	mu             sync.Mutex
	format         DeviceFormat
	latencyTarget  time.Duration
	capacity       int
	samples        []int16
	peakQueued     int
	dropped        uint64
	overflowEvents uint64
	discarded      uint64
	discardEvents  uint64
}

// NewPlaybackQueue creates a queue using DefaultPlaybackLatencyTarget.
func NewPlaybackQueue(format DeviceFormat) (*PlaybackQueue, error) {
	return NewPlaybackQueueWithLatency(format, DefaultPlaybackLatencyTarget)
}

// NewPlaybackQueueWithLatency creates a queue whose capacity is the number of
// interleaved PCM samples represented by latency at format's rate/channels.
// The result is rounded up so a positive latency always retains at least one
// sample.
func NewPlaybackQueueWithLatency(format DeviceFormat, latency time.Duration) (*PlaybackQueue, error) {
	capacity, err := PlaybackQueueCapacity(format, latency)
	if err != nil {
		return nil, err
	}
	return &PlaybackQueue{
		format:        format,
		latencyTarget: latency,
		capacity:      capacity,
	}, nil
}

// PlaybackQueueCapacity derives the bounded queue size from an explicit
// device format and latency target. It intentionally does not refer to the
// legacy audio.FrameSize constant.
func PlaybackQueueCapacity(format DeviceFormat, latency time.Duration) (int, error) {
	if err := format.Validate(); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrInvalidPlaybackQueue, err)
	}
	if latency <= 0 {
		return 0, fmt.Errorf("%w: latency target must be positive; got %s", ErrInvalidPlaybackQueue, latency)
	}

	seconds := uint64(time.Second)
	rate := uint64(format.SampleRate)
	channels := uint64(format.Channels)
	nanos := uint64(latency)
	if rate > ^uint64(0)/channels || rate*channels > ^uint64(0)/nanos {
		return 0, fmt.Errorf("%w: latency capacity overflows for %s at %s", ErrInvalidPlaybackQueue, format, latency)
	}
	numerator := rate * channels * nanos
	capacity := numerator / seconds
	if numerator%seconds != 0 {
		capacity++
	}
	maxInt := uint64(^uint(0) >> 1)
	if capacity == 0 || capacity > maxInt {
		return 0, fmt.Errorf("%w: derived capacity %d is outside int range", ErrInvalidPlaybackQueue, capacity)
	}
	return int(capacity), nil
}

// Enqueue appends samples and returns the exact number of oldest samples
// discarded to keep the queue within its latency-derived capacity. The
// newest samples are retained when one input is larger than the queue.
func (q *PlaybackQueue) Enqueue(samples []int16) int {
	if q == nil || len(samples) == 0 {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	overflow := len(q.samples) + len(samples) - q.capacity
	if overflow <= 0 {
		q.samples = append(q.samples, samples...)
	} else if len(samples) >= q.capacity {
		// The incoming chunk is itself larger than the queue. Existing samples
		// and the oldest part of this chunk are both older than its retained
		// tail, so the tail is the exact drop-oldest result.
		start := len(samples) - q.capacity
		q.samples = append(q.samples[:0], samples[start:]...)
	} else {
		// With a smaller incoming chunk, overflow cannot exceed the existing
		// queue length. Compact before appending so order remains exact.
		remaining := len(q.samples) - overflow
		copy(q.samples, q.samples[overflow:])
		q.samples = append(q.samples[:remaining], samples...)
	}
	if overflow > 0 {
		q.dropped += uint64(overflow)
		q.overflowEvents++
	}
	if len(q.samples) > q.peakQueued {
		q.peakQueued = len(q.samples)
	}
	return maxIntValue(overflow, 0)
}

// Dequeue returns up to maxSamples in FIFO order. The returned storage is
// owned by the caller and may be modified.
func (q *PlaybackQueue) Dequeue(maxSamples int) []int16 {
	if q == nil || maxSamples <= 0 {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	n := min(maxSamples, len(q.samples))
	if n == 0 {
		return nil
	}
	out := append([]int16(nil), q.samples[:n]...)
	q.consumeLocked(n)
	return out
}

// ReadInto copies up to len(destination) samples in FIFO order without
// allocating. It is intended for device callbacks and returns the count read.
func (q *PlaybackQueue) ReadInto(destination []int16) int {
	if q == nil || len(destination) == 0 {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	n := min(len(destination), len(q.samples))
	copy(destination[:n], q.samples[:n])
	q.consumeLocked(n)
	return n
}

// Discard removes only samples still queued for a future device callback and
// returns the exact amount removed. Samples already handed to a callback are
// outside this queue and cannot be recalled.
func (q *PlaybackQueue) Discard() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	discarded := len(q.samples)
	if discarded == 0 {
		return 0
	}
	q.samples = nil
	q.discarded += uint64(discarded)
	q.discardEvents++
	return discarded
}

// Snapshot returns a consistent queue observation for diagnostics and tests.
func (q *PlaybackQueue) Snapshot() PlaybackQueueStats {
	if q == nil {
		return PlaybackQueueStats{}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.snapshotLocked()
}

func (q *PlaybackQueue) snapshotLocked() PlaybackQueueStats {
	return PlaybackQueueStats{
		Format:            q.format,
		LatencyTarget:     q.latencyTarget,
		CapacitySamples:   q.capacity,
		QueuedSamples:     len(q.samples),
		PeakQueuedSamples: q.peakQueued,
		DroppedSamples:    q.dropped,
		OverflowEvents:    q.overflowEvents,
		DiscardedSamples:  q.discarded,
		DiscardEvents:     q.discardEvents,
	}
}

// readPCM16 drains samples directly into a mono PCM16 device callback buffer.
// The queue is only used with validated mono PCM16 DeviceFormats.
func (q *PlaybackQueue) readPCM16(destination []byte) int {
	if q == nil || len(destination) < 2 {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	n := min(len(destination)/2, len(q.samples))
	encodePCM16(destination[:n*2], q.samples[:n])
	q.consumeLocked(n)
	return n
}

func (q *PlaybackQueue) consumeLocked(n int) {
	if n <= 0 {
		return
	}
	copy(q.samples, q.samples[n:])
	q.samples = q.samples[:len(q.samples)-n]
	if len(q.samples) == 0 {
		q.samples = nil
	}
}

func maxIntValue(value, floor int) int {
	if value < floor {
		return floor
	}
	return value
}

// PlaybackStatsProvider exposes the queue observation owned by a playback
// device. It is optional so existing third-party OpenedDevice implementations
// remain source-compatible.
type PlaybackStatsProvider interface {
	PlaybackStats() PlaybackQueueStats
}

// PlaybackDiscarder exposes cancellation-scoped removal of queued samples.
// It is optional for compatibility with non-queueing device implementations.
type PlaybackDiscarder interface {
	DiscardPlayback() int
}

func emptyPlaybackQueueStats(format DeviceFormat) PlaybackQueueStats {
	if format.Validate() != nil {
		format = DefaultDeviceFormat()
	}
	capacity, _ := PlaybackQueueCapacity(format, DefaultPlaybackLatencyTarget)
	return PlaybackQueueStats{Format: format, LatencyTarget: DefaultPlaybackLatencyTarget, CapacitySamples: capacity}
}

func playbackQueueForFormat(format DeviceFormat) (*PlaybackQueue, error) {
	if format.Validate() != nil {
		format = DefaultDeviceFormat()
	}
	return NewPlaybackQueue(format)
}
