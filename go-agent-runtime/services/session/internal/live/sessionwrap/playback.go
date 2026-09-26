package sessionwrap

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// The session runner's local barge-in asks the provider session whether it
// runs its own turn detection, what its input format is and whether provider
// audio is still playing locally. orderedSession and terminalDrainSession
// forward those questions through the embedded messages.SessionCapabilities.

// A turn replay renders provider audio through its own session media, so it
// answers playback questions itself.
func (s *mediaSession) LocalPlayback() messages.LocalPlaybackState {
	activity := s.media.PlaybackActivity()
	return messages.LocalPlaybackState{Active: activity.Active, Level: activity.Level}
}

func (s *mediaSession) InterruptLocalPlayback(context.Context) bool {
	_, interrupted := s.media.InterruptInbound()
	return interrupted
}
