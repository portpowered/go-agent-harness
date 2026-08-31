package main

import (
	"fmt"
	"os"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
)

func main() {
	agentCLI, err := wire.InitializeAgentCLI()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to initialize CLI: %v\n", err)
		os.Exit(1)
	}

	rootCmd := agentCLI.Generate()
	if err := rootCmd.Execute(); err != nil {
		// Cobra owns rendering command execution errors. Keep the process
		// boundary responsible for the exit status so a returned command error
		// is not rendered a second time here.
		os.Exit(1)
	}
}
