package audio

import "context"

// AudioSink accepts raw PCM int16 audio frames.
type AudioSink interface {
	// WriteFrame writes exactly one FrameSize-sample frame.
	WriteFrame(ctx context.Context, frame []int16) error
	// Close finalizes the sink and releases any owned resources.
	Close() error
}

// SampleSink is an optional count-aware extension to AudioSink. File and
// replay consumers use it when a response ends between fixed device quanta;
// ordinary device sinks can continue implementing AudioSink alone and must
// reject an unsupported partial tail explicitly.
type SampleSink interface {
	AudioSink
	WriteSamples(context.Context, []int16) error
}
