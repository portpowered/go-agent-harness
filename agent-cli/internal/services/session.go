package services

import (
	"context"
	"io"
)

// RunSession validates and runs the session inference command surface.
func RunSession(ctx context.Context, out io.Writer, opts SessionRunOptions) error {
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	plan, err := planSessionRuntime(opts)
	if err != nil {
		return err
	}
	return plan.run(ctx, out)
}
