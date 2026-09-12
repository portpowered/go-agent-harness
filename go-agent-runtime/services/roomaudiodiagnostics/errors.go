package roomaudiodiagnostics

import "fmt"

type contractError string

func (e contractError) Error() string { return string(e) }

// ErrInvalidDisposition identifies a disposition outside the stable contract.
const ErrInvalidDisposition contractError = "room audio ingress disposition is invalid"

// ErrFinished identifies a write submitted after summary publication began.
const ErrFinished contractError = "room audio ingress diagnostics is finished"

// InvalidDispositionError retains the rejected value while preserving
// errors.Is(err, ErrInvalidDisposition) for host error classification.
type InvalidDispositionError struct {
	Value Disposition
}

func (e InvalidDispositionError) Error() string {
	return fmt.Sprintf("%v: %q", ErrInvalidDisposition, e.Value)
}

func (e InvalidDispositionError) Unwrap() error { return ErrInvalidDisposition }
