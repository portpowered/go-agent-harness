package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/testtimeout"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("testtimeout", flag.ContinueOnError)
	flags.SetOutput(stderr)
	timeout := flags.Duration("timeout", 0, "finite outer test-command timeout")
	dir := flags.String("dir", "", "working directory for the test command")
	label := flags.String("label", "agent-cli test command", "diagnostic label for the command")
	if err := flags.Parse(args); err != nil {
		return err
	}
	commandArgs := flags.Args()
	if len(commandArgs) > 0 && commandArgs[0] == "--" {
		commandArgs = commandArgs[1:]
	}
	if len(commandArgs) == 0 {
		return fmt.Errorf("testtimeout requires a command after --")
	}
	if *timeout <= 0 {
		return fmt.Errorf("testtimeout requires a positive finite timeout, got %s", *timeout)
	}

	result, err := testtimeout.Run(context.Background(), testtimeout.Config{
		Command: commandArgs[0],
		Args:    commandArgs[1:],
		Dir:     *dir,
		Label:   strings.TrimSpace(*label),
		Timeout: *timeout,
		Stdout:  stdout,
		Stderr:  stderr,
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("test command exited with status %d", result.ExitCode)
	}
	return nil
}
