package main

import (
	"bytes"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/testtimeout"
)

func TestRunReportsSuccessfulCommandBudget(t *testing.T) {
	stdout, _, err := runCommandForTest(t, "printf success")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, want := range []string{"success", "Classification: success", "Configured timeout: 2s", "Remaining headroom:"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestRunReportsNonTimeoutCommandFailure(t *testing.T) {
	stdout, stderr, err := runCommandForTest(t, "printf assertion >&2; exit 7")
	if err == nil {
		t.Fatal("run unexpectedly succeeded")
	}
	if !strings.Contains(stderr, "assertion") {
		t.Fatalf("stderr missing command diagnostic:\n%s", stderr)
	}
	if !strings.Contains(stdout, "Classification: test failure (non-timeout command exit)") {
		t.Fatalf("stdout missing non-timeout classification:\n%s", stdout)
	}
	if strings.Contains(stdout, "Classification: timeout") {
		t.Fatalf("non-timeout failure was reported as timeout:\n%s", stdout)
	}
}

func TestRunReportsTimeoutWithoutUsingProductionBudget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell process-group fixture is Unix-specific")
	}
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"--timeout", "40ms",
		"--report-budget",
		"--",
		"sh", "-c", "while true; do :; done",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("run unexpectedly succeeded")
	}
	var timeoutErr *testtimeout.Error
	if !errors.As(err, &timeoutErr) || !timeoutErr.TimedOut {
		t.Fatalf("error = %T %v, want timeout error", err, err)
	}
	if !strings.Contains(stdout.String(), "Classification: timeout (outer test-command budget exceeded)") {
		t.Fatalf("stdout missing timeout classification:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Remaining headroom:") {
		t.Fatalf("stdout missing headroom on timeout:\n%s", stdout.String())
	}
}

func runCommandForTest(t *testing.T, shellCommand string) (string, string, error) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell command fixture is Unix-specific")
	}
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"--timeout", "2s",
		"--report-budget",
		"--",
		"sh", "-c", shellCommand,
	}, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}
