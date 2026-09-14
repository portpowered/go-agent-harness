//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

package wire

import (
	"context"

	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal/internal/service"
)

func WithReporter(ctx context.Context, reporter sessionterminal.Reporter) context.Context {
	return service.WithReporter(ctx, reporter)
}

func ReporterFromContext(ctx context.Context) sessionterminal.Reporter {
	return service.ReporterFromContext(ctx)
}

func HasIndependentFailure(err error) bool { return service.HasIndependentFailure(err) }

// NewService assembles one stateless terminal policy service.
func NewService() sessionterminal.Service {
	wire.Build(service.New, wire.Bind(new(sessionterminal.Service), new(*service.Service)))
	return nil
}

// NewReporter constructs one invocation-local terminal reporter.
func NewReporter() sessionterminal.Reporter {
	wire.Build(service.NewReporter)
	return nil
}

func NewTerminationBoundary(options sessionterminal.TerminationOptions) sessionterminal.TerminationBoundary {
	wire.Build(service.NewTerminationBoundary)
	return nil
}
