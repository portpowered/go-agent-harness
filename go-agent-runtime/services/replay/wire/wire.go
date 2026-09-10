//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

package wire

import (
	"time"

	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/internal/plan"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/internal/strict"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// NewService assembles the replay planning service without opening artifacts.
func NewService() replay.Service {
	wire.Build(plan.New, wire.Bind(new(replay.Service), new(*plan.Service)))
	return nil
}

// newReplayClockFactory creates one deterministic scheduler per strict
// preparation, rooted at the trace origin rather than wall time.
func newReplayClockFactory() strict.ClockFactory {
	return func(origin time.Time) *clock.Deterministic {
		return clock.NewDeterministic(origin, time.Millisecond)
	}
}

// NewStrictService assembles the complete public strict workflow. The private
// implementation receives the canonical plan service through the narrow
// CaptureAdmission contract; it never imports this Wire package.
func NewStrictService() replay.StrictService {
	wire.Build(
		plan.New,
		newReplayClockFactory,
		strict.NewOpenAIRuntimeFactory,
		wire.Struct(new(strict.Dependencies), "*"),
		strict.New,
		wire.Bind(new(replay.CaptureAdmission), new(*plan.Service)),
		wire.Bind(new(replay.StrictService), new(*strict.Service)),
	)
	return nil
}
