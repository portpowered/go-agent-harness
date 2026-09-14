package agentruntime

import (
	"context"
	"errors"
	"fmt"
)

// joinSessionTerminationErrors preserves real failures while treating the
// coordinated cancellation used to stop the session as an expected result.
func joinSessionTerminationErrors(runErr, producerErr error) error {
	var errs []error
	if runErr != nil && !isSessionCancellation(runErr) {
		errs = append(errs, fmt.Errorf("session error: %w", runErr))
	}
	if producerErr != nil && (!isSessionCancellation(producerErr) || errors.Is(producerErr, ErrRuntimeAudioInputEndOfTurnLost)) {
		errs = append(errs, producerErr)
	}
	return errors.Join(errs...)
}

func isSessionCancellation(err error) bool {
	return err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
