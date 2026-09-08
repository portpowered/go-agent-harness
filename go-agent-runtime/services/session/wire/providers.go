//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire assembles the session service and keeps its implementation
// graph private to this service's composition boundary.
package wire

import (
	"github.com/google/wire"
	looplogging "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	agent "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/execution"
	persistence "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/persistence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/service"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

// Dependencies contains the provider-neutral edges for a text session.
// Hosts can supply fakes for deterministic tests or production implementations
// without importing private service packages.
type Dependencies struct {
	ToolExecutor    messages.ToolExecutor
	ToolDefinitions []messages.ToolDefinition
	Inferencer      messages.Inferencer
	RelaxValidation bool
	Resolver        session.Resolver
	Store           session.SessionStore
	TraceStore      session.TraceStore
	ProviderService providers.Service
	ToolService     tools.Service
	Logger          looplogging.Logger
}

// NewService assembles the session implementation and returns only its public
// service contract.
func NewService(deps Dependencies) session.Service {
	wire.Build(newExecutor, newService)
	return nil
}

// NewFileStoreFactory assembles the built-in durable store factory. The
// factory is deliberately separate from NewService: embedders may inject a
// remote or in-memory SessionStore, while CLI hosts opt into the canonical
// file-backed adapter at their outer composition edge.
func NewFileStoreFactory() session.FileStoreFactory {
	wire.Build(newFileStoreFactory, wire.Bind(new(session.FileStoreFactory), new(*persistence.Factory)))
	return nil
}

func newFileStoreFactory() *persistence.Factory { return persistence.NewFactory() }

func newExecutor(deps Dependencies) *agent.Executor {
	return agent.NewExecutorWithToolServiceAndLogger(
		deps.ToolService,
		deps.ToolExecutor,
		append([]messages.ToolDefinition(nil), deps.ToolDefinitions...),
		deps.Inferencer,
		deps.Logger,
		deps.RelaxValidation,
	)
}

func newService(deps Dependencies, executor *agent.Executor) session.Service {
	return service.New(service.Dependencies{
		Executor: executor, Resolver: deps.Resolver, Store: deps.Store, TraceStore: deps.TraceStore,
		ProviderService: deps.ProviderService,
	})
}
