package realtime

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// Surface is the part of a Session that callers of a provider session may
// reach. Providers embed a Surface for its promoted methods and keep the
// *Session itself in a named field, so the skeleton's mutators
// (SetTerminalError, WriteTerminal, WriteEvent, SendQueue, the enqueue and
// loop methods) are not promoted onto the provider session, where any caller
// could reach them with an interface assertion.
type Surface struct{ s *Session }

// Surface returns the caller-facing view of s.
func (s *Session) Surface() Surface { return Surface{s: s} }

// Receive returns the normalized inbound buffer.
func (v Surface) Receive() *messages.TypedBuffer[messages.StreamMessage] { return v.s.Receive() }

// Done returns a channel closed when the session terminates.
func (v Surface) Done() <-chan struct{} { return v.s.Done() }

// Close terminates the session.
func (v Surface) Close() error { return v.s.Close() }

// TerminalError returns the provider-side error that terminated the session.
func (v Surface) TerminalError() error { return v.s.TerminalError() }

// InputDrops reports cumulative drops on the client-to-provider send queue.
func (v Surface) InputDrops() int64 { return v.s.InputDrops() }

// OutputDrops reports cumulative drops on the provider-to-client receive buffer.
func (v Surface) OutputDrops() int64 { return v.s.OutputDrops() }

// FlushOutbound waits until admitted outbound events have been written.
func (v Surface) FlushOutbound(ctx context.Context) error { return v.s.FlushOutbound(ctx) }

// RTCMedia returns the session's RTC media endpoints.
func (v Surface) RTCMedia() sharedaudio.MediaEndpoints { return v.s.RTCMedia() }

// RTCMediaWithOptions returns the session's RTC media endpoints with options.
func (v Surface) RTCMediaWithOptions(options sharedaudio.MediaSessionOptions) sharedaudio.MediaEndpoints {
	return v.s.RTCMediaWithOptions(options)
}

// LocalPlayback reports local RTC playback activity.
func (v Surface) LocalPlayback() messages.LocalPlaybackState { return v.s.LocalPlayback() }

// InterruptLocalPlayback discards local RTC playback.
func (v Surface) InterruptLocalPlayback(ctx context.Context) bool {
	return v.s.InterruptLocalPlayback(ctx)
}

// InputAudioSampleRate reports the provider input audio rate.
func (v Surface) InputAudioSampleRate() int { return v.s.InputAudioSampleRate() }
