package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

// ErrSessionFinalizationPanic identifies a panic from a non-capability
// finalizer callback. A cleanup panic must not skip the remaining ordered
// stages or escape the session command.
var ErrSessionFinalizationPanic = errors.New("session finalization panicked")

// sessionRuntimeFinalizer is the one post-loop owner for a planned session.
// The loop is responsible for stopping admission and reconciling pending
// provider work; this owner then performs browser capability cleanup, runtime
// teardown, provider capture flushing, and the mode-specific finalization.
//
// A plan can be reached through several nested command wrappers. Keeping this
// finalizer idempotent means those wrappers can retain pre-plan guards without
// creating a second cleanup owner after a plan has started.
type sessionRuntimeFinalizer struct {
	plan          sessionRuntimePlan
	deviceBinding *RTCDeviceBinding

	once sync.Once
	mu   sync.Mutex
	err  error
}

func newSessionRuntimeFinalizer(plan sessionRuntimePlan) *sessionRuntimeFinalizer {
	return &sessionRuntimeFinalizer{plan: plan}
}

func (f *sessionRuntimeFinalizer) setDeviceBinding(binding *RTCDeviceBinding) {
	if f != nil {
		f.deviceBinding = binding
	}
}

// finish runs the complete ordered shutdown once and joins its failures with
// the primary session result. Calling finish again returns the same cleanup
// failures without invoking any resource a second time.
func (f *sessionRuntimeFinalizer) finish(ctx context.Context, out io.Writer, primary error) error {
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

func (f *sessionRuntimeFinalizer) cleanup(ctx context.Context, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}

	var errs []error
	appendErr := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	// The capability hook owns browser admission, pending invocations, target
	// detachment, semantic browser recording, and any harness-owned browser
	// transport. It must run before provider/runtime teardown so its final
	// browser event can still be recorded against the session.
	if f.plan.capabilityCoordinator != nil {
		appendErr(wrapSessionPhaseError("close session capabilities", f.plan.capabilityCoordinator.Close()))
	}

	// RTC provider sessions close their provider session before releasing the
	// runtime data plane. The callback is idempotent because the agent loop's
	// model runner also closes the session it connected.
	if f.plan.closeSession != nil {
		appendErr(wrapSessionPhaseError("close WebRTC provider session", invokeSessionFinalizer(f.plan.closeSession)))
	}
	if f.deviceBinding != nil {
		appendErr(wrapSessionPhaseError("close RTC device binding", invokeSessionFinalizer(f.deviceBinding.Close)))
	}
	if f.plan.rtcRuntime != nil {
		appendErr(wrapSessionPhaseError("close WebRTC runtime", invokeSessionFinalizer(f.plan.rtcRuntime.Close)))
	}

	// Provider capture must be durable before a recording-directory finalizer
	// (owned by the outer recording wrapper) writes its manifest. Attempt the
	// mode finalizer even when flushing fails so one failure cannot hide the
	// other.
	if f.plan.flushCapture != nil {
		appendErr(wrapSessionRuntimeError(f.plan, wrapSessionPhaseError("flush capture", invokeSessionFinalizer(f.plan.flushCapture))))
	}
	if f.plan.finalize != nil {
		finalizeCtx := ctx
		if reporter := f.plan.loop.terminalReporter; reporter != nil {
			finalizeCtx = withSessionTerminalReporter(ctx, reporter)
		}
		appendErr(wrapSessionRuntimeError(f.plan, invokeSessionFinalizer(func() error {
			return f.plan.finalize(finalizeCtx, out)
		})))
	}

	return errors.Join(errs...)
}

func invokeSessionFinalizer(cleanup func() error) (err error) {
	if cleanup == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: %v", ErrSessionFinalizationPanic, recovered)
		}
	}()
	return cleanup()
}
