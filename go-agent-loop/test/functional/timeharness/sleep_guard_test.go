package timeharness

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestTimeHarnessSleepGuardChild(t *testing.T) {
	if os.Getenv("TIMEHARNESS_SLEEP_CHILD") != "1" {
		return
	}
	s := NewScenario(t, time.Unix(0, 0).UTC(), time.Millisecond, WithWatchdogTimeout(200*time.Millisecond))
	sleeper, err := s.Register("sleeper")
	if err != nil {
		t.Fatal(err)
	}
	observer, err := s.Register("observer")
	if err != nil {
		t.Fatal(err)
	}
	sleeper.Run(func() { time.Sleep(time.Hour) })
	observer.Run(func() {
		_, _ = observer.Observe(1)
		observer.Complete()
	})
	if _, err := s.AdvanceTo(1); err != nil {
		t.Fatal(err)
	}
	t.Fatal("sleeping participant unexpectedly crossed the barrier")
}

func TestSleepGuardNegativeControl(t *testing.T) {
	runFailureChild(t, "^TestTimeHarnessSleepGuardChild$", "TIMEHARNESS_SLEEP_CHILD=1", "sleeper", "time.Sleep", "forbidden")
}

func TestTimeHarnessStuckParticipantChild(t *testing.T) {
	if os.Getenv("TIMEHARNESS_STUCK_CHILD") != "1" {
		return
	}
	s := NewScenario(t, time.Unix(0, 0).UTC(), time.Millisecond, WithWatchdogTimeout(50*time.Millisecond))
	if _, err := s.Register("stuck-peer"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdvanceTo(3); err != nil {
		t.Fatal(err)
	}
	t.Fatal("stuck participant unexpectedly crossed the barrier")
}

func TestStuckParticipantNegativeControl(t *testing.T) {
	runFailureChild(t, "^TestTimeHarnessStuckParticipantChild$", "TIMEHARNESS_STUCK_CHILD=1", "stuck-peer", "target tick 3", "watchdog")
}

func runFailureChild(t *testing.T, testName, marker string, fragments ...string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run="+testName, "-test.v", "-test.timeout=2s")
	command.Env = append(os.Environ(), marker)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("negative child unexpectedly passed:\n%s", output)
	}
	text := string(output)
	for _, fragment := range fragments {
		if !strings.Contains(text, fragment) {
			t.Fatalf("child output missing %q:\n%s", fragment, text)
		}
	}
}
