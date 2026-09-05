package agentruntime

import (
	"context"
	"io"
)

// RunSession validates and runs the session inference command surface.
func RunSession(ctx context.Context, out io.Writer, opts SessionRunOptions) (runErr error) {
	var coordinator *SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return err
	}
	defer func() { _ = claim.release() }()
	plan, err := planSessionRuntime(opts)
	if err != nil {
		return err
	}
	return plan.run(ctx, out)
}
