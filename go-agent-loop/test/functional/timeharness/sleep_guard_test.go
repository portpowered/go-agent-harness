package timeharness

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestDiagnosticsChild(t *testing.T) {
	switch os.Getenv("TIMEHARNESS_CHILD") {
	case "sleep":
		s := NewScenario(t, time.Unix(0, 0).UTC(), time.Millisecond)
		sleeper, observer := register(t, s, "sleeper"), register(t, s, "observer")
		sleeper.Run(func() { time.Sleep(time.Hour) })
		observer.Run(func() { _, _ = observer.Observe(1); observer.Complete() })
		if _, err := s.AdvanceTo(1); err != nil {
			t.Fatal(err)
		}
		t.Fatal("sleeping participant unexpectedly crossed the barrier")
	case "stuck":
		s := NewScenario(t, time.Unix(0, 0).UTC(), time.Millisecond)
		register(t, s, "stuck-peer")
		if _, err := s.AdvanceTo(3); err != nil {
			t.Fatal(err)
		}
		t.Fatal("stuck participant unexpectedly crossed the barrier")
	}
}

func TestDiagnosticNegativeControls(t *testing.T) {
	runFailureChild(t, "^TestDiagnosticsChild$", "TIMEHARNESS_CHILD=sleep", "sleeper", "time.Sleep", "forbidden")
	runFailureChild(t, "^TestDiagnosticsChild$", "TIMEHARNESS_CHILD=stuck", "stuck-peer", "target tick 3", "watchdog")
}

func runFailureChild(t *testing.T, testName, marker string, fragments ...string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run="+testName, "-test.v", "-test.timeout=2s")
	cmd.Env = append(os.Environ(), marker)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("negative child unexpectedly passed:\n%s", output)
	}
	for _, fragment := range fragments {
		if !strings.Contains(string(output), fragment) {
			t.Fatalf("child output missing %q:\n%s", fragment, output)
		}
	}
}
