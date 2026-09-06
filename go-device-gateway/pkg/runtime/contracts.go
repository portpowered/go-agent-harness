package runtime

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// CaptureFilter is the narrow feedback gate seam used by the capture worker.
type CaptureFilter interface {
	FilterCapture(context.Context, []int16) ([][]int16, error)
	DiscardHeld()
}

// PlaybackObserver is the narrow feedback gate seam used by the playback
// worker. It keeps device runtime independent from the feedback implementation.
type PlaybackObserver interface {
	WritePlayback(context.Context, []int16, func() error) error
	FeedbackConfirmed() bool
}

var _ CaptureFilter = (*audio.PCM16FeedbackGate)(nil)
var _ PlaybackObserver = (*audio.PCM16FeedbackGate)(nil)
