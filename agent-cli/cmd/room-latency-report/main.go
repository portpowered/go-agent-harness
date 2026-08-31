package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("room-latency-report", flag.ContinueOnError)
	flags.SetOutput(stderr)
	destination := flags.String("out", "", "finalized room evidence directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*destination) == "" {
		return errors.New("room latency report requires -out")
	}
	if len(flags.Args()) != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	report, err := services.ReadRoomLatencyReport(*destination)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}
