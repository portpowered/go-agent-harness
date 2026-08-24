package metrics

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalidObservation is the broad class for rejected recordings.
	ErrInvalidObservation = errors.New("metrics: invalid observation")
	// ErrInvalidDirection identifies an unsupported stream direction.
	ErrInvalidDirection = errors.New("metrics: invalid direction")
	// ErrInvalidModality identifies an unsupported stream modality.
	ErrInvalidModality = errors.New("metrics: invalid modality")
	// ErrInvalidByteSize identifies a byte size outside the non-negative range.
	ErrInvalidByteSize = errors.New("metrics: invalid byte size")
	// ErrInvalidHistogramConfiguration is the broad class for rejected bucket configurations.
	ErrInvalidHistogramConfiguration = errors.New("metrics: invalid histogram configuration")
	// ErrEmptyHistogramBounds identifies a histogram with no upper bounds.
	ErrEmptyHistogramBounds = errors.New("metrics: empty histogram bounds")
	// ErrInvalidHistogramBound identifies a negative upper bound.
	ErrInvalidHistogramBound = errors.New("metrics: invalid histogram bound")
	// ErrNonIncreasingHistogramBounds identifies bounds that do not increase strictly.
	ErrNonIncreasingHistogramBounds = errors.New("metrics: non-increasing histogram bounds")
	// ErrDuplicateHistogramBound identifies two equal adjacent upper bounds.
	ErrDuplicateHistogramBound = errors.New("metrics: duplicate histogram bound")
	// ErrCounterOverflow identifies a recording that cannot be represented safely.
	ErrCounterOverflow = errors.New("metrics: counter overflow")
	// ErrNilSink identifies a method call on a nil in-memory sink.
	ErrNilSink = errors.New("metrics: nil sink")
)

// ErrInvalidHistogramBounds is a compatibility alias for the specific
// histogram-configuration error class.
var ErrInvalidHistogramBounds = ErrInvalidHistogramConfiguration

// ErrInvalidBounds is a concise alias for ErrInvalidHistogramBounds.
var ErrInvalidBounds = ErrInvalidHistogramBounds

// ValidationError is a stable, typed validation failure. Use errors.Is to
// match Kind and errors.As to inspect the diagnostic message.
type ValidationError struct {
	Kind    error
	Message string
}

// Error returns the stable diagnostic message for the validation failure.
func (e *ValidationError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return e.Message
}

// Is matches both the specific validation kind and its broad observation or
// histogram-configuration class.
func (e *ValidationError) Is(target error) bool {
	if e == nil {
		return false
	}
	if target == e.Kind {
		return true
	}
	if target == ErrInvalidObservation {
		return e.Kind == ErrInvalidDirection || e.Kind == ErrInvalidModality || e.Kind == ErrInvalidByteSize
	}
	if target == ErrInvalidHistogramConfiguration {
		return e.Kind == ErrEmptyHistogramBounds || e.Kind == ErrInvalidHistogramBound ||
			e.Kind == ErrNonIncreasingHistogramBounds || e.Kind == ErrDuplicateHistogramBound
	}
	return false
}

// CounterOverflowError describes the series and counter that could not be
// updated without wrapping.
type CounterOverflowError struct {
	Key   SeriesKey
	Field string
}

// Error returns the operator-readable overflow message.
func (e *CounterOverflowError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("metrics: counter overflow for %s %s", e.Key, e.Field)
}

// Is matches ErrCounterOverflow.
func (e *CounterOverflowError) Is(target error) bool {
	return e != nil && target == ErrCounterOverflow
}

func validationError(kind error, message string) error {
	return &ValidationError{Kind: kind, Message: message}
}
