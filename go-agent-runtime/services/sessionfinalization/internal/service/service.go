// Package service contains the private implementation of the public session
// finalization contract.
package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfinalization"
)

// Service is the private implementation exposed through the dedicated Wire
// package. Its value carries no process state; each boundary owns its own
// once-only lifecycle state.
type Service struct{}

func New() *Service { return &Service{} }

func (s *Service) NewFinalizer(req sessionfinalization.FinalizerRequest) sessionfinalization.Finalizer {
	return &finalizer{req: req}
}

func (s *Service) NewTerminationBoundary(ctx context.Context, req sessionfinalization.TerminationRequest) sessionfinalization.TerminationBoundary {
	if ctx == nil {
		ctx = context.Background()
	}
	return &terminationBoundary{ctx: ctx, req: req}
}

func (s *Service) SIGINTErrorOnly(err error, options sessionfinalization.ErrorTreeOptions) bool {
	return errorTreeOnly(err, cancellationErrors(options))
}

func (s *Service) SIGINTCancellationOnly(err error, intent sessionfinalization.SIGINTIntent, options sessionfinalization.ErrorTreeOptions) bool {
	return safeSIGINTReceived(intent) && s.SIGINTErrorOnly(err, options)
}

func (s *Service) SIGINTCleanForObserver(err error, intent sessionfinalization.SIGINTIntent, failure *sessionfinalization.ObserverFailure, options sessionfinalization.ErrorTreeOptions) bool {
	return s.SIGINTCancellationOnly(err, intent, options) && s.SIGINTObserverFailureOnly(failure)
}

func (s *Service) SIGINTObserverFailureOnly(failure *sessionfinalization.ObserverFailure) bool {
	return failure == nil || (failure.TerminalReason == sessionfinalization.TerminalReasonCancellation && failure.Provenance == sessionfinalization.TerminalProvenanceLoop)
}

type finalizer struct {
	req  sessionfinalization.FinalizerRequest
	once sync.Once
	err  error
}

func (f *finalizer) Finish(ctx context.Context, out io.Writer, primary error) error {
	if f == nil {
		return primary
	}
	return errors.Join(primary, f.Cleanup(ctx, out))
}

func (f *finalizer) Cleanup(ctx context.Context, out io.Writer) error {
	if f == nil {
		return nil
	}
	f.once.Do(func() { f.err = f.cleanup(ctx, out) })
	return f.err
}

func (f *finalizer) cleanup(ctx context.Context, out io.Writer) error {
	ctx = nonNilContext(ctx)
	if out == nil {
		out = io.Discard
	}
	var errs []error
	add := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	add(f.phase("close session capabilities", call(f.req.CloseCapabilities)))
	add(f.phase("close WebRTC provider session", call(f.req.CloseSession)))
	add(f.phase("close RTC device binding", call(f.req.CloseDevice)))
	add(f.phase("close WebRTC runtime", call(f.req.CloseRuntime)))
	flushErr := call(f.req.FlushCapture)
	if flushErr != nil {
		flushErr = f.phase("flush capture", flushErr)
	}
	add(f.runtime(flushErr))
	if f.req.FinalizeContext != nil {
		var contextErr error
		ctx, contextErr = callContext(f.req.FinalizeContext, ctx)
		add(f.runtime(contextErr))
	}
	add(f.runtime(callFinalize(f.req.Finalize, ctx, out)))
	add(f.runtime(call(f.req.ReleaseCaptureClaim)))
	return errors.Join(errs...)
}

func (f *finalizer) phase(name string, err error) error {
	if err == nil || f.req.PhaseError == nil {
		return err
	}
	return decorate(f.req.PhaseError, name, err)
}

func (f *finalizer) runtime(err error) error {
	if err == nil || f.req.RuntimeError == nil {
		return err
	}
	return decorateRuntime(f.req.RuntimeError, err)
}

func decorate(fn func(string, error) error, name string, err error) (out error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			out = panicError(recovered)
		}
	}()
	if name == "" {
		return fn("", err)
	}
	return fn(name, err)
}

