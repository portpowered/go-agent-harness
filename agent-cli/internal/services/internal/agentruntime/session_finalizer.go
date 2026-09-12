package agentruntime
import (
	"context"
	"io"
	sessionfinalization "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfinalization"
	sessionfinalizationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfinalization/wire"
)
// Deprecated: use sessionfinalization.ErrFinalizationPanic.
const ErrSessionFinalizationPanic = sessionfinalization.ErrFinalizationPanic
type sessionRuntimeFinalizer struct {
	plan sessionRuntimePlan
	deviceBinding *RTCDeviceBinding
	delegate sessionfinalization.Finalizer
	setDeviceBinding func(*RTCDeviceBinding)
	finish func(context.Context, io.Writer, error) error
	cleanup func(context.Context, io.Writer) error
}
func newSessionRuntimeFinalizer(plan sessionRuntimePlan) *sessionRuntimeFinalizer {
	f := &sessionRuntimeFinalizer{plan: plan}
	f.delegate = sessionfinalizationwire.NewService().NewFinalizer(sessionfinalization.NewFinalizerRequest(sessionfinalization.OptionalCloser(plan.capabilityCoordinator), plan.closeSession, func() error { return f.deviceBinding.Close() }, sessionfinalization.OptionalCloser(plan.rtcRuntime), plan.flushCapture, plan.finalize, func(ctx context.Context) context.Context { return withSessionTerminalReporter(ctx, f.plan.loop.terminalReporter) }, func() error { return f.plan.captureClaim.release() }, wrapSessionPhaseError, func(err error) error { return wrapSessionRuntimeError(f.plan, err) }))
	f.setDeviceBinding = func(b *RTCDeviceBinding) { f.deviceBinding = b }
	f.finish, f.cleanup = f.delegate.Finish, f.delegate.Cleanup
	return f
}
func invokeSessionFinalizer(cleanup func() error) error { return sessionfinalization.Invoke(cleanup) }
