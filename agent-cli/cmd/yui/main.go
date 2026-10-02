package main

import (
	"os"

	"github.com/portpowered/go-agent-harness/agent-cli/cmd/internal/agentmain"
)

func main() {
	os.Exit(agentmain.Run(os.Args[1:]))
}
