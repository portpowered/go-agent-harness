package clitest

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const watchdogHelperEnv = "CLITEST_WATCHDOG_HELPER"

// TestWatchdogFailsBubbleBlockedOnRealIO proves the real-time watchdog: a
// goroutine blocked in a real socket Accept keeps the bubble from going idle,
// so the body's virtual sleep never ends. The stuck body runs in a child test
// process, which the watchdog must fail with its diagnostic long before the
// child's global -timeout.
func TestWatchdogFailsBubbleBlockedOnRealIO(t *testing.T) {
	if os.Getenv(watchdogHelperEnv) != "" {
		testWithin(t, 200*time.Millisecond, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			go func() { _, _ = listener.Accept() }() //nolint:errcheck // blocks forever by design.
			time.Sleep(time.Hour)
		})
		return
	}
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestWatchdogFailsBubbleBlockedOnRealIO$", "-test.count=1", "-test.timeout=60s")
	command.Env = append(os.Environ(), watchdogHelperEnv+"=1")
	output, err := command.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("stuck bubble child = %v, want a failing exit; output:\n%s", err, output)
	}
	for _, want := range []string{"synctest bubble never went idle", "real I/O (socket, exec or cgo)", "Accept"} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("watchdog output lacks %q:\n%s", want, output)
		}
	}
	if strings.Contains(string(output), "test timed out") {
		t.Fatalf("stuck bubble hit the global -timeout instead of the watchdog:\n%s", output)
	}
}
