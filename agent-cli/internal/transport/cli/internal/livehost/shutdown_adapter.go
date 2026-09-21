package livehost

import (
	"errors"

	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

// suppressExpectedDuration is the stateless CLI exit adapter for an explicit
// max-duration stop. Runtime terminal events and recordings retain the
// duration classification; only the process exit value is translated.
func suppressExpectedDuration(err error) error {
	if err == nil {
		return nil
	}
	// Leave unrelated typed causes untouched. In particular, a scheduled
	// audio error carries its own concrete counters through Unwrap; traversing
	// that wrapper merely because it has an Unwrap method would replace the
	// type with the sentinel and break errors.As for the caller.
	if !errors.Is(err, runtimeSession.ErrLiveDurationExceeded) {
		return err
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		return retainNonDurationCauses(joined.Unwrap())
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		cause := wrapped.Unwrap()
		if cause != nil {
			retained := suppressExpectedDuration(cause)
			if retained == nil {
				return nil
			}
			// Preserve the outer operation context while exposing the retained
			// cause to errors.Is/errors.As. This matters when a fmt.Errorf
			// wrapper surrounds an errors.Join(duration, independentFailure).
			return retainedLiveError{message: err.Error(), cause: retained}
		}
	}
	return nil
}

func retainNonDurationCauses(causes []error) error {
	kept := make([]error, 0, len(causes))
	for _, cause := range causes {
		if retained := suppressExpectedDuration(cause); retained != nil {
			kept = append(kept, retained)
		}
	}
	return errors.Join(kept...)
}

type retainedLiveError struct {
	message string
	cause   error
}

func (e retainedLiveError) Error() string { return e.message }

func (e retainedLiveError) Unwrap() error { return e.cause }
