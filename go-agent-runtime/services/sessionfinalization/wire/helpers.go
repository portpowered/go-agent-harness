package wire

import (
	"context"
	"fmt"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfinalization"
)

// OptionalCloser turns a nil-or-closable host resource into a cleanup
// callback. Composition helpers live in Wire so the service root remains a
// type-and-value-only contract.
func OptionalCloser(value any) sessionfinalization.Cleanup {
	if value == nil {
		return nil
	}
	closer, ok := value.(interface{ Close() error })
	if !ok {
		return nil
	}
	return closer.Close
}

// NewFinalizerRequest adapts callback variables into the public request
// without exposing the private implementation to the host.
func NewFinalizerRequest(closeCapabilities, closeSession, closeDevice, closeRuntime, flushCapture sessionfinalization.Cleanup, finalize func(context.Context, io.Writer) error, finalizeContext func(context.Context) context.Context, releaseCaptureClaim sessionfinalization.Cleanup, phaseError func(string, error) error, runtimeError func(error) error) sessionfinalization.FinalizerRequest {
	return sessionfinalization.FinalizerRequest{
		CloseCapabilities:   closeCapabilities,
		CloseSession:        closeSession,
		CloseDevice:         closeDevice,
		CloseRuntime:        closeRuntime,
		FlushCapture:        flushCapture,
		Finalize:            finalize,
		FinalizeContext:     finalizeContext,
		ReleaseCaptureClaim: releaseCaptureClaim,
		PhaseError:          phaseError,
		RuntimeError:        runtimeError,
	}
}

// Invoke runs one cleanup callback with the contract's panic conversion.
func Invoke(cleanup sessionfinalization.Cleanup) (err error) {
	if cleanup == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: %v", sessionfinalization.ErrFinalizationPanic, recovered)
		}
	}()
	return cleanup()
}

// AdaptDrain converts a host-local drain policy without exposing that policy
// type through the reusable contract. A nil callback remains nil.
func AdaptDrain[T any](wait func(T) error, convert func(sessionfinalization.DrainPolicy) T) func(sessionfinalization.DrainPolicy) error {
	if wait == nil {
		return nil
	}
	return func(policy sessionfinalization.DrainPolicy) error { return wait(convert(policy)) }
}
