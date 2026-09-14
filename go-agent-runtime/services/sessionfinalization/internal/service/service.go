package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfinalization"
)

type Service struct{}

func New() *Service { return &Service{} }

func (s *Service) New(callbacks sessionfinalization.Callbacks) sessionfinalization.Finalizer {
	return &finalizer{callbacks: callbacks}
}

func (s *Service) CancellationOnly(request sessionfinalization.CancellationRequest) bool {
	if !request.SignalReceived || !cancellationErrorOnly(request.Err, request.Allowed, request.Normalize) {
		return false
	}
	if request.Failure == nil {
		return true
	}
	return request.Failure.TerminalReason == "cancellation" && request.Failure.Provenance == "loop"
}

func cancellationErrorOnly(err error, allowed []error, normalize func(error) error) bool {
	if err == nil {
		return true
	}
	if normalize != nil {
		if normalized := normalize(err); normalized != nil && normalized != err {
			return cancellationErrorOnly(normalized, allowed, normalize)
		}
	}
	if unwrapper, ok := err.(interface{ Unwrap() []error }); ok {
		return cancellationCausesOnly(unwrapper.Unwrap(), allowed, normalize)
	}
	if unwrapper, ok := err.(interface{ Unwrap() error }); ok {
		return cancellationErrorOnly(unwrapper.Unwrap(), allowed, normalize)
	}
	return cancellationLeafAllowed(err, allowed)
}

func cancellationCausesOnly(causes []error, allowed []error, normalize func(error) error) bool {
	if len(causes) == 0 {
		return false
	}
	for _, cause := range causes {
		if !cancellationErrorOnly(cause, allowed, normalize) {
			return false
		}
	}
	return true
}

func cancellationLeafAllowed(err error, allowed []error) bool {
	for _, candidate := range allowed {
		if candidate != nil && errors.Is(err, candidate) {
			return true
		}
	}
	return false
}

type finalizer struct {
	callbacks sessionfinalization.Callbacks
	binding   func() error
	once      sync.Once
	mu        sync.Mutex
	err       error
}

func (f *finalizer) SetDeviceBinding(binding func() error) {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.binding = binding
	f.mu.Unlock()
}

func (f *finalizer) Finish(ctx context.Context, out io.Writer, primary error) error {
	if f == nil {
		return primary
	}
	f.once.Do(func() {
		cleanupErr := f.cleanup(ctx, out)
		f.mu.Lock()
		f.err = cleanupErr
		f.mu.Unlock()
	})
	f.mu.Lock()
	cleanupErr := f.err
	f.mu.Unlock()
	return errors.Join(primary, cleanupErr)
}

func (f *finalizer) cleanup(ctx context.Context, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	var errs []error
	appendErr := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	appendErr(wrap("close session capabilities", invoke(f.callbacks.CloseCapabilities)))
	appendErr(wrap("close WebRTC provider session", invoke(f.callbacks.CloseSession)))
	f.mu.Lock()
	binding := f.binding
	f.mu.Unlock()
	appendErr(wrap("close RTC device binding", invoke(binding)))
	appendErr(wrap("close WebRTC runtime", invoke(f.callbacks.CloseRuntime)))
	appendErr(wrap("flush capture", invoke(f.callbacks.FlushCapture)))
	if f.callbacks.Finalize != nil {
		appendErr(invoke(func() error { return f.callbacks.Finalize(ctx, out) }))
	}
	appendErr(invoke(f.callbacks.ReleaseCapture))
	return errors.Join(errs...)
}

func invoke(cleanup func() error) (err error) {
	if cleanup == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: %v", sessionfinalization.ErrPanic, recovered)
		}
	}()
	return cleanup()
}

func wrap(phase string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", phase, err)
}

var _ sessionfinalization.Service = (*Service)(nil)
