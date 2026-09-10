// Package probe contains source-only dependency-check fixtures for C25.
package probe

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/mixer"
)

// AllowedRoomGraph is intentionally not built. The checker uses it as a
// positive control for the intended provider-media-to-buffer graph.
func AllowedRoomGraph(scheduler clock.TimerSource, frame audio.PCMFrame) *mixer.Input {
	graph, err := mixer.New(context.Background(), scheduler, mixer.Config{Format: mixer.DefaultFormat()})
	if err != nil {
		return nil
	}
	input, err := graph.AddInput("participant")
	if err != nil {
		return nil
	}
	_ = frame
	return input
}
