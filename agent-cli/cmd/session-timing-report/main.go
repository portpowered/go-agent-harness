package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	runtimeReplayWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/wire"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fatal(err)
	}
}

func run(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("session-timing-report", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	capturePath := flags.String("capture", "", "integrity-protected session capture to analyze")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *capturePath == "" {
		return fmt.Errorf("session timing report requires -capture")
	}
	report, err := runtimeReplayWire.NewService().AnalyzeTiming(context.Background(), *capturePath)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, "session-timing-report:", err)
	os.Exit(1)
}
