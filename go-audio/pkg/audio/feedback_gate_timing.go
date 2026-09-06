package audio

import (
	"context"
	"time"
)

// pcm16DeviceDurationAtRate converts samples using the negotiated device
// rate. Keeping this beside the feedback timing policy makes capture expiry,
// playback position, and acoustic tolerances share one sample-clock rule.
func pcm16DeviceDurationAtRate(samples, rate int) time.Duration {
	if samples <= 0 || rate <= 0 {
		return 0
	}
	// Match PCM16TimedFrame's nearest-nanosecond conversion exactly. Flooring
	// here while the detector rounded can make a following frame appear to
	// start before the prior detector end at ordinary non-integral rates.
	return time.Duration((int64(samples)*int64(time.Second) + int64(rate)/2) / int64(rate))
}

func addPCM16FeedbackDuration(start, duration time.Duration) time.Duration {
	if duration > 0 && start > time.Duration(1<<63-1)-duration {
		return time.Duration(1<<63 - 1)
	}
	return start + duration
}

func pcm16FeedbackContextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
