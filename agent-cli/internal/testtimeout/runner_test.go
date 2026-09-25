package testtimeout

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	timeoutFixturePackage = "./internal/testtimeout/testdata/blockedchild"
	// fixtureSafetyBudget is the outer bound for runs that finish on their own
	// or are cancelled once the fixture reports readiness. It only fails a run
	// that never becomes ready, so it can be generous for cold or contended
	// workers without slowing the passing path.
	fixtureSafetyBudget = 8 * time.Second
	// expiryBudget is the real timeout exercised by the budget-expiry case.
	// That case asserts nothing about startup, so a short budget cannot flake
	// on a slow fixture start.
	expiryBudget        = 300 * time.Millisecond
	fixtureBuildTimeout = 2 * time.Minute
)

// TestTimeoutContract compiles the fixture once and runs every contract case
// against it in parallel.
func TestTimeoutContract(t *testing.T) {
	fixtureBinary := buildTimeoutFixture(t, moduleRootPath(t))

	t.Run("BudgetExpiryFailsClosed", func(t *testing.T) {
		t.Parallel()
		testBudgetExpiryFailsClosed(t, fixtureBinary)
	})
	t.Run("BlockedChildFailsClosedAndCleansDescendants", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("process-group fixture contract is covered by the Windows taskkill implementation")
		}
		t.Parallel()
		testBlockedChildFailsClosedAndCleansDescendants(t, fixtureBinary)
	})
	t.Run("SuccessControlUsesSameBoundary", func(t *testing.T) {
		t.Parallel()
		testSuccessControlUsesSameBoundary(t, fixtureBinary)
	})
}

// testBudgetExpiryFailsClosed proves the real timer path: a command that
// never finishes is terminated when its finite budget expires and reported
// as a timeout with the cleanup outcome.
func testBudgetExpiryFailsClosed(t *testing.T, fixtureBinary string) {
	marker := filepath.Join(t.TempDir(), "expiry.markers")
	result, runErr := runFixture(context.Background(), fixtureBinary, marker, "grandchild", "TestTimeoutFixtureGrandchild", expiryBudget)
	var timeoutErr *Error
	if !errors.As(runErr, &timeoutErr) || !result.TimedOut || !timeoutErr.TimedOut || timeoutErr.Timeout != expiryBudget {
		t.Fatalf("expired fixture error = %T %v, result=%+v; want timeout boundary", runErr, runErr, result)
	}
	if result.ExitCode == 0 {
		t.Fatalf("expired fixture exit code = 0, want non-zero: %+v", result)
	}
	if result.Duration < expiryBudget || result.Duration >= expiryBudget+waitAfterTermination {
		t.Fatalf("expired fixture duration = %s, want the %s budget plus at most one cleanup grace", result.Duration, expiryBudget)
	}
	for _, want := range []string{"timed out after " + expiryBudget.String(), "descendants terminated"} {
		if !strings.Contains(runErr.Error(), want) {
			t.Fatalf("timeout diagnostic missing %q: %v", want, runErr)
		}
	}
}

// testBlockedChildFailsClosedAndCleansDescendants waits until the blocked
// parent has started its child and grandchild, then ends the run through the
// shared termination path and proves every descendant exits.
func testBlockedChildFailsClosedAndCleansDescendants(t *testing.T, fixtureBinary string) {
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
	var pids fixturePIDs
	select {
	case pids = <-ready:
	case err := <-readyErr:
		t.Fatalf("blocked fixture never reported its descendants within %s: %v; result=%+v output:\n%s", fixtureSafetyBudget, err, result, result.Output)
	}
	var runnerErr *Error
	if !errors.As(runErr, &runnerErr) || result.TimedOut || runnerErr.TimedOut {
		t.Fatalf("blocked fixture error = %T %v, result=%+v; want a fail-closed cancellation", runErr, runErr, result)
	}
	if result.ExitCode == 0 {
		t.Fatalf("blocked fixture exit code = 0, want non-zero: %+v", result)
	}
	if runnerErr.Termination != "descendants terminated" {
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

func testSuccessControlUsesSameBoundary(t *testing.T, fixtureBinary string) {
	marker := filepath.Join(t.TempDir(), "success.markers")
	result, err := runFixture(context.Background(), fixtureBinary, marker, "success", "TestTimeoutFixtureSuccess", fixtureSafetyBudget)
	if err != nil {
		t.Fatalf("success fixture: %v\noutput:\n%s", err, result.Output)
	}
	if result.ExitCode != 0 || result.TimedOut {
		t.Fatalf("success fixture result = %+v, want zero non-timeout result", result)
	}
	for _, want := range []string{"fixture=success", "active_test=TestTimeoutFixtureSuccess", "process=success"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("success output missing %q: %q", want, result.Output)
		}
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("success marker missing: %v", err)
	}
}

func buildTimeoutFixture(t *testing.T, moduleRoot string) string {
	t.Helper()
	fixtureBinary := filepath.Join(t.TempDir(), "blockedchild.test")
	result, err := Run(context.Background(), Config{
		Command: "go",
		Dir:     moduleRoot,
		Args:    []string{"test", "-c", "-o", fixtureBinary, timeoutFixturePackage},
		Label:   "timeout fixture preflight",
		// Compiling the fixture is setup, not the timeout contract under test.
		// A cold-cache build measured 5-10s at load average ~100, so a 10s
		// bound failed the preflight before the contract ran.
		Timeout: fixtureBuildTimeout,
	})
	if err != nil {
		t.Fatalf("compile timeout fixture: %v\noutput:\n%s", err, result.Output)
	}
	// The first exec of a freshly linked binary on macOS waits for a code
	// assessment that can exceed the fixture budgets on a loaded machine; pay
	// it once here, outside the timed runs.
	warmup, err := Run(context.Background(), Config{
		Command: fixtureBinary,
		Args:    []string{"-test.list", "^$"},
		Label:   "timeout fixture warm-up",
		Timeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("warm up timeout fixture: %v\noutput:\n%s", err, warmup.Output)
	}
	return fixtureBinary
}

func runFixture(ctx context.Context, fixtureBinary, marker, mode, testName string, timeout time.Duration) (Result, error) {
	env := replaceEnv(os.Environ(), "AGENT_CLI_TIMEOUT_FIXTURE_MODE", mode)
	env = replaceEnv(env, "AGENT_CLI_TIMEOUT_FIXTURE_MARKER", marker)
	return Run(ctx, Config{
		Command: fixtureBinary,
		Env:     env,
		Args: []string{
			"-test.v", "-test.count=1", "-test.timeout", "10s",
			"-test.run", "^(" + testName + ")$",
		},
		Label:   "agent-cli timeout fixture " + testName,
		Timeout: timeout,
	})
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
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processRunning(pids.parent) && !processRunning(pids.child) && !processRunning(pids.descendant) && !processRunning(pids.grandchild) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fixture processes remain after timeout: %+v", pids)
}

func moduleRootPath(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve testtimeout module root: runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
}

func replaceEnv(environment []string, key, value string) []string {
	prefix := key + "="
	filtered := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		if strings.HasPrefix(item, prefix) {
			continue
		}
		filtered = append(filtered, item)
	}
	return append(filtered, prefix+value)
}
