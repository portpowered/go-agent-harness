package mixer

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

const (
	defaultAccumulatorDuration = 10 * time.Minute
	maxSourceTimelines         = 256
)

// PCMAccumulator records a bounded, mono PCM timeline. It owns sample
// alignment, overlap summing, clipping, and WAV serialization so room
// evidence code never grows a second DSP implementation.
type PCMAccumulator struct {
	rate      int
	maxSample int

	mu        sync.Mutex
	samples   []int64
	truncated bool
	sources   map[SourceKey]sourceTimeline
	active    map[sourceIdentity]SourceKey
}

// SourceKey identifies one source lineage in a reconstructed timeline. A
// source may reuse a stream ID across interruption epochs; Epoch keeps those
// lineages independent while the accumulator retires the prior epoch.
type SourceKey struct {
	SourceID string
	StreamID string
	Epoch    uint64
}

type sourceIdentity struct {
	sourceID string
	streamID string
}

type sourceTimeline struct {
	anchorSample int64
	lastCursor   uint64
	hasCursor    bool
	lastEnded    bool
}

// NewPCMAccumulator creates a bounded sample accumulator for the supplied
// format. Room evidence currently records the negotiated mono provider stream;
// rejecting other channel layouts keeps the timeline unambiguous.
func NewPCMAccumulator(format Format, maxDuration time.Duration) (*PCMAccumulator, error) {
	if format.SampleRate <= 0 {
		return nil, fmt.Errorf("%w: sample rate must be positive", ErrInvalidFormat)
	}
	if format.Channels != 1 {
		return nil, fmt.Errorf("%w: PCM accumulator requires mono audio", ErrInvalidFormat)
	}
	if maxDuration <= 0 {
		maxDuration = defaultAccumulatorDuration
	}
	maxSample64 := durationSamples(maxDuration, format.SampleRate)
	if maxSample64 <= 0 || maxSample64 > int64(maxInt()) {
		return nil, fmt.Errorf("%w: accumulator duration is out of range", ErrInvalidFormat)
	}
	return &PCMAccumulator{
		rate: format.SampleRate, maxSample: int(maxSample64),
		sources: make(map[SourceKey]sourceTimeline), active: make(map[sourceIdentity]SourceKey),
	}, nil
}

// Add places one source span on the timeline using its arrival offset.
// Overlapping sources are summed and clipped once during Finalize. A span
// beyond the configured bound is retained up to the bound and returns an
// error so the caller can mark evidence as partial without blocking the media
// path.
func (a *PCMAccumulator) Add(offset time.Duration, samples []int16) error {
	return a.AddFrame(offset, 0, false, samples)
}

// AddFrame places one source span on the timeline. When hasStartSample is
// true, startSample is the source's authoritative sample cursor and offset is
// retained only as arrival timing by the caller. This keeps transport jitter
// from becoming artificial silence or overlap in reconstructed playback while
// preserving an offset based fallback for legacy frames without a cursor.
func (a *PCMAccumulator) AddFrame(offset time.Duration, startSample uint64, hasStartSample bool, samples []int16) error {
	if a == nil || len(samples) == 0 {
		return nil
	}
	start, err := a.frameStart(offset, startSample, hasStartSample)
	if err != nil {
		return err
	}
	count, err := a.frameSampleCount(start, len(samples))
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.addSamplesLocked(start, samples[:count])
	if count < len(samples) {
		a.truncated = true
	}
	a.mu.Unlock()
	if count < len(samples) {
		return accumulatorDurationBoundError()
	}
	return nil
}

func (a *PCMAccumulator) frameStart(offset time.Duration, startSample uint64, hasStartSample bool) (int64, error) {
	if !hasStartSample {
		return maxInt64(durationSamples(offset, a.rate)), nil
	}
	if startSample > uint64(math.MaxInt64) {
		a.markTruncated()
		return 0, fmt.Errorf("PCM accumulator sample cursor is out of range")
	}
	return int64(startSample), nil
}

