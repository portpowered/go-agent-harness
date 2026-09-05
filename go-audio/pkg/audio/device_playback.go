package audio

import (
	"encoding/binary"
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
	// DefaultPlaybackLowWatermark is the amount of queued audio left when a
	// throttled producer may resume. Keeping this reserve avoids starving the
	// next device callback while the producer is being scheduled.
	DefaultPlaybackLowWatermark = 120 * time.Millisecond
	// DefaultPlaybackHighWatermark is the normal queue ceiling. Producers
	// pause above this target instead of filling the drop-oldest capacity.
	DefaultPlaybackHighWatermark = 180 * time.Millisecond
)

// ErrInvalidPlaybackQueue identifies an invalid playback format or latency target.
var ErrInvalidPlaybackQueue = errors.New("invalid audio playback queue")

// PlaybackQueueStats is a synchronized snapshot of one device playback
// queue. DroppedSamples and DiscardedSamples are cumulative for the lifetime
// of the queue; the former counts overflow loss and the latter counts an
// explicit cancellation/discard operation.
type PlaybackQueueStats struct {
	Format               DeviceFormat
	LatencyTarget        time.Duration
	CapacitySamples      int
	QueuedSamples        int
	PeakQueuedSamples    int
	DroppedSamples       uint64
	OverflowEvents       uint64
	DiscardedSamples     uint64
	DiscardEvents        uint64
	CallbackCount        uint64
	RenderedSamples      uint64
	UnderflowEvents      uint64
	UnderflowSamples     uint64
	ZeroFilledSamples    uint64
	MinimumQueuedSamples int
}

// PlaybackQueue is a bounded, drop-oldest PCM16 queue for a device callback.
// All queue mutation and counters share one synchronization boundary so a
// producer, callback, cancellation, and close can safely race.
type PlaybackQueue struct {
	mu               sync.Mutex
	format           DeviceFormat
	latencyTarget    time.Duration
	capacity         int
	samples          []int16
	head             int
	size             int
	peakQueued       int
	dropped          uint64
	overflowEvents   uint64
	discarded        uint64
	discardEvents    uint64
	callbackCount    uint64
	rendered         uint64
	underflows       uint64
	underflowSamples uint64
	minimumQueued    int
	renderObserver   PlaybackRenderObserver
	renderPool       sync.Pool
}

// PlaybackRenderObserver receives the full PCM16 device callback, including
// underflow silence inserted by RenderInto and ReadPCM16. ReadInto reports only
// dequeued samples because its caller owns any remaining destination.
// Implementations must return immediately and
// copy samples they retain; the callback-owned slice is invalid after return.
type PlaybackRenderObserver func(sampleRate int, samples []int16)

