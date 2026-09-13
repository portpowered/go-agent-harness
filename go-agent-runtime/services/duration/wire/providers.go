//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/duration"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/duration/internal/lifecycle"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func NewService(source clock.Source) duration.Service {
	wire.Build(durationClock, lifecycle.New)
	return nil
}

func NewServiceWithClock(source duration.Clock) duration.Service {
	wire.Build(lifecycle.New)
	return nil
}

func durationClock(source clock.Source) duration.Clock {
	if candidate, ok := source.(duration.Clock); ok {
		return candidate
	}
	return nil
}
