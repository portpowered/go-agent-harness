// Command mock-tool-agent is the agent CLI with its tool executor replaced
// by the fixture named in YUI_E2E_TOOL_MOCK_FIXTURE (see package mocktool).
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/agent-cli/test/integration/testcmd/mocktool"
)

func main() {
	executor, err := mocktool.LoadFixtureExecutor(os.Getenv(mocktool.FixtureEnvironment))
	if err != nil {
		fatal(err)
	}
	agentCLI, err := wire.InitializeMockAgentCLIWithPorts(mocktool.Ports(executor)...)
	if err != nil {
		fatal(err)
	}
	mocktool.ConfigureHoldTone(agentCLI, os.Getenv(mocktool.DisableHoldToneEnvironment) == "1")
	if code := cli.Execute(context.Background(), agentCLI.Generate(), cli.Invocation{Args: os.Args[1:]}); code != 0 {
		os.Exit(code)
	}
	if err := executor.Verify(); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, "mock-tool-agent:", err)
	os.Exit(1)
}
