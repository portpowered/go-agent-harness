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
		// Cobra owns customer-facing error rendering; this boundary owns only
		// the process exit status.
		os.Exit(1)
	}
}
