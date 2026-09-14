package service

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

func normalizeOptions(options sessionduration.Options) (sessionduration.Options, bool, error) {
	if options.MaxDuration < 0 {
		return options, false, &sessionduration.InvalidDurationError{Duration: options.MaxDuration}
	}
	if options.Liveness.Timeout < 0 || options.Retry.MaxRetries < 0 || options.Retry.DefaultDelay < 0 || options.Retry.MaxDelay < 0 {
		return options, false, fmt.Errorf("session duration policy values must be non-negative")
	}
	if options.Liveness.Timeout > 0 {
		options.Liveness.Enabled = true
	}
	if options.Retry.MaxRetries != 0 || options.Retry.DefaultDelay != 0 || options.Retry.MaxDelay != 0 {
		options.Retry.Enabled = true
	}
	if options.Context == nil {
		options.Context = context.Background()
	}
	if options.LivenessClock == nil {
		options.LivenessClock = options.Clock
	}
	needsClock := options.MaxDuration > 0 || options.Liveness.Enabled || options.Retry.Enabled
	return options, needsClock, nil
}
