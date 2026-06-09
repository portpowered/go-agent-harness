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
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}
}
