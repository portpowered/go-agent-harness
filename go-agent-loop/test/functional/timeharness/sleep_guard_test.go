package timeharness

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// diagnosticWatchdog shortens the generation watchdog for the negative
// controls. It only has to outlast a participant goroutine reaching
// time.Sleep, not a real tick of work.
const diagnosticWatchdog = 100 * time.Millisecond

// sleepChildEnv selects the forbidden-sleep child mode of this test binary.
const sleepChildEnv = "TIMEHARNESS_CHILD"

// TestMain runs the forbidden-sleep child when this test binary is
// re-executed as one, and the package tests otherwise.
func TestMain(m *testing.M) {
	if os.Getenv(sleepChildEnv) == "sleep" {
		os.Exit(runForbiddenSleepChild(os.Stderr))
	}
	os.Exit(m.Run())
}

// runForbiddenSleepChild is the child process for the forbidden-sleep
// control: its sleeper is abandoned in time.Sleep, so it must not share the
// parent's process. It exits non-zero with the harness diagnosis.
func runForbiddenSleepChild(stderr io.Writer) int {
	s := New(time.Unix(0, 0).UTC(), time.Millisecond, WithWatchdogTimeout(diagnosticWatchdog))
	sleeper, err := s.Register("sleeper")
	if err != nil {
		return reportChildFailure(stderr, err.Error())
	}
	observer, err := s.Register("observer")
	if err != nil {
		return reportChildFailure(stderr, err.Error())
	}
	sleeper.Run(func() { time.Sleep(time.Hour) })                            //nolint:forbidigo // The forbidden sleep is the subject of this negative control.
	observer.Run(func() { _, _ = observer.Observe(1); observer.Complete() }) //nolint:errcheck // The observer only needs to reach the barrier; the test asserts the outcome.
	if _, err := s.AdvanceTo(1); err != nil {
		return reportChildFailure(stderr, err.Error())
	}
	return reportChildFailure(stderr, "sleeping participant unexpectedly crossed the barrier")
}

// reportChildFailure writes the child's diagnosis and returns its failing
// exit code; the exit code is the result even if the write fails.
func reportChildFailure(stderr io.Writer, diagnosis string) int {
	_, _ = fmt.Fprintln(stderr, diagnosis) //nolint:errcheck // Best-effort diagnostic; the exit code is the result.
	return 1
}

func TestDiagnosticNegativeControls(t *testing.T) {
	t.Parallel()
	t.Run("forbidden sleep", func(t *testing.T) {
		t.Parallel()
		runFailureChild(t, sleepChildEnv+"=sleep", "sleeper", "time.Sleep", "forbidden")
	})
	t.Run("stuck participant", func(t *testing.T) {
		t.Parallel()
		s := New(time.Unix(0, 0).UTC(), time.Millisecond, WithWatchdogTimeout(diagnosticWatchdog))
		defer s.Close()
		register(t, s, "stuck-peer")
		_, err := s.AdvanceTo(3)
		var harnessErr *HarnessError
		if !errors.As(err, &harnessErr) || harnessErr.Kind != "stuck participant" {
			t.Fatalf("AdvanceTo error = %v, want a stuck participant diagnosis", err)
		}
		for _, fragment := range []string{"stuck-peer", "target tick 3", "watchdog"} {
			if !strings.Contains(err.Error(), fragment) {
				t.Fatalf("diagnosis %q missing %q", err, fragment)
			}
		}
	})
}

func runFailureChild(t *testing.T, marker string, fragments ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.timeout=10s")
	cmd.Env = append(os.Environ(), marker)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("negative child unexpectedly passed:\n%s", output)
	}
	text := string(output)
	for _, fragment := range fragments {
		if !strings.Contains(text, fragment) {
			t.Fatalf("child output missing %q:\n%s", fragment, output)
		}
	}
}
