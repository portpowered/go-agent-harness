// Package sessionfinalization owns ordered, idempotent session cleanup.
package sessionfinalization

import (
	"context"
	"io"
)

type finalizationError string

func (e finalizationError) Error() string { return string(e) }

const ErrPanic finalizationError = "session finalization panicked"

// Callbacks are the host-neutral resource stages for one session plan. The
// service owns ordering, panic recovery, and idempotence; callers only supply
// already-admitted resource operations.
type Callbacks struct {
	CloseCapabilities func() error
	CloseSession      func() error
	CloseBinding      func() error
	CloseRuntime      func() error
	FlushCapture      func() error
	Finalize          func(context.Context, io.Writer) error
	ReleaseCapture    func() error
}

type Finalizer interface {
	SetDeviceBinding(func() error)
	Finish(context.Context, io.Writer, error) error
}

type CancellationFailure struct {
	TerminalReason string
	Provenance     string
}

type CancellationRequest struct {
	Err            error
	SignalReceived bool
	Allowed        []error
	Normalize      func(error) error
	Failure        *CancellationFailure
}

type Service interface {
	New(Callbacks) Finalizer
	CancellationOnly(CancellationRequest) bool
}
