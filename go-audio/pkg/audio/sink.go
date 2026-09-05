package audio

import "context"

// AudioSink accepts raw PCM int16 audio frames.
type AudioSink interface {
	// WriteFrame writes exactly one FrameSize-sample frame.
	WriteFrame(ctx context.Context, frame []int16) error
	// Close finalizes the sink and releases any owned resources.
	Close() error
}
