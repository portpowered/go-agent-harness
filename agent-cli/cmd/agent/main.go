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
	command, err := rootCmd.ExecuteC()
	if err != nil {
		// Cobra renders ordinary command errors. Commands that explicitly set
		// SilenceErrors defer rendering to this process boundary instead, so
		// every production command has exactly one error owner.
		if command == nil || command.SilenceErrors || rootCmd.SilenceErrors {
			fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		}
		os.Exit(1)
	}
}
