package main

import (
	"context"
	"fmt"
	"os"

	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	runtimeReplayWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/wire"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: strict-replay <bundle-directory>")
		os.Exit(2)
	}
	result, err := runtimeReplayWire.NewStrictService().Run(context.Background(), os.Stdout, runtimeReplay.StrictRequest{BundlePath: os.Args[1]})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "Replay verified: %d wire events, %d tool calls. Device execution: %t.\n", result.WireEvents, result.ToolCalls, result.Scope.DeviceExecution)
}
