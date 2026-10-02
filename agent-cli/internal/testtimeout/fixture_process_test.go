package testtimeout

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/testcover"
)

const (
	fixtureModeEnv   = "AGENT_CLI_TIMEOUT_FIXTURE_MODE"
	fixtureMarkerEnv = "AGENT_CLI_TIMEOUT_FIXTURE_MARKER"
)

// TestMain re-enters this test binary as one timeout-contract fixture process
// when the timeout contract sets fixtureModeEnv; ordinary runs execute the
// tests. The fixture names keep the historical active_test labels so the
// contract's diagnostics stay comparable.
func TestMain(m *testing.M) {
	if mode := os.Getenv(fixtureModeEnv); mode != "" {
		testcover.MustIsolateFixtureProcess()
		os.Exit(runTimeoutFixture(context.Background(), mode))
	}
	os.Exit(m.Run())
}

// runTimeoutFixture runs one fixture process. The blocked fixture starts a
// child and grandchild, then blocks so the production test-command boundary
// must terminate the entire process group; the success fixture announces
// itself and exits cleanly.
func runTimeoutFixture(ctx context.Context, mode string) int {
	switch mode {
	case "blocked":
		child, err := startFixtureProcess(ctx, "child")
		if err != nil {
			return fixtureStartFailure(err)
		}
		announceFixture("fixture=blocked-child active_test=TestTimeoutFixtureBlockedChild process=parent parent_pid=%d child_pid=%d", os.Getpid(), child.Pid)
	case "child":
		descendant, err := startFixtureProcess(ctx, "grandchild")
		if err != nil {
			return fixtureStartFailure(err)
		}
		announceFixture("fixture=blocked-child active_test=TestTimeoutFixtureChild process=child child_pid=%d descendant_pid=%d", os.Getpid(), descendant.Pid)
	case "grandchild":
		announceFixture("fixture=blocked-child active_test=TestTimeoutFixtureGrandchild process=grandchild grandchild_pid=%d", os.Getpid())
	case "success":
		announceFixture("fixture=success active_test=TestTimeoutFixtureSuccess process=success pid=%d", os.Getpid())
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown timeout fixture mode %q\n", mode)
		return fixtureUsageExitCode
	}
	blockForever()
	return 0
}

// fixtureUsageExitCode reports an unknown fixture mode, mirroring the exit
// status of a test binary invoked with bad flags.
const fixtureUsageExitCode = 2

func fixtureStartFailure(err error) int {
	fmt.Fprintf(os.Stderr, "start timeout fixture: %v\n", err)
	return 1
}

func startFixtureProcess(ctx context.Context, mode string) (*os.Process, error) {
	cmd := exec.CommandContext(ctx, os.Args[0])
	cmd.Env = fixtureEnvironment(mode)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s fixture: %w", mode, err)
	}
	return cmd.Process, nil
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
	if _, err := os.Stdout.WriteString(line + "\n"); err != nil {
		fmt.Fprintf(os.Stderr, "fixture announcement error: %v\n", err)
	}
	if path := os.Getenv(fixtureMarkerEnv); path != "" {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fixture marker error: %v\n", err)
			return
		}
		_, writeErr := fmt.Fprintln(file, line)
		closeErr := file.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			fmt.Fprintf(os.Stderr, "fixture marker error: %v\n", err)
		}
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
