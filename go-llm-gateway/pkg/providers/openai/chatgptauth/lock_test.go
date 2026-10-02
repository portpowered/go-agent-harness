package chatgptauth

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// lockHolderEnv makes the test binary act as a second process that holds
// the store lock: it takes the lock, reports "locked" on stdout, and keeps
// it until stdin closes (or until it is killed).
const lockHolderEnv = "CHATGPTAUTH_TEST_LOCK_HOLDER"

func TestMain(m *testing.M) {
	if path := os.Getenv(lockHolderEnv); path != "" {
		os.Exit(runLockHolder(path))
	}
	os.Exit(m.Run())
}

func runLockHolder(path string) int {
	lock, err := NewFileStore(path).Lock(context.Background())
	if err != nil {
		return 2
	}
	if _, err := os.Stdout.WriteString("locked\n"); err != nil {
		return 3
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		return 4
	}
	if err := lock.Release(); err != nil {
		return 5
	}
	return 0
}

type lockHolderProcess struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
}

// startLockHolder runs this test binary as a separate process that holds
// the lock on path, and returns once it reports holding it.
func startLockHolder(t *testing.T, path string) *lockHolderProcess {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("test executable: %v", err)
	}
	cmd := exec.CommandContext(t.Context(), executable, "-test.run=^$")
	cmd.Env = append(os.Environ(), lockHolderEnv+"="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start lock holder: %v", err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("lock holder said %q (%v), want locked", line, err)
	}
	return &lockHolderProcess{cmd: cmd, stdin: stdin}
}

// releaseOnWait is a sleeper that, the first time a waiter has to wait,
// runs release (the other holder lets go) and records the wait.
func releaseOnWait(waits *atomic.Int32, release func() error) Sleeper {
	return func(ctx context.Context, _ time.Duration) error {
		if waits.Add(1) == 1 {
			return release()
		}
		runtime.Gosched()
		return ctx.Err()
	}
}

func TestStoreLockExcludesAnotherProcessUntilItReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chatgpt.json")
	holder := startLockHolder(t, path)
	var waits atomic.Int32
	store := NewFileStore(path, WithStoreSleeper(releaseOnWait(&waits, func() error {
		return errors.Join(holder.stdin.Close(), holder.cmd.Wait())
	})))

	lock, err := store.Lock(t.Context())
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if waits.Load() == 0 {
		t.Fatal("Lock succeeded while another process held the lock")
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestStoreLockIsFreedWhenItsHolderProcessCrashes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chatgpt.json")
	holder := startLockHolder(t, path)
	if err := holder.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill holder: %v", err)
	}
	if err := holder.cmd.Wait(); err == nil {
		t.Fatal("killed holder exited cleanly")
	}
	// No staleness to wait out: the OS released the crashed holder's lock.
	store := NewFileStore(path, WithStoreSleeper(func(context.Context, time.Duration) error {
		return errors.New("waited for a lock whose holder crashed")
	}))
	lock, err := store.Lock(t.Context())
	if err != nil {
		t.Fatalf("Lock after the holder crashed: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestStoreLockExcludesSeparateOpensInOneProcess(t *testing.T) {
	const rounds, waiters = 20, 2
	path := filepath.Join(t.TempDir(), "chatgpt.json")
	for round := range rounds {
		var active, maxActive atomic.Int32
		var wg sync.WaitGroup
		for range waiters {
			wg.Go(func() {
				store := NewFileStore(path, WithStoreSleeper(func(ctx context.Context, _ time.Duration) error {
					runtime.Gosched()
					return ctx.Err()
				}))
				lock, err := store.Lock(t.Context())
				if err != nil {
					t.Errorf("round %d: Lock: %v", round, err)
					return
				}
				now := active.Add(1)
				for current := maxActive.Load(); now > current && !maxActive.CompareAndSwap(current, now); current = maxActive.Load() {
				}
				for range 50 {
					runtime.Gosched()
				}
				active.Add(-1)
				if err := lock.Release(); err != nil {
					t.Errorf("round %d: Release: %v", round, err)
				}
			})
		}
		wg.Wait()
		if got := maxActive.Load(); got != 1 {
			t.Fatalf("round %d: %d holders at once, want 1", round, got)
		}
	}
}