// SetRenderObserver installs the optional device-consumption tap.
func (q *PlaybackQueue) SetRenderObserver(observer PlaybackRenderObserver) {
	if q == nil {
		return
	}
	q.mu.Lock()
	q.renderObserver = observer
	q.mu.Unlock()
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
	queue := &PlaybackQueue{
		format:        format,
		latencyTarget: latency,
		capacity:      capacity,
		samples:       make([]int16, capacity),
		minimumQueued: capacity,
	}
	queue.renderPool.New = func() any {
		samples := make([]int16, FrameSize)
		return &samples
	}
	return queue, nil
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

// PlaybackQueueWatermarks converts the default pacing latency targets into
// interleaved sample counts for format. The high watermark remains below the
// queue's hard drop-oldest capacity; the low watermark provides hysteresis so
// a producer wakes in useful batches instead of once per consumed sample.
func PlaybackQueueWatermarks(format DeviceFormat) (lowSamples, highSamples int, err error) {
	lowSamples, err = PlaybackQueueCapacity(format, DefaultPlaybackLowWatermark)
	if err != nil {
		return 0, 0, err
	}
	highSamples, err = PlaybackQueueCapacity(format, DefaultPlaybackHighWatermark)
	if err != nil {
		return 0, 0, err
	}
	hardCapacity, err := PlaybackQueueCapacity(format, DefaultPlaybackLatencyTarget)
	if err != nil {
		return 0, 0, err
	}
	if lowSamples >= highSamples || highSamples >= hardCapacity {
		return 0, 0, fmt.Errorf("%w: pacing watermarks low=%d high=%d capacity=%d", ErrInvalidPlaybackQueue, lowSamples, highSamples, hardCapacity)
	}
	return lowSamples, highSamples, nil
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

	overflow := q.size + len(samples) - q.capacity
	if len(samples) >= q.capacity {
		samples = samples[len(samples)-q.capacity:]
		q.head, q.size = 0, 0
	} else if overflow > 0 {
		q.consumeLocked(overflow)
	}
	q.writeLocked(samples)
	if overflow > 0 {
		q.dropped += uint64(overflow)
		q.overflowEvents++
	}
	if q.size > q.peakQueued {
		q.peakQueued = q.size
	}
	return MaxIntValue(overflow, 0)
}

// Dequeue returns up to maxSamples in FIFO order. The returned storage is
// owned by the caller and may be modified.
func (q *PlaybackQueue) Dequeue(maxSamples int) []int16 {
	if q == nil || maxSamples <= 0 {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	n := min(maxSamples, q.size)
	if n == 0 {
		return nil
	}
	out := make([]int16, n)
	q.readIntoLocked(out)
	return out
}

// ReadInto copies up to len(destination) samples in FIFO order without
// allocating. It is intended for device callbacks and returns the count read.
func (q *PlaybackQueue) ReadInto(destination []int16) int {
	if q == nil || len(destination) == 0 {
		return 0
	}
	q.mu.Lock()
	n := min(len(destination), q.size)
	q.readIntoLocked(destination[:n])
	observer, rate := q.renderObserver, q.format.SampleRate
	q.mu.Unlock()
	if n > 0 && observer != nil {
		observer(rate, destination[:n])
	}
	return n
}

// RenderInto is the callback-facing read. It always initializes destination,
// records exact zero-fill loss, and performs work proportional only to the
// callback quantum, independent of queued depth.
func (q *PlaybackQueue) RenderInto(destination []int16) int {
	if q == nil || len(destination) == 0 {
		return 0
	}
	q.mu.Lock()
	queuedBefore := q.size
	n := min(len(destination), q.size)
	q.readIntoLocked(destination[:n])
	clear(destination[n:])
	q.callbackCount++
	q.rendered += uint64(len(destination))
	missing := len(destination) - n
	if missing > 0 {
		q.underflows++
		q.underflowSamples += uint64(missing)
	}
	if queuedBefore < q.minimumQueued {
		q.minimumQueued = queuedBefore
	}
	observer, rate := q.renderObserver, q.format.SampleRate
	q.mu.Unlock()
	if observer != nil {
		observer(rate, destination)
	}
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
	discarded := q.size
	if discarded == 0 {
		return 0
	}
	q.head, q.size = 0, 0
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
		Format:               q.format,
		LatencyTarget:        q.latencyTarget,
		CapacitySamples:      q.capacity,
		QueuedSamples:        q.size,
		PeakQueuedSamples:    q.peakQueued,
		DroppedSamples:       q.dropped,
		OverflowEvents:       q.overflowEvents,
		DiscardedSamples:     q.discarded,
		DiscardEvents:        q.discardEvents,
		CallbackCount:        q.callbackCount,
		RenderedSamples:      q.rendered,
		UnderflowEvents:      q.underflows,
		UnderflowSamples:     q.underflowSamples,
		ZeroFilledSamples:    q.underflowSamples,
		MinimumQueuedSamples: q.minimumQueued,
	}
}

// ReadPCM16 drains samples directly into a mono PCM16 device callback buffer.
// The queue is only used with validated mono PCM16 DeviceFormats.
func (q *PlaybackQueue) ReadPCM16(destination []byte) int {
	if q == nil || len(destination) < 2 {
		return 0
	}
	q.mu.Lock()
	requested := len(destination) / 2
	queuedBefore := q.size
	n := min(requested, q.size)
	for index := 0; index < n; index++ {
		value := uint16(q.samples[(q.head+index)%q.capacity])
		destination[index*2] = byte(value)
		destination[index*2+1] = byte(value >> 8)
	}
	clear(destination[n*2 : requested*2])
	q.consumeLocked(n)
	q.callbackCount++
	q.rendered += uint64(requested)
	if missing := requested - n; missing > 0 {
		q.underflows++
		q.underflowSamples += uint64(missing)
	}
	if queuedBefore < q.minimumQueued {
		q.minimumQueued = queuedBefore
	}
	observer, rate := q.renderObserver, q.format.SampleRate
	var rendered []int16
	var pooled *[]int16
	if observer != nil {
		pooled = q.renderPool.Get().(*[]int16)
		rendered = *pooled
		if cap(rendered) < requested {
			q.renderPool.Put(pooled)
			pooled = nil
			rendered = make([]int16, requested)
		} else {
			rendered = rendered[:requested]
		}
		for index := range rendered {
			rendered[index] = int16(binary.LittleEndian.Uint16(destination[index*2:]))
		}
	}
	q.mu.Unlock()
	if len(rendered) > 0 {
		observer(rate, rendered)
		if pooled != nil {
			*pooled = rendered[:FrameSize]
			q.renderPool.Put(pooled)
		}
	}
	return n
}

func (q *PlaybackQueue) consumeLocked(n int) {
	if n <= 0 || q.size == 0 {
		return
	}
	if n > q.size {
		n = q.size
	}
	q.head = (q.head + n) % q.capacity
	q.size -= n
	if q.size == 0 {
		q.head = 0
	}
}

func (q *PlaybackQueue) readIntoLocked(destination []int16) {
	first := min(len(destination), q.capacity-q.head)
	copy(destination[:first], q.samples[q.head:q.head+first])
	copy(destination[first:], q.samples[:len(destination)-first])
	q.consumeLocked(len(destination))
}

func (q *PlaybackQueue) writeLocked(samples []int16) {
	if len(samples) == 0 {
		return
	}
	tail := (q.head + q.size) % q.capacity
	first := min(len(samples), q.capacity-tail)
	copy(q.samples[tail:tail+first], samples[:first])
	copy(q.samples[:len(samples)-first], samples[first:])
	q.size += len(samples)
}

func MaxIntValue(value, floor int) int {
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

// CaptureStatsProvider is the optional device capability for synchronized
// native capture queue and loss counters.
type CaptureStatsProvider interface {
	CaptureStats() CaptureQueueStats
}

// PlaybackDiscarder exposes cancellation-scoped removal of queued samples.
// It is optional for compatibility with non-queueing device implementations.
type PlaybackDiscarder interface {
	DiscardPlayback() int
}

func EmptyPlaybackQueueStats(format DeviceFormat) PlaybackQueueStats {
	if format.Validate() != nil {
		format = DefaultDeviceFormat()
	}
	capacity, _ := PlaybackQueueCapacity(format, DefaultPlaybackLatencyTarget)
	return PlaybackQueueStats{Format: format, LatencyTarget: DefaultPlaybackLatencyTarget, CapacitySamples: capacity}
}

func PlaybackQueueForFormat(format DeviceFormat) (*PlaybackQueue, error) {
	if format.Validate() != nil {
		format = DefaultDeviceFormat()
	}
	return NewPlaybackQueue(format)
}
