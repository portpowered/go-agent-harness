package main

import (
	"context"
	"fmt"
	"os"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
)

func main() {
	agentCLI, err := wire.InitializeAgentCLI()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to initialize CLI: %v\n", err)
		os.Exit(1)
	}

	// Cobra owns rendering command execution errors; the process boundary
	// only turns the status into the exit code. Nil streams keep cobra's
	// process defaults (os.Stdin, os.Stdout, os.Stderr).
	rootCmd := agentCLI.Generate()
	os.Exit(cli.Execute(context.Background(), rootCmd, cli.Invocation{Args: os.Args[1:]}))
}
