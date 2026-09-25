package timeharness

import (
	"errors"
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

// TestDiagnosticsChild is the child process for the forbidden-sleep control:
// its sleeper is abandoned in time.Sleep, so it must not share the parent's
// process. Run directly, it has nothing to do.
func TestDiagnosticsChild(t *testing.T) {
	if os.Getenv("TIMEHARNESS_CHILD") != "sleep" {
		t.Skip("runs only as the forbidden-sleep child process")
	}
	s := New(time.Unix(0, 0).UTC(), time.Millisecond, WithWatchdogTimeout(diagnosticWatchdog))
	sleeper, observer := register(t, s, "sleeper"), register(t, s, "observer")
	sleeper.Run(func() { time.Sleep(time.Hour) })
	observer.Run(func() { _, _ = observer.Observe(1); observer.Complete() }) //nolint:errcheck // The observer only needs to reach the barrier; the test asserts the outcome.
	if _, err := s.AdvanceTo(1); err != nil {
		t.Fatal(err)
	}
	t.Fatal("sleeping participant unexpectedly crossed the barrier")
}

func TestDiagnosticNegativeControls(t *testing.T) {
	t.Run("forbidden sleep", func(t *testing.T) {
		t.Parallel()
		runFailureChild(t, "^TestDiagnosticsChild$", "TIMEHARNESS_CHILD=sleep", "sleeper", "time.Sleep", "forbidden")
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

func runFailureChild(t *testing.T, testName, marker string, fragments ...string) {
	cmd := exec.Command(os.Args[0], "-test.run="+testName, "-test.v", "-test.timeout=10s")
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
