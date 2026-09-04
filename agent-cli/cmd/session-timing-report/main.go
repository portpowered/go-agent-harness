package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/sessiontiming"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
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
	capture, err := gwtesting.LoadSessionCapture(*capturePath)
	if err != nil {
		return err
	}
	report, err := sessiontiming.AnalyzeCapture(capture)
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
