package metrics

import (
	"fmt"
	"sort"
	"sync"
)

const maxUint64 = ^uint64(0)

// Sink records stream observations and exposes an exact read snapshot.
type Sink interface {
	Record(direction Direction, modality Modality, byteSize int64) error
	Snapshot() Snapshot
}

// Recorder is the write-only portion of Sink for instrumentation seams that
// do not need to read metrics.
type Recorder interface {
	Record(direction Direction, modality Modality, byteSize int64) error
}

type seriesState struct {
	eventCount    uint64
	totalBytes    uint64
	bucketCounts  []uint64
	overflowCount uint64
	sampleCount   uint64
	byteSum       uint64
}

// InMemorySink is a concurrency-safe metrics sink whose state lives only in
// memory. Each Snapshot call returns independent slices that can be retained
// or modified by the caller without affecting the sink or another snapshot.
type InMemorySink struct {
	mu     sync.RWMutex
	bounds []int64
	series map[SeriesKey]*seriesState
}

// NewInMemorySink constructs a sink. With no argument it uses
// DefaultHistogramBounds. With one argument it validates and copies the
// supplied inclusive upper bounds. Bounds must be non-empty, non-negative, and
// strictly increasing.
func NewInMemorySink(bounds ...[]int64) (*InMemorySink, error) {
	if len(bounds) > 1 {
		return nil, validationError(ErrInvalidHistogramConfiguration, "metrics: histogram bounds configuration accepts at most one slice")
	}
	selectedBounds := DefaultHistogramBounds()
	if len(bounds) == 1 {
		selectedBounds = bounds[0]
	}
	validatedBounds, err := validateHistogramBounds(selectedBounds)
	if err != nil {
		return nil, err
	}

	sink := &InMemorySink{
		bounds: validatedBounds,
		series: make(map[SeriesKey]*seriesState),
	}
	for _, key := range orderedSeriesKeys() {
		sink.series[key] = &seriesState{bucketCounts: make([]uint64, len(validatedBounds))}
	}
	return sink, nil
}

// NewInMemorySinkWithBounds is an explicit-name constructor for callers that
// prefer not to use the variadic default-enabled form of NewInMemorySink.
func NewInMemorySinkWithBounds(bounds []int64) (*InMemorySink, error) {
	return NewInMemorySink(bounds)
}

// Record adds one observation to exactly one direction-and-modality series.
// The byte size is metadata only; no payload is accepted or retained.
func (s *InMemorySink) Record(direction Direction, modality Modality, byteSize int64) error {
	if s == nil {
		return ErrNilSink
	}
	if err := validateObservation(direction, modality, byteSize); err != nil {
		return err
	}

	key := SeriesKey{Direction: direction, Modality: modality}
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.series[key]
	if !ok {
		return validationError(ErrInvalidObservation, fmt.Sprintf("metrics: unsupported series %s", key))
	}
	byteCount := uint64(byteSize)
	bucketIndex := sort.Search(len(s.bounds), func(index int) bool {
		return byteCount <= uint64(s.bounds[index])
	})
	if err := validateCounterCapacity(key, state, byteCount, bucketIndex, len(s.bounds)); err != nil {
		return err
	}

	state.eventCount++
	state.totalBytes += byteCount
	state.sampleCount++
	state.byteSum += byteCount
	if bucketIndex == len(s.bounds) {
		state.overflowCount++
	} else {
		state.bucketCounts[bucketIndex]++
	}
	return nil
}

func validateObservation(direction Direction, modality Modality, byteSize int64) error {
	if !direction.Valid() {
		return validationError(ErrInvalidDirection, fmt.Sprintf("metrics: invalid direction %q", direction))
	}
	if !modality.Valid() {
		return validationError(ErrInvalidModality, fmt.Sprintf("metrics: invalid modality %q", modality))
	}
	if byteSize < 0 {
		return validationError(ErrInvalidByteSize, fmt.Sprintf("metrics: byte size must be non-negative: %d", byteSize))
	}
	return nil
}

func validateCounterCapacity(key SeriesKey, state *seriesState, byteCount uint64, bucketIndex, bucketCount int) error {
	if state.eventCount == maxUint64 {
		return &CounterOverflowError{Key: key, Field: "event count"}
	}
	if state.totalBytes > maxUint64-byteCount {
		return &CounterOverflowError{Key: key, Field: "total bytes"}
	}
	if state.sampleCount == maxUint64 {
		return &CounterOverflowError{Key: key, Field: "histogram sample count"}
	}
	if state.byteSum > maxUint64-byteCount {
		return &CounterOverflowError{Key: key, Field: "histogram byte sum"}
	}
	if bucketIndex == bucketCount {
		if state.overflowCount == maxUint64 {
			return &CounterOverflowError{Key: key, Field: "histogram overflow count"}
		}
	} else if state.bucketCounts[bucketIndex] == maxUint64 {
		return &CounterOverflowError{Key: key, Field: "histogram bucket count"}
	}
	return nil
}

// Snapshot is a deterministic, deep-copied view of all supported series. The
// Series slice is ordered by direction and then modality.
type Snapshot struct {
	HistogramBounds []int64
	Series          []SeriesSnapshot
}

// SeriesSnapshot is the exact counter and histogram state for one series.
type SeriesSnapshot struct {
	Direction  Direction
	Modality   Modality
	EventCount uint64
	TotalBytes uint64
	Histogram  HistogramSnapshot
}

// Snapshot returns a deep copy of the sink state.
func (s *InMemorySink) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	snapshot := Snapshot{
		HistogramBounds: append([]int64(nil), s.bounds...),
		Series:          make([]SeriesSnapshot, 0, len(s.series)),
	}
	for _, key := range orderedSeriesKeys() {
		state := s.series[key]
		if state == nil {
			state = &seriesState{bucketCounts: make([]uint64, len(s.bounds))}
		}
		snapshot.Series = append(snapshot.Series, SeriesSnapshot{
			Direction:  key.Direction,
			Modality:   key.Modality,
			EventCount: state.eventCount,
			TotalBytes: state.totalBytes,
			Histogram:  newHistogramSnapshot(s.bounds, *state),
		})
	}
	return snapshot
}

// Lookup returns the series for a supported direction and modality.
func (s Snapshot) Lookup(direction Direction, modality Modality) (SeriesSnapshot, bool) {
	for _, series := range s.Series {
		if series.Direction == direction && series.Modality == modality {
			return series, true
		}
	}
	return SeriesSnapshot{}, false
}

// SeriesFor returns the series for a supported direction and modality. It
// returns a zero SeriesSnapshot when the key is not present.
func (s Snapshot) SeriesFor(direction Direction, modality Modality) SeriesSnapshot {
	series, _ := s.Lookup(direction, modality)
	return series
}

// Get is a concise alias for SeriesFor.
func (s Snapshot) Get(direction Direction, modality Modality) SeriesSnapshot {
	return s.SeriesFor(direction, modality)
}
