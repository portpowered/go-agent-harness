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
	timeoutFixturePackage       = "./internal/testtimeout/testdata/blockedchild"
	blockedFixtureTimeoutBudget = 8 * time.Second
)

func TestTimeoutContractBlockedChildFailsClosedAndCleansDescendants(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group fixture contract is covered by the Windows taskkill implementation")
	}
	moduleRoot := moduleRootPath(t)
	fixtureBinary := buildTimeoutFixture(t, moduleRoot)

	for attempt := 0; attempt < 3; attempt++ {
		t.Run(fmt.Sprintf("attempt-%d", attempt+1), func(t *testing.T) {
			t.Parallel()
			marker := filepath.Join(t.TempDir(), "blocked-child.markers")
			// Leave enough startup headroom for the parent, child, and grandchild
			// on cold or contended workers while keeping this test-only budget
			// well below the contract's ten-second completion bound.
			result, runErr := runFixture(t, fixtureBinary, marker, "blocked", "TestTimeoutFixtureBlockedChild", blockedFixtureTimeoutBudget)
			if runErr == nil {
				t.Fatalf("blocked fixture unexpectedly exited successfully: result=%+v output=%q", result, result.Output)
			}
			var timeoutErr *Error
			if !errors.As(runErr, &timeoutErr) || !result.TimedOut || !timeoutErr.TimedOut {
				t.Fatalf("blocked fixture error = %T %v, result=%+v; want timeout boundary", runErr, runErr, result)
			}
			if result.ExitCode == 0 {
				t.Fatalf("blocked fixture exit code = 0, want non-zero: %+v", result)
			}
			if result.Duration >= 12*time.Second {
				t.Fatalf("blocked fixture waited too long: %s", result.Duration)
			}

			pids, err := readFixturePIDs(marker)
			if err != nil {
				t.Fatalf("read blocked fixture marker: %v\noutput:\n%s", err, result.Output)
			}
			for _, want := range []string{
				"fixture=blocked-child",
				"active_test=TestTimeoutFixtureBlockedChild",
				"child_pid=",
				"descendant_pid=",
				"pid=",
				"descendants terminated",
			} {
				if !strings.Contains(result.Output+"\n"+runErr.Error(), want) {
					t.Fatalf("blocked diagnostic missing %q:\noutput:\n%s\nerror:\n%v", want, result.Output, runErr)
				}
			}
			waitForProcessesToExit(t, pids)
		})
	}
}

func TestTimeoutContractSuccessControlUsesSameBoundary(t *testing.T) {
	moduleRoot := moduleRootPath(t)
	fixtureBinary := buildTimeoutFixture(t, moduleRoot)
	marker := filepath.Join(t.TempDir(), "success.markers")
	result, err := runFixture(t, fixtureBinary, marker, "success", "TestTimeoutFixtureSuccess", 3*time.Second)
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
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("compile timeout fixture: %v\noutput:\n%s", err, result.Output)
	}
	return fixtureBinary
}

func runFixture(t *testing.T, fixtureBinary, marker, mode, testName string, timeout time.Duration) (Result, error) {
	t.Helper()
	env := replaceEnv(os.Environ(), "AGENT_CLI_TIMEOUT_FIXTURE_MODE", mode)
	env = replaceEnv(env, "AGENT_CLI_TIMEOUT_FIXTURE_MARKER", marker)
	return Run(context.Background(), Config{
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

func readFixturePIDs(path string) (fixturePIDs, error) {
	deadline := time.Now().Add(2 * time.Second)
	var data []byte
	var err error
	for time.Now().Before(deadline) {
		data, err = os.ReadFile(path)
		if err == nil && strings.Contains(string(data), "child_pid=") && strings.Contains(string(data), "descendant_pid=") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		return fixturePIDs{}, err
	}
	text := string(data)
	pids := fixturePIDs{
		parent:     markerPID(text, "parent_pid"),
		child:      markerPID(text, "child_pid"),
		descendant: markerPID(text, "descendant_pid"),
		grandchild: markerPID(text, "grandchild_pid"),
	}
	if pids.parent <= 0 || pids.child <= 0 || pids.descendant <= 0 {
		return fixturePIDs{}, fmt.Errorf("incomplete marker %q", text)
	}
	return pids, nil
}

func markerPID(text, name string) int {
	for _, field := range strings.Fields(text) {
		keyValue := strings.SplitN(field, "=", 2)
		if len(keyValue) != 2 || keyValue[0] != name {
			continue
		}
		pid, _ := strconv.Atoi(keyValue[1])
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
