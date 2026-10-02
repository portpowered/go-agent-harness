//go:build aix || darwin || dragonfly || freebsd || hurd || illumos || linux || netbsd || openbsd || solaris

package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation"
)

func shValidator(t *testing.T, script string, timeout time.Duration) browserConversationValidatorProcessResult {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Fatalf("sh unavailable: %v", err)
	}
	return runBrowserConversationValidator(t.Context(), []string{"sh", "-c", script}, t.TempDir(), []string{"PATH=/usr/bin:/bin"}, []byte(`{"in":1}`), timeout)
}

func TestValidatorProcessReturnsStdoutAndReadsStdin(t *testing.T) {
	result := shValidator(t, `cat`, 5*time.Second)
	if result.err != nil || string(result.stdout) != `{"in":1}` || result.stdoutTruncated {
		t.Fatalf("result = %+v", result)
	}
}

func TestValidatorProcessFailureAndStartError(t *testing.T) {
	result := shValidator(t, `echo partial; exit 3`, 5*time.Second)
	if !errors.Is(result.err, browserconversation.ErrBrowserConversationValidatorFailed) || !strings.Contains(string(result.stdout), "partial") {
		t.Fatalf("failed validator = %+v", result)
	}
	missing := runBrowserConversationValidator(t.Context(), []string{"/nonexistent/validator"}, "", nil, nil, time.Second)
	if !errors.Is(missing.err, browserconversation.ErrBrowserConversationValidatorStart) {
		t.Fatalf("missing validator = %v", missing.err)
	}
}

func TestValidatorProcessBoundsOutput(t *testing.T) {
	result := shValidator(t, `head -c 1100000 /dev/zero; head -c 1100000 /dev/zero >&2`, 10*time.Second)
	if result.err != nil || !result.stdoutTruncated || !result.stderrTruncated || len(result.stdout) != maxBrowserConversationValidatorOutput {
		t.Fatalf("truncation = out %v err %v len %d (%v)", result.stdoutTruncated, result.stderrTruncated, len(result.stdout), result.err)
	}
}

// TestValidatorProcessTimeoutTerminatesGroup proves a hung validator is
// stopped with SIGTERM once its timeout passes.
func TestValidatorProcessTimeoutTerminatesGroup(t *testing.T) {
	result := shValidator(t, `exec sleep 30`, 50*time.Millisecond)
	if !errors.Is(result.err, browserconversation.ErrBrowserConversationValidatorTimeout) || strings.Contains(result.err.Error(), "did not finish cleanup") {
		t.Fatalf("timeout result = %v", result.err)
	}
}

// TestValidatorProcessCancellationKillsGroupIgnoringSIGTERM proves a group
// that ignores SIGTERM is killed. The script reports through a FIFO once its
// SIGTERM disposition is installed (and inherited by its child), and only then
// is the run cancelled, so the SIGKILL escalation is exercised on every run.
// The ignored SIGTERM keeps the process alive, so the bounded wait, not the
// process exit, decides the outcome.
func TestValidatorProcessCancellationKillsGroupIgnoringSIGTERM(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	if err := syscall.Mkfifo(ready, 0o600); err != nil {
		t.Fatalf("Mkfifo = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan browserConversationValidatorProcessResult, 1)
	script := `trap "" TERM; sleep 30 & echo ready > "$1"; wait`
	go func() {
		result <- runBrowserConversationValidator(ctx, []string{"sh", "-c", script, "sh", ready}, "", []string{"PATH=/usr/bin:/bin"}, nil, time.Minute)
	}()
	// Opening the FIFO for reading blocks until the script writes.
	if _, err := os.ReadFile(ready); err != nil {
		t.Fatalf("read readiness = %v", err)
	}
	cancel()
	got := <-result
	if !errors.Is(got.err, browserconversation.ErrBrowserConversationValidatorTimeout) || strings.Contains(got.err.Error(), "did not finish cleanup") {
		t.Fatalf("cancellation result = %v", got.err)
	}
}

// The group-exit poll never sleeps past the remaining cleanup budget.
func TestCleanupPollIsBoundedByRemainingBudget(t *testing.T) {
	if got := minBrowserConversationDuration(3*time.Millisecond, 10*time.Millisecond); got != 3*time.Millisecond {
		t.Fatalf("short budget = %v", got)
	}
	if got := minBrowserConversationDuration(time.Second, 10*time.Millisecond); got != 10*time.Millisecond {
		t.Fatalf("long budget = %v", got)
	}
}

// The bounded buffer keeps exactly limit bytes across any write split and
// reports truncation without failing the writer.
func TestBoundedBufferTruncatesAcrossWrites(t *testing.T) {
	buffer := &browserConversationBoundedBuffer{limit: 5}
	for _, chunk := range []string{"ab", "cde", "", "fg"} {
		if n, err := buffer.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write(%q) = %d, %v", chunk, n, err)
		}
	}
	if buffer.String() != "abcde" || buffer.Len() != 5 || !buffer.truncated {
		t.Fatalf("buffer = %q (len %d, truncated %v)", buffer.String(), buffer.Len(), buffer.truncated)
	}
	split := &browserConversationBoundedBuffer{limit: 3}
	if n, err := split.Write([]byte("wxyz")); err != nil || n != 4 || string(split.Bytes()) != "wxy" || !split.truncated {
		t.Fatalf("straddling Write = %d, %v, %q, truncated %v", n, err, split.Bytes(), split.truncated)
	}
	exact := &browserConversationBoundedBuffer{limit: 2}
	if _, err := exact.Write([]byte("ok")); err != nil || exact.truncated || exact.String() != "ok" {
		t.Fatalf("exact Write = %v, %q, truncated %v", err, exact.String(), exact.truncated)
	}
}

func TestValidatorProcessCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result := runBrowserConversationValidator(ctx, []string{"sh", "-c", "sleep 30"}, "", []string{"PATH=/usr/bin:/bin"}, nil, time.Minute)
	// A caller that already gave up never starts the validator.
	if !errors.Is(result.err, browserconversation.ErrBrowserConversationValidatorStart) || !errors.Is(result.err, context.Canceled) {
		t.Fatalf("canceled validator = %v", result.err)
	}
	if browserConversationProcessGroupExists(nil) || browserConversationProcessGroupExists(&exec.Cmd{}) {
		t.Fatal("absent process reported as existing")
	}
	if terminateBrowserConversationProcessGroup(&exec.Cmd{}) != nil || killBrowserConversationProcessGroup(&exec.Cmd{}) != nil {
		t.Fatal("signalling an unstarted command failed")
	}
}
