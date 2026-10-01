package testtimeout

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	// fixtureSafetyBudget is the outer bound for runs that finish on their own
	// or are cancelled once the fixture reports readiness. It only fails a run
	// that never becomes ready, so it can be generous for cold or contended
	// workers without slowing the passing path.
	fixtureSafetyBudget = 8 * time.Second
	// expiryBudget is the real timeout exercised by the budget-expiry case.
	// That case asserts nothing about startup, so a short budget cannot flake
	// on a slow fixture start.
	expiryBudget = 300 * time.Millisecond
)

// TestTimeoutContract runs every contract case in parallel against this test
// binary, which re-executes itself as the fixture process tree (see
// fixture_process_test.go), so no fixture has to be compiled first.
func TestTimeoutContract(t *testing.T) {
	t.Parallel()
	fixtureBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve fixture binary: %v", err)
	}

	t.Run("BudgetExpiryFailsClosed", func(t *testing.T) {
		t.Parallel()
		testBudgetExpiryFailsClosed(t, fixtureBinary)
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
	t.Helper()
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
	for _, want := range []string{"timed out after " + expiryBudget.String(), descendantsTerminated} {
		if !strings.Contains(runErr.Error(), want) {
			t.Fatalf("timeout diagnostic missing %q: %v", want, runErr)
		}
	}
}

func testSuccessControlUsesSameBoundary(t *testing.T, fixtureBinary string) {
	t.Helper()
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

func runFixture(ctx context.Context, fixtureBinary, marker, mode, testName string, timeout time.Duration) (Result, error) {
	env := replaceEnv(os.Environ(), fixtureModeEnv, mode)
	env = replaceEnv(env, fixtureMarkerEnv, marker)
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
