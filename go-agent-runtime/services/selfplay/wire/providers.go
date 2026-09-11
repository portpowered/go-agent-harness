//go:build wireinject
// +build wireinject

//go:generate go run github.com/google/wire/cmd/wire

// Package wire composes the self-play runtime behind its public service.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay/internal/runtime"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// Dependencies are the host ports required by one self-play runtime.
type Dependencies struct {
	Clock          clock.Source
	ModelAdmission providers.ModelAdmission
	Sessions       selfplay.SessionFactory
	Runner         selfplay.SessionRunner
}

func NewService(deps Dependencies) selfplay.Service {
	wire.Build(
		wire.FieldsOf(new(Dependencies), "Clock", "ModelAdmission", "Sessions", "Runner"),
		newRuntimeDependencies,
		runtime.New,
		wire.Bind(new(selfplay.Service), new(*runtime.Service)),
	)
	return nil
}

func newRuntimeDependencies(source clock.Source, admission providers.ModelAdmission, sessions selfplay.SessionFactory, runner selfplay.SessionRunner) selfplay.Dependencies {
	return selfplay.Dependencies{Clock: source, ModelAdmission: admission, Sessions: sessions, Runner: runner}
}
