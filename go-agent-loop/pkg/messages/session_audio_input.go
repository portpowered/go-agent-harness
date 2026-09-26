package messages

import (
	"context"
	"sync"
)

// SessionAudioInputPolicy controls whether contentful session audio is
// allowed to interrupt the response currently owned by the session runner.
// The zero value and every unknown value are intentionally interrupting so
// legacy callers remain safe by default.
type SessionAudioInputPolicy string

const (
	// SessionAudioInputPolicyDefault preserves the legacy interrupting
	// behavior for input whose origin has not been classified.
	SessionAudioInputPolicyDefault SessionAudioInputPolicy = ""
	// SessionAudioInputPolicyInterrupt marks human/customer or otherwise
	// explicitly interrupting input.
	SessionAudioInputPolicyInterrupt SessionAudioInputPolicy = "interrupt"
	// SessionAudioInputPolicyDoNotInterrupt marks audio from peer agents. It
	// still reaches the provider, but does not cancel its ordinary response.
	SessionAudioInputPolicyDoNotInterrupt SessionAudioInputPolicy = "do_not_interrupt"
)

// InterruptsResponse reports whether this policy permits contentful audio to
// cancel an ordinary in-flight response. Unknown policies use the safe,
// interrupting default.
func (p SessionAudioInputPolicy) InterruptsResponse() bool {
	return p != SessionAudioInputPolicyDoNotInterrupt
}

// SessionAudioInput carries one PCM frame and the interruption intent that
// was established at its admission boundary. Keeping the intent beside the
// bytes means a participant runner never has to infer the source from PCM
// contents or room state.
type SessionAudioInput struct {
	PCM                []byte
	InterruptionPolicy SessionAudioInputPolicy
}

// LocalPlaybackState describes provider audio that is still queued for, or
// audible on, the local playback device. Playback outlives the provider's
// response lifecycle: audio arrives faster than real time, so a response can
// be done while seconds of it are still playing.
type LocalPlaybackState struct {
	// Active reports that provider audio is still queued or audible.
	Active bool
	// Level is the RMS, in PCM16 units, of the audio currently audible. It is
	// the level an acoustic echo of the playback has at a unity-gain
	// microphone and serves as the echo reference for local barge-in.
	Level float64
	// EchoCancelled reports that the capture path removes this playback from
	// the microphone signal (for example a feedback gate or AEC), so the
	// playback level is not an echo reference.
	EchoCancelled bool
}

// SessionLocalPlayback is implemented by sessions that own local playback of
// provider audio.
type SessionLocalPlayback interface {
	LocalPlayback() LocalPlaybackState
	// InterruptLocalPlayback discards playback that has not been heard and
	// truncates the audible conversation item at the heard position. It
	// reports whether any playback was interrupted.
	InterruptLocalPlayback(ctx context.Context) bool
}

// SessionTurnDetection is implemented by sessions that know whether the
// provider detects user speech itself (server VAD). Such a provider cancels
// the active response and reports speech_started, so a client-side energy
// detector must not second-guess it.
type SessionTurnDetection interface {
	ProviderTurnDetection() bool
}

// SessionInputFormat is implemented by sessions that know the sample rate of
// the PCM16 audio the client sends, so audio durations can be measured.
type SessionInputFormat interface {
	InputAudioSampleRate() int
}

// BargeInCapableSession is a session that answers every question the session
// runner's local barge-in asks. Every session wrapper returned to a runner
// implements it (usually by embedding SessionCapabilities), so a wrapper cannot
// silently hide the provider's answers.
type BargeInCapableSession interface {
	Session
	SessionTurnDetection
	SessionLocalPlayback
	SessionInputFormat
	SessionReceiveSyncer
}

// SessionReceiveSyncer is implemented by sessions that relay provider messages
// asynchronously. SyncReceive returns once every provider message queued
// before the call is readable from Receive; it may block while Receive is full,
// so the reader must keep draining Receive while it waits.
type SessionReceiveSyncer interface {
	SyncReceive(ctx context.Context)
}

// SessionCapabilities forwards the optional capabilities the session runner's
// local barge-in reads -- turn detection, local playback and input format --
// from a wrapped session. Every session wrapper embeds it, so a wrapper cannot
// silently hide them from the runner.
type SessionCapabilities struct {
	Wrapped Session
}

var (
	_ SessionTurnDetection = SessionCapabilities{}
	_ SessionLocalPlayback = SessionCapabilities{}
	_ SessionInputFormat   = SessionCapabilities{}
	_ SessionReceiveSyncer = SessionCapabilities{}
)

func (c SessionCapabilities) ProviderTurnDetection() bool {
	detector, ok := c.Wrapped.(SessionTurnDetection)
	return ok && detector.ProviderTurnDetection()
}

func (c SessionCapabilities) LocalPlayback() LocalPlaybackState {
	if playback, ok := c.Wrapped.(SessionLocalPlayback); ok {
		return playback.LocalPlayback()
	}
	return LocalPlaybackState{}
}

func (c SessionCapabilities) InterruptLocalPlayback(ctx context.Context) bool {
	playback, ok := c.Wrapped.(SessionLocalPlayback)
	return ok && playback.InterruptLocalPlayback(ctx)
}

func (c SessionCapabilities) InputAudioSampleRate() int {
	if format, ok := c.Wrapped.(SessionInputFormat); ok {
		return format.InputAudioSampleRate()
	}
	return 0
}

func (c SessionCapabilities) SyncReceive(ctx context.Context) {
	if syncer, ok := c.Wrapped.(SessionReceiveSyncer); ok {
		syncer.SyncReceive(ctx)
	}
}

// RelayBarrier lets a session wrapper that relays provider messages through
// its own goroutine honour SyncReceive. The relay goroutine selects on
// Requests and, for each request, forwards every source message already
// queued before closing the reply channel.
type RelayBarrier struct {
	once     sync.Once
	requests chan chan struct{}
}

// Requests is the channel the relay goroutine selects on.
func (b *RelayBarrier) Requests() chan chan struct{} {
	b.once.Do(func() { b.requests = make(chan chan struct{}) })
	return b.requests
}

// Await asks the relay to publish every queued message and waits until it
// has, the relay has stopped (stopped closes), or ctx ends.
func (b *RelayBarrier) Await(ctx context.Context, stopped <-chan struct{}) {
	reply := make(chan struct{})
	select {
	case b.Requests() <- reply:
	case <-stopped:
		return
	case <-ctx.Done():
		return
	}
	select {
	case <-reply:
	case <-stopped:
	case <-ctx.Done():
	}
}

// Relay forwards every message already queued in source with forward and
// then answers the barrier request reply.
func (b *RelayBarrier) Relay(reply chan struct{}, source *TypedBuffer[StreamMessage], forward func(StreamMessage) bool) {
	defer close(reply)
	for msg, ok := source.Read(); ok; msg, ok = source.Read() {
		if !forward(msg) {
			return
		}
	}
}
