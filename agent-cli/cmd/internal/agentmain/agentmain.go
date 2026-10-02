// Package agentmain is the process entry point shared by the cmd/agent and
// cmd/yui binaries, which differ only in their name.
package agentmain

import (
	"context"
	"fmt"
	"os"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
)

// Run builds the production CLI, executes args and returns the process exit
// code. Cobra owns rendering command execution errors; nil streams keep
// cobra's process defaults (os.Stdin, os.Stdout, os.Stderr).
func Run(args []string) int {
	ctx := context.Background()
	agentCLI, err := wire.InitializeAgentCLI(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to initialize CLI: %v\n", err)
		return 1
	}
	return cli.Execute(ctx, agentCLI.Generate(), cli.Invocation{Args: args})
}
