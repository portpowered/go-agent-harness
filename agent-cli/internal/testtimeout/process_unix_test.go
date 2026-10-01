//go:build !windows

package testtimeout

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func processRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	if _, err := os.Stat("/proc/" + itoa(pid) + "/stat"); err == nil {
		data, readErr := os.ReadFile("/proc/" + itoa(pid) + "/stat")
		if readErr == nil && isZombie(data) {
			return false
		}
		return true
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func isZombie(stat []byte) bool {
	lastClose := -1
	for index, value := range stat {
		if value == ')' {
			lastClose = index
		}
	}
	return lastClose >= 0 && lastClose+2 < len(stat) && stat[lastClose+2] == 'Z'
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits [20]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		index--
		digits[index] = '-'
	}
	return string(digits[index:])
}

// testBlockedChildFailsClosedAndCleansDescendants waits until the blocked
// parent has started its child and grandchild, then ends the run through the
// shared termination path and proves every descendant exits.
// TestTimeoutContractBlockedChildCleansDescendants is the process-group case
// of TestTimeoutContract; Windows covers it with its taskkill implementation.
func TestTimeoutContractBlockedChildCleansDescendants(t *testing.T) {
	t.Parallel()
	fixtureBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve fixture binary: %v", err)
	}
	testBlockedChildFailsClosedAndCleansDescendants(t, fixtureBinary)
}

func testBlockedChildFailsClosedAndCleansDescendants(t *testing.T, fixtureBinary string) {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "blocked-child.markers")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan fixturePIDs, 1)
	readyErr := make(chan error, 1)
	go func() {
		pids, err := waitForFixturePIDs(ctx, marker)
		if err != nil {
			readyErr <- err
			return
		}
		ready <- pids
		cancel()
	}()

	result, runErr := runFixture(ctx, fixtureBinary, marker, "blocked", "TestTimeoutFixtureBlockedChild", fixtureSafetyBudget)
	// The run is over (cancelled on readiness, budget expired, or fixture
	// crashed). Cancel so the readiness poller stops and reports on one of its
	// buffered channels; the guard only bounds a poller that fails to return.
	cancel()
	var pids fixturePIDs
	select {
	case pids = <-ready:
	case err := <-readyErr:
		t.Fatalf("blocked fixture never reported its descendants within %s: %v; run error=%v result=%+v output:\n%s", fixtureSafetyBudget, err, runErr, result, result.Output)
	case <-time.After(fixtureSafetyBudget):
		t.Fatalf("blocked fixture never reported its descendants: readiness poller did not stop; run error=%v result=%+v output:\n%s", runErr, result, result.Output)
	}
	var runnerErr *Error
	if !errors.As(runErr, &runnerErr) || result.TimedOut || runnerErr.TimedOut {
		t.Fatalf("blocked fixture error = %T %v, result=%+v; want a fail-closed cancellation", runErr, runErr, result)
	}
	if result.ExitCode == 0 {
		t.Fatalf("blocked fixture exit code = 0, want non-zero: %+v", result)
	}
	if runnerErr.Termination != descendantsTerminated {
		t.Fatalf("blocked fixture termination = %q, want descendants terminated", runnerErr.Termination)
	}
	for _, want := range []string{
		"fixture=blocked-child",
		"active_test=TestTimeoutFixtureBlockedChild",
		"child_pid=",
		"descendant_pid=",
	} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("blocked diagnostic missing %q:\noutput:\n%s", want, result.Output)
		}
	}
	waitForProcessesToExit(t, pids)
}

type fixturePIDs struct {
	parent     int
	child      int
	descendant int
	grandchild int
}

// waitForFixturePIDs polls the fixture marker until the parent, child and
// descendant have all announced themselves, or ctx ends.
func waitForFixturePIDs(ctx context.Context, path string) (fixturePIDs, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			text := string(data)
			pids := fixturePIDs{
				parent:     markerPID(text, "parent_pid"),
				child:      markerPID(text, "child_pid"),
				descendant: markerPID(text, "descendant_pid"),
				grandchild: markerPID(text, "grandchild_pid"),
			}
			if pids.parent > 0 && pids.child > 0 && pids.descendant > 0 {
				return pids, nil
			}
		}
		select {
		case <-ctx.Done():
			return fixturePIDs{}, fmt.Errorf("marker %s incomplete: %w", path, ctx.Err())
		case <-ticker.C:
		}
	}
}

func markerPID(text, name string) int {
	for _, field := range strings.Fields(text) {
		keyValue := strings.SplitN(field, "=", 2)
		if len(keyValue) != 2 || keyValue[0] != name {
			continue
		}
		pid, err := strconv.Atoi(keyValue[1])
		if err != nil {
			return 0
		}
		return pid
	}
	return 0
}

func waitForProcessesToExit(t *testing.T, pids fixturePIDs) {
	t.Helper()
	// The descendants are not children of this process, so their exit cannot be
	// awaited directly; observe it on a ticker until the deadline.
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	observe := time.NewTicker(20 * time.Millisecond)
	defer observe.Stop()
	for {
		if !processRunning(pids.parent) && !processRunning(pids.child) && !processRunning(pids.descendant) && !processRunning(pids.grandchild) {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("fixture processes remain after timeout: %+v", pids)
		case <-observe.C:
		}
	}
}
