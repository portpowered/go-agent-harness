package blockedchild

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	fixtureModeEnv   = "AGENT_CLI_TIMEOUT_FIXTURE_MODE"
	fixtureMarkerEnv = "AGENT_CLI_TIMEOUT_FIXTURE_MARKER"
)

// TestTimeoutFixtureBlockedChild is intentionally selected only by the
// focused timeout contract. It starts a child and grandchild, then blocks so
// the production test-command boundary must terminate the entire process
// group. The testdata directory keeps this fixture out of ./... discovery.
func TestTimeoutFixtureBlockedChild(t *testing.T) {
	if os.Getenv(fixtureModeEnv) != "blocked" {
		t.Skip("blocked-child fixture is launched only by the timeout contract")
	}

	child := startFixtureProcess(t, "child", "TestTimeoutFixtureChild")
	announceFixture("fixture=blocked-child active_test=TestTimeoutFixtureBlockedChild process=parent parent_pid=%d child_pid=%d", os.Getpid(), child.Pid)
	blockForever()
}

func TestTimeoutFixtureChild(t *testing.T) {
	if os.Getenv(fixtureModeEnv) != "child" {
		t.Skip("child fixture is launched only by TestTimeoutFixtureBlockedChild")
	}

	descendant := startFixtureProcess(t, "grandchild", "TestTimeoutFixtureGrandchild")
	announceFixture("fixture=blocked-child active_test=TestTimeoutFixtureChild process=child child_pid=%d descendant_pid=%d", os.Getpid(), descendant.Pid)
	blockForever()
}

func TestTimeoutFixtureGrandchild(t *testing.T) {
	if os.Getenv(fixtureModeEnv) != "grandchild" {
		t.Skip("grandchild fixture is launched only by TestTimeoutFixtureChild")
	}
	announceFixture("fixture=blocked-child active_test=TestTimeoutFixtureGrandchild process=grandchild grandchild_pid=%d", os.Getpid())
	blockForever()
}

func TestTimeoutFixtureSuccess(t *testing.T) {
	if os.Getenv(fixtureModeEnv) != "success" {
		t.Skip("success fixture is launched only by the timeout contract")
	}
	announceFixture("fixture=success active_test=TestTimeoutFixtureSuccess process=success pid=%d", os.Getpid())
}

func startFixtureProcess(t *testing.T, mode, testName string) *os.Process {
	t.Helper()
	args := []string{"-test.v", "-test.count=1", "-test.run", "^(" + testName + ")$"}
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = fixtureEnvironment(mode)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s fixture: %v", mode, err)
	}
	return cmd.Process
}

func fixtureEnvironment(mode string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, fixtureModeEnv+"=") {
			continue
		}
		env = append(env, value)
	}
	return append(env, fixtureModeEnv+"="+mode)
}

func announceFixture(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	fmt.Fprintln(os.Stdout, line)
	if path := os.Getenv(fixtureMarkerEnv); path != "" {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fixture marker error: %v\n", err)
			return
		}
		_, _ = fmt.Fprintln(file, line)
		_ = file.Close()
	}
}

func blockForever() {
	// Keep an active timer so the Go runtime does not turn this intentional
	// blocked-test fixture into its own deadlock failure before the outer
	// timeout boundary gets a chance to terminate the process group.
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
	}
}
