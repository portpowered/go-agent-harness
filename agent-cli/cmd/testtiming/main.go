package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/portpowered/agent-cli/internal/testtiming"
)

func main() {
	if err := run(os.Stdout, os.Stderr, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(stdout, stderr io.Writer, args []string) error {
	flags := flag.NewFlagSet("testtiming", flag.ContinueOnError)
	flags.SetOutput(stderr)
	goCommand := flags.String("go", "go", "Go command to invoke")
	timeout := flags.String("timeout", "120s", "go test package timeout")
	top := flags.Int("top", 20, "number of slow package and test entries to print")
	if err := flags.Parse(args); err != nil {
		return err
	}

	ctx := context.Background()
	preflightStarted := time.Now()
	preflightResult := runCommand(ctx, *goCommand, "test", "./...", "-run", "^$", "-count=1", "-timeout", *timeout)
	preflightDuration := time.Since(preflightStarted)
	if preflightResult.err != nil {
		_, _ = stderr.Write(preflightResult.combined)
		return fmt.Errorf("preflight no-test go test failed: %w", preflightResult.err)
	}

	suiteStarted := time.Now()
	suiteResult := runCommand(ctx, *goCommand, "test", "./...", "-json", "-v", "-count=1", "-timeout", *timeout)
	suiteDuration := time.Since(suiteStarted)

	summary, err := testtiming.Parse(bytes.NewReader(suiteResult.combined))
	if err != nil {
		return fmt.Errorf("parse go test JSON: %w", err)
	}
	if err := testtiming.WriteReport(stdout, summary, preflightDuration, suiteDuration, *top, suiteResult.exitCode); err != nil {
		return fmt.Errorf("write timing report: %w", err)
	}
	if suiteResult.err != nil {
		_, _ = stderr.Write(suiteResult.combined)
		return fmt.Errorf("suite go test failed: %w", suiteResult.err)
	}
	return nil
}

type commandResult struct {
	combined []byte
	exitCode int
	err      error
}

func runCommand(ctx context.Context, name string, args ...string) commandResult {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	result := commandResult{
		combined: output,
		exitCode: 0,
		err:      err,
	}
	if err == nil {
		return result
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.exitCode = exitErr.ExitCode()
		return result
	}
	result.exitCode = -1
	return result
}
