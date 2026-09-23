package service

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

// Execute owns duration-run validation and the lifecycle order surrounding a
// host's startup and invocation effects. Finalization runs even when either
// effect returns an error.
func (s *Service) Execute(request sessionduration.ExecutionRequest) (runErr error) {
	ctx := request.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.ValidateDuration(request.MaxDuration); err != nil {
		return err
	}
	clock := selectExecutionClock(request)
	if request.Finalization.Artifacts == nil {
		request.Finalization.Artifacts = s.ArtifactsFromContext(ctx)
	}
	finalizer := s.NewFinalizer(request.Finalization)
	defer func() {
		runErr = finalizer.Finish(ctx, request.Output, runErr)
	}()
	if request.Prepare != nil {
		if err := request.Prepare(ctx, request.Output); err != nil {
			return err
		}
	}
	if request.Run == nil {
		return nil
	}
	return request.Run(ctx, request.Output, clock)
}

func selectExecutionClock(request sessionduration.ExecutionRequest) sessionduration.TimerScheduler {
	if request.Clock != nil {
		return request.Clock
	}
	if request.SourceClock != nil {
		return request.SourceClock
	}
	return request.FallbackClock
}
