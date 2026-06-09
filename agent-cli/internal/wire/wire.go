//go:build wireinject
// +build wireinject

//go:generate wire

package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/agent"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ToolExecutorSet provides the registry-based tool executor.
var ToolExecutorSet = wire.NewSet(
	tools.NewToolRegistry,
	tools.NewRegistryExecutor,
	wire.Bind(new(messages.ToolExecutor), new(*tools.RegistryExecutor)),
)

// provideNilInferencer supplies no inferencer override for production (config-backed inferencer is used).
func provideNilInferencer() messages.Inferencer { return nil }

// provideNilSessionInferencer supplies no session inferencer override for production.
func provideNilSessionInferencer() messages.SessionInferencer { return nil }

func provideStrictModelValidation() bool { return false }

func provideRelaxedModelValidation() bool { return true }

// ExecutorSet provides the agent executor (config-backed inferencer, injected tool executor).
var ExecutorSet = wire.NewSet(
	services.DefaultToolDefs,
	provideNilInferencer,
	provideStrictModelValidation,
	agent.NewExecutor,
)

// FlagsSet provides global and command-specific CLI flags.
var FlagsSet = wire.NewSet(
	flags.NewGlobalFlags,
	flags.NewAskFlags,
	flags.NewChatFlags,
	flags.NewLoopFlags,
)

// CliSet provides CLI commands, router, and root.
var CliSet = wire.NewSet(
	FlagsSet,
	cli.NewRootCommand,
	cli.NewAskCommand,
	cli.NewChatCommand,
	cli.NewToolCommand,
	cli.NewInteractionCommand,
	cli.NewInteractionReplayCommand,
	cli.NewSessionCommand,
	cli.NewSessionShowCommand,
	cli.NewSessionListCommand,
	cli.NewSessionDeleteCommand,
	cli.NewConfigCommand,
	cli.NewConfigAddLocalCommand,
	cli.NewRouter,
	cli.NewAgentCLI,
)

// InitializeAgentCLI builds the agent CLI with production dependencies (real executor, config-based inferencer).
func InitializeAgentCLI() (*cli.AgentCLI, error) {
	wire.Build(
		ToolExecutorSet,
		ExecutorSet,
		provideStrictModelValidation,
		provideNilSessionInferencer,
		CliSet,
	)
	return nil, nil
}

// InitializeMockAgentCLI builds the agent CLI with injected executor and inferencer for testing.
func InitializeMockAgentCLI(executor messages.ToolExecutor, inferencer messages.Inferencer) (*cli.AgentCLI, error) {
	wire.Build(
		tools.NewToolRegistry,
		wire.NewSet(
			services.DefaultToolDefs,
			provideRelaxedModelValidation,
			provideNilSessionInferencer,
			agent.NewExecutor,
			CliSet,
		),
	)
	return nil, nil
}

// InitializeMockAgentCLIWithSessionInferencer builds the agent CLI with injected one-shot and session inferencers for testing.
func InitializeMockAgentCLIWithSessionInferencer(executor messages.ToolExecutor, inferencer messages.Inferencer, sessionInferencer messages.SessionInferencer) (*cli.AgentCLI, error) {
	wire.Build(
		tools.NewToolRegistry,
		services.DefaultToolDefs,
		provideRelaxedModelValidation,
		agent.NewExecutor,
		CliSet,
	)
	return nil, nil
}

// InitializeAgentCLIWithInferencerOverride builds the CLI with agent.Executor and injected executor/inferencer.
// Use for tests that need real ask path (config dir, AGENTS.md) but capture inference requests.
func InitializeAgentCLIWithInferencerOverride(executor messages.ToolExecutor, inferencer messages.Inferencer) (*cli.AgentCLI, error) {
	wire.Build(
		tools.NewToolRegistry,
		services.DefaultToolDefs,
		provideStrictModelValidation,
		provideNilSessionInferencer,
		agent.NewExecutor,
		CliSet,
	)
	return nil, nil
}
