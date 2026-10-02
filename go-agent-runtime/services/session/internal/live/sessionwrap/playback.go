package sessionwrap

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// Every session wrapper relays the provider session's optional capabilities
// through the embedded messages.SessionCapabilities and overrides only the
// ones it answers itself.

var (
	_ messages.BargeInCapableSession = (*orderedSession)(nil)
	_ messages.BargeInCapableSession = (*terminalDrainSession)(nil)
	_ messages.BargeInCapableSession = (*mediaSession)(nil)
)

// A turn replay renders provider audio through its own session media, so it
// answers media and playback questions itself instead of relaying them.

func (s *mediaSession) SupportsRTCMedia() bool { return s != nil && s.media != nil }

func (s *mediaSession) RTCMedia() sharedaudio.MediaEndpoints {
	if s == nil || s.media == nil {
		return sharedaudio.MediaEndpoints{}
	}
	return sharedaudio.MediaEndpoints{Inbound: s.media.Endpoints().Inbound}
}

// RTCMediaWithOptions ignores options: the turn replay fixed its inbound
// framing when it created its media.
func (s *mediaSession) RTCMediaWithOptions(sharedaudio.MediaSessionOptions) sharedaudio.MediaEndpoints {
	return s.RTCMedia()
}

func (s *mediaSession) LocalPlayback() messages.LocalPlaybackState {
	activity := s.media.PlaybackActivity()
	return messages.LocalPlaybackState{Active: activity.Active, Level: activity.Level}
}

func (s *mediaSession) InterruptLocalPlayback(context.Context) bool {
	_, interrupted := s.media.InterruptInbound()
	return interrupted
}

// SyncReceive returns once every provider message queued before the call has
// crossed the turn replay relay into Receive.
func (s *mediaSession) SyncReceive(ctx context.Context) {
	s.SessionCapabilities.SyncReceive(ctx)
	s.barrier.Await(ctx, s.forwarded)
}
