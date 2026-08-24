//go:build wireinject
// +build wireinject

//go:generate wire

package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/agent"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func provideModelValidation(
	relaxModelValidation bool,
	observer assemblyObserver,
	toolExecutor messages.ToolExecutor,
	transportDialer transport.Dialer,
	deviceRegistry DeviceRegistry,
	audioSource AudioSource,
	audioSink AudioSink,
	clockSource Clock,
	inferencer messages.Inferencer,
	sessionInferencer messages.SessionInferencer,
) []bool {
	if observer != nil {
		observer(compositionValues{
			toolExecutor:      toolExecutor,
			transportDialer:   transportDialer,
			deviceRegistry:    deviceRegistry,
			audioSource:       audioSource,
			audioSink:         audioSink,
			clockSource:       clockSource,
			inferencer:        inferencer,
			sessionInferencer: sessionInferencer,
		})
	}
	return []bool{relaxModelValidation}
}

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
	cli.NewProbeCommand,
	cli.NewProbeRunCommand,
	cli.NewSessionCommand,
	cli.NewSessionShowCommand,
	cli.NewSessionListCommand,
	cli.NewSessionDeleteCommand,
	cli.NewConfigCommand,
	cli.NewConfigAddLocalCommand,
	cli.NewRouter,
	cli.NewAgentCLI,
)

// assembleAgentCLI is the generated implementation shared by production and
// mock composition. Its parameters are explicit so the generated graph cannot
// hide a dependency behind a bag or locator.
func assembleAgentCLI(
	toolExecutor messages.ToolExecutor,
	transportDialer transport.Dialer,
	deviceRegistry DeviceRegistry,
	audioSource AudioSource,
	audioSink AudioSink,
	clockSource Clock,
	toolDefs []messages.ToolDefinition,
	inferencer messages.Inferencer,
	sessionInferencer messages.SessionInferencer,
	relaxModelValidation bool,
	observer assemblyObserver,
) (*cli.AgentCLI, error) {
	wire.Build(CliSet, provideModelValidation, agent.NewExecutor)
	return nil, nil
}