func maxInt64(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func (a *PCMAccumulator) frameSampleCount(start int64, length int) (int, error) {
	if start >= int64(a.maxSample) {
		a.markTruncated()
		return 0, accumulatorDurationBoundError()
	}
	remaining := a.maxSample - int(start)
	if length > remaining {
		return remaining, nil
	}
	return length, nil
}

func (a *PCMAccumulator) addSamplesLocked(start int64, samples []int16) {
	needed := int(start) + len(samples)
	a.ensureSampleLengthLocked(needed)
	for index, sample := range samples {
		a.samples[int(start)+index] += int64(sample)
	}
}

func (a *PCMAccumulator) ensureSampleLengthLocked(needed int) {
	if needed <= len(a.samples) {
		return
	}
	if needed <= cap(a.samples) {
		oldLength := len(a.samples)
		a.samples = a.samples[:needed]
		clear(a.samples[oldLength:])
		return
	}
	capacity := cap(a.samples)
	if capacity == 0 {
		capacity = 256
	}
	for capacity < needed {
		if capacity > a.maxSample/2 {
			capacity = a.maxSample
			break
		}
		capacity *= 2
	}
	grown := make([]int64, needed, capacity)
	copy(grown, a.samples)
	a.samples = grown
}

// AddSource places a source frame on the room timeline while keeping its
// transport arrival offset separate from its source-local sample cursor. The
// first frame of each source/epoch establishes an anchor; subsequent frames
// follow that cursor. An end-of-response frame retires the current anchor for
// the next frame, which preserves inter-response pauses even when a provider
// uses one monotonic cursor across responses.
func (a *PCMAccumulator) AddSource(key SourceKey, offset time.Duration, startSample uint64, hasStartSample, endOfResponse bool, samples []int16) error {
	if a == nil {
		return nil
	}
	if !hasStartSample {
		return a.Add(offset, samples)
	}
	if startSample > uint64(math.MaxInt64) {
		a.markTruncated()
		return fmt.Errorf("PCM accumulator sample cursor is out of range")
	}
	arrival := durationSamples(offset, a.rate)
	identity := sourceIdentity{sourceID: key.SourceID, streamID: key.StreamID}
	a.mu.Lock()
	if previous, exists := a.active[identity]; exists && previous != key {
		delete(a.sources, previous)
	}
	if _, exists := a.sources[key]; !exists && len(a.sources) >= maxSourceTimelines {
		a.truncated = true
		a.mu.Unlock()
		return fmt.Errorf("PCM accumulator source timeline bound reached")
	}
	state, known := a.sources[key]
	reset := !known || !state.hasCursor || state.lastEnded || startSample < state.lastCursor
	if reset {
		state.anchorSample = saturatingSub(arrival, int64(startSample))
	}
	start := saturatingAdd(state.anchorSample, int64(startSample))
	state.lastCursor = startSample + uint64(len(samples))
	if state.lastCursor < startSample {
		state.lastCursor = ^uint64(0)
	}
	state.hasCursor = true
	state.lastEnded = endOfResponse
	a.sources[key] = state
	a.active[identity] = key
	a.mu.Unlock()
	if start < 0 {
		start = 0
	}
	return a.AddFrame(0, uint64(start), true, samples)
}

func (a *PCMAccumulator) markTruncated() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.truncated = true
	a.mu.Unlock()
}

// Finalize writes the bounded timeline as PCM16 WAV. The requested span is
// padded with silence when it is within the bound; an empty timeline produces
// a valid zero-sample WAV data chunk rather than fabricated audio samples.
func (a *PCMAccumulator) Finalize(span time.Duration, path string) error {
	if a == nil {
		return nil
	}
	values, boundErr := a.snapshot(span)
	if err := writePCMAccumulatorWAV(a.rate, values, path); err != nil {
		return errors.Join(boundErr, err)
	}
	return boundErr
}

func accumulatorDurationBoundError() error {
	return fmt.Errorf("PCM accumulator duration bound reached")
}

func durationSamples(value time.Duration, rate int) int64 {
	if value <= 0 || rate <= 0 {
		return 0
	}
	seconds := int64(value / time.Second)
	nanos := int64(value % time.Second)
	if seconds > math.MaxInt64/int64(rate) {
		return math.MaxInt64
	}
	whole := seconds * int64(rate)
	if nanos > 0 {
		// The conservative quotient avoids multiplying a near-maximum
		// duration by the sample rate before checking overflow.
		if nanos > (math.MaxInt64-whole)/int64(rate) {
			return math.MaxInt64
		}
	}
	return whole + nanos*int64(rate)/int64(time.Second)
}

func saturatingAdd(left, right int64) int64 {
	if right > 0 && left > math.MaxInt64-right {
		return math.MaxInt64
	}
	if right < 0 && left < math.MinInt64-right {
		return math.MinInt64
	}
	return left + right
}

func saturatingSub(left, right int64) int64 {
	if right > 0 && left < math.MinInt64+right {
		return math.MinInt64
	}
	if right < 0 && left > math.MaxInt64+right {
		return math.MaxInt64
	}
	return left - right
}

func maxInt() int { return int(^uint(0) >> 1) }
