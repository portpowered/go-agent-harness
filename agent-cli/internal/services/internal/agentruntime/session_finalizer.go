package agentruntime

import (
	"context"
	sf "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfinalization"
	sfw "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfinalization/wire"
	"io"
)

// Deprecated: use sessionfinalization.ErrFinalizationPanic.
const ErrSessionFinalizationPanic = sf.ErrFinalizationPanic

type sessionRuntimeFinalizer struct {
	plan             sessionRuntimePlan
	delegate         sf.Finalizer
	setDeviceBinding func(*RTCDeviceBinding)
	finish           func(context.Context, io.Writer, error) error
	cleanup          func(context.Context, io.Writer) error
}

func newSessionRuntimeFinalizer(plan sessionRuntimePlan) *sessionRuntimeFinalizer {
	f := &sessionRuntimeFinalizer{plan: plan}
	f.delegate = sfw.NewService().NewFinalizer(sfw.NewFinalizerRequest(sfw.OptionalCloser(plan.capabilityCoordinator), plan.closeSession, func() error { return closeRTCDeviceBinding(f.plan.loop.rtcDeviceBinding) }, sfw.OptionalCloser(plan.rtcRuntime), plan.flushCapture, plan.finalize, func(ctx context.Context) context.Context {
		return withSessionTerminalReporter(ctx, f.plan.loop.terminalReporter)
	}, func() error { return f.plan.captureClaim.release() }, wrapSessionPhaseError, func(err error) error { return wrapSessionRuntimeError(f.plan, err) }))
	f.setDeviceBinding = func(b *RTCDeviceBinding) { f.plan.loop.rtcDeviceBinding = b }
	f.finish, f.cleanup = f.delegate.Finish, f.delegate.Cleanup
	return f
}
func invokeSessionFinalizer(cleanup func() error) error { return sfw.Invoke(cleanup) }
