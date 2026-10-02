package chatgptauth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// plantLock leaves a lock directory owned by owner, dated at.
func plantLock(t *testing.T, lockPath, owner string, at time.Time) {
	t.Helper()
	if err := os.MkdirAll(lockPath, storeDirMode); err != nil {
		t.Fatalf("plant lock: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lockPath, lockOwnerFile), []byte(owner), storeFileMode); err != nil {
		t.Fatalf("plant owner: %v", err)
	}
	touchLock(t, lockPath, at)
}

func yieldingSleeper(ctx context.Context, _ time.Duration) error {
	runtime.Gosched()
	return ctx.Err()
}

func TestTwoWaitersOnAStaleLockNeverHoldItTogether(t *testing.T) {
	const rounds, waiters = 10, 2
	for round := range rounds {
		path := filepath.Join(t.TempDir(), "chatgpt.json")
		// The stale lock is an hour old on the wall clock: its holder crashed.
		plantLock(t, path+lockSuffix, "crashed-holder", time.Now().Add(-time.Hour))
		var active, maxActive atomic.Int32
		var wg sync.WaitGroup
		for range waiters {
			wg.Go(func() {
				store := NewFileStore(path, WithStoreSleeper(yieldingSleeper))
				unlock, err := store.Lock(t.Context())
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
				if err := unlock(); err != nil {
					t.Errorf("round %d: unlock: %v", round, err)
				}
			})
		}
		wg.Wait()
		if maxActive.Load() != 1 {
			t.Fatalf("round %d: %d waiters held the lock at once, want 1", round, maxActive.Load())
		}
		entries, err := os.ReadDir(filepath.Dir(path))
		if err != nil || len(entries) != 0 {
			t.Fatalf("round %d: leftover lock entries %v (%v)", round, entries, err)
		}
	}
}

func TestReleaseLeavesTheLockOfTheHolderThatBrokeIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chatgpt.json")
	lockPath := path + lockSuffix
	slow := NewFileStore(path)
	releaseSlow, err := slow.Lock(t.Context())
	if err != nil {
		t.Fatalf("slow Lock: %v", err)
	}
	// The slow holder stalls past the stale age; another process breaks the
	// lock and takes it.
	touchLock(t, lockPath, time.Now().Add(-time.Hour))
	releaseNext, err := NewFileStore(path).Lock(t.Context())
	if err != nil {
		t.Fatalf("next Lock: %v", err)
	}
	nextOwner := lockOwner(lockPath)

	if err := releaseSlow(); err != nil {
		t.Fatalf("slow release: %v", err)
	}
	if got := lockOwner(lockPath); got == "" || got != nextOwner {
		t.Fatalf("after the broken holder released, lock owner = %q, want the new holder %q", got, nextOwner)
	}
	if err := releaseNext(); err != nil {
		t.Fatalf("next release: %v", err)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock left after its holder released: %v", err)
	}
}

func TestTakeLockFromRestoresALockThatChangedHands(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "chatgpt.json") + lockSuffix
	plantLock(t, lockPath, "live-holder", time.Now())
	if err := takeLockFrom(lockPath, "judged-stale-holder"); !errors.Is(err, errLockChangedHands) {
		t.Fatalf("takeLockFrom = %v, want errLockChangedHands", err)
	}
	if got := lockOwner(lockPath); got != "live-holder" {
		t.Fatalf("lock owner after restore = %q, want the live holder", got)
	}
	if err := takeLockFrom(filepath.Join(filepath.Dir(lockPath), "missing.lock"), "x"); err != nil {
		t.Fatalf("taking a missing lock = %v, want nil", err)
	}
}