func decorateRuntime(fn func(error) error, err error) (out error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			out = panicError(recovered)
		}
	}()
	return fn(err)
}

type terminationBoundary struct {
	ctx    context.Context
	req    sessionfinalization.TerminationRequest
	once   sync.Once
	result error
}

func (b *terminationBoundary) Terminate(primary error) error {
	if b == nil {
		return primary
	}
	b.once.Do(func() {
		quiesceErr := call(b.req.QuiesceUpstream)
		var waitErr error = sessionfinalization.ErrMissingStragglerDrain
		if b.req.WaitForStragglers != nil {
			waitErr = callDrain(b.req.WaitForStragglers, sessionfinalization.DrainPolicy{
				QuietPeriod: sessionfinalization.DefaultStragglerDrainQuietPeriod,
				WallSafety:  sessionfinalization.DefaultStragglerDrainWallSafety,
			})
		}
		stopErr := call(b.req.StopOwnedResources)
		flushErr := call(b.req.FlushBuffered)
		b.result = errors.Join(primary, quiesceErr, waitErr, stopErr, flushErr, b.ctx.Err())
	})
	return b.result
}

func call(fn sessionfinalization.Cleanup) (err error) {
	if fn == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = panicError(recovered)
		}
	}()
	return fn()
}

func callFinalize(fn func(context.Context, io.Writer) error, ctx context.Context, out io.Writer) (err error) {
	if fn == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = panicError(recovered)
		}
	}()
	return fn(ctx, out)
}

func callContext(fn func(context.Context) context.Context, ctx context.Context) (out context.Context, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			out, err = ctx, panicError(recovered)
		}
	}()
	return fn(ctx), nil
}

func callDrain(fn func(sessionfinalization.DrainPolicy) error, policy sessionfinalization.DrainPolicy) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = panicError(recovered)
		}
	}()
	return fn(policy)
}

func panicError(recovered any) error {
	return fmt.Errorf("%w: %v", sessionfinalization.ErrFinalizationPanic, recovered)
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func cancellationErrors(options sessionfinalization.ErrorTreeOptions) []error {
	if len(options.CancellationErrors) == 0 {
		return (sessionfinalization.ErrorTreeOptions{}).WithDefaults().CancellationErrors
	}
	return options.CancellationErrors
}

func safeSIGINTReceived(intent sessionfinalization.SIGINTIntent) (received bool) {
	if intent == nil {
		return false
	}
	defer func() { _ = recover() }()
	return intent.SIGINTReceived()
}

func errorTreeOnly(err error, allowed []error) bool {
	if err == nil {
		return true
	}
	if provider, ok := err.(sessionfinalization.CancellationCauseProvider); ok {
		return cancellationCauseOnly(provider, allowed)
	}
	if causes, ok := joinedCauses(err); ok {
		return allErrorTreeOnly(causes, allowed)
	}
	if cause, ok := wrappedCause(err); ok {
		return errorTreeOnly(cause, allowed)
	}
	return allowedError(err, allowed)
}

func joinedCauses(err error) ([]error, bool) {
	unwrapper, ok := err.(interface{ Unwrap() []error })
	if !ok {
		return nil, false
	}
	return unwrapper.Unwrap(), true
}

func allErrorTreeOnly(causes []error, allowed []error) bool {
	if len(causes) == 0 {
		return false
	}
	for _, cause := range causes {
		if !errorTreeOnly(cause, allowed) {
			return false
		}
	}
	return true
}

func wrappedCause(err error) (error, bool) {
	unwrapper, ok := err.(interface{ Unwrap() error })
	if !ok {
		return nil, false
	}
	return unwrapper.Unwrap(), true
}

func allowedError(err error, allowed []error) bool {
	for _, candidate := range allowed {
		if candidate != nil && errors.Is(err, candidate) {
			return true
		}
	}
	return false
}

func cancellationCauseOnly(provider sessionfinalization.CancellationCauseProvider, allowed []error) (clean bool) {
	var cause error
	func() {
		defer func() { _ = recover() }()
		cause = provider.CancellationCause()
	}()
	if cause == nil {
		return false
	}
	return errorTreeOnly(cause, allowed)
}
