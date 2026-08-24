package metrics

import "fmt"

var defaultHistogramBounds = [...]int64{0, 64, 256, 1024, 4096, 16384, 65536, 262144, 1048576}

// DefaultHistogramBounds returns a copy of the default inclusive upper
// bounds used when NewInMemorySink is called without explicit bounds.
func DefaultHistogramBounds() []int64 {
	return append([]int64(nil), defaultHistogramBounds[:]...)
}

func validateHistogramBounds(bounds []int64) ([]int64, error) {
	if len(bounds) == 0 {
		return nil, validationError(ErrEmptyHistogramBounds, "metrics: histogram bounds must not be empty")
	}

	validated := append([]int64(nil), bounds...)
	for index, bound := range validated {
		if bound < 0 {
			return nil, validationError(ErrInvalidHistogramBound, fmt.Sprintf("metrics: histogram bound at index %d must be non-negative: %d", index, bound))
		}
		if index == 0 {
			continue
		}
		previous := validated[index-1]
		if bound == previous {
			return nil, validationError(ErrDuplicateHistogramBound, fmt.Sprintf("metrics: histogram bound at index %d duplicates %d", index, bound))
		}
		if bound < previous {
			return nil, validationError(ErrNonIncreasingHistogramBounds, fmt.Sprintf("metrics: histogram bound at index %d is less than previous bound %d: %d", index, previous, bound))
		}
	}
	return validated, nil
}

// HistogramSnapshot is an immutable-by-convention value copy of one byte-size
// histogram. BucketCounts are non-cumulative counts for the corresponding
// inclusive Bounds entries. OverflowCount contains samples larger than the
// final bound.
type HistogramSnapshot struct {
	Bounds        []int64
	BucketCounts  []uint64
	OverflowCount uint64
	SampleCount   uint64
	ByteSum       uint64
}

func newHistogramSnapshot(bounds []int64, state seriesState) HistogramSnapshot {
	return HistogramSnapshot{
		Bounds:        append([]int64(nil), bounds...),
		BucketCounts:  append([]uint64(nil), state.bucketCounts...),
		OverflowCount: state.overflowCount,
		SampleCount:   state.sampleCount,
		ByteSum:       state.byteSum,
	}
}
