package sessionwrap

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// The session runner's local barge-in asks the provider session whether it
// runs its own turn detection and whether provider audio is still playing
// locally. Every wrapper forwards those questions to the provider session it
// wraps; a provider that does not answer reports no turn detection and no
// local playback, which keeps the runner's own detector in charge.

func providerTurnDetection(inner messages.Session) bool {
	detector, ok := inner.(messages.SessionTurnDetection)
	return ok && detector.ProviderTurnDetection()
}

func localPlayback(inner messages.Session) messages.LocalPlaybackState {
	if playback, ok := inner.(messages.SessionLocalPlayback); ok {
		return playback.LocalPlayback()
	}
	return messages.LocalPlaybackState{}
}

func interruptLocalPlayback(ctx context.Context, inner messages.Session) bool {
	playback, ok := inner.(messages.SessionLocalPlayback)
	return ok && playback.InterruptLocalPlayback(ctx)
}

func (s *orderedSession) ProviderTurnDetection() bool { return providerTurnDetection(s.inner) }
func (s *orderedSession) LocalPlayback() messages.LocalPlaybackState {
	return localPlayback(s.inner)
}
func (s *orderedSession) InterruptLocalPlayback(ctx context.Context) bool {
	return interruptLocalPlayback(ctx, s.inner)
}

func (s *terminalDrainSession) ProviderTurnDetection() bool { return providerTurnDetection(s.inner) }
func (s *terminalDrainSession) LocalPlayback() messages.LocalPlaybackState {
	return localPlayback(s.inner)
}
func (s *terminalDrainSession) InterruptLocalPlayback(ctx context.Context) bool {
	return interruptLocalPlayback(ctx, s.inner)
}

// A turn replay renders provider audio through its own session media, so it
// answers playback questions itself.
func (s *mediaSession) ProviderTurnDetection() bool { return providerTurnDetection(s.inner) }
func (s *mediaSession) LocalPlayback() messages.LocalPlaybackState {
	activity := s.media.PlaybackActivity()
	return messages.LocalPlaybackState{Active: activity.Active, Level: activity.Level}
}
func (s *mediaSession) InterruptLocalPlayback(context.Context) bool {
	_, interrupted := s.media.InterruptInbound()
	return interrupted
}
