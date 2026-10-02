package chatgptauth

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func storedCredentialFixture(t *testing.T, expires time.Time) Credential {
	t.Helper()
	return Credential{
		Issuer: DefaultIssuer, ClientID: DefaultClientID, IDToken: "id", AccessToken: "access-old", RefreshToken: "refresh-old",
		AccountID: "acct_1", Email: "user@example.com", PlanType: "plus", ExpiresAt: expires, LastRefresh: testEpoch().Add(-time.Hour),
	}
}

func newTestStore(t *testing.T, clock *virtualClock, waits *[]time.Duration) *FileStore {
	t.Helper()
	return NewFileStore(filepath.Join(t.TempDir(), "auth", "chatgpt.json"), WithStoreClock(clock.Now), WithStoreSleeper(clock.sleeper(waits)))
}

// touchLock dates a lock directory on the virtual clock, so staleness is
// judged against virtual time rather than the wall clock.
func touchLock(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatalf("date lock %s: %v", path, err)
	}
}

func TestFileStoreRoundTripsACredential(t *testing.T) {
	clock := newVirtualClock()
	store := newTestStore(t, clock, nil)
	if _, err := store.Load(); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("empty Load = %v, want ErrNotLoggedIn", err)
	}
	for _, want := range []Credential{storedCredentialFixture(t, testEpoch().Add(time.Hour)), storedCredentialFixture(t, time.Time{})} {
		if err := store.Save(want); err != nil {
			t.Fatalf("Save: %v", err)
		}
		got, err := store.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !got.ExpiresAt.Equal(want.ExpiresAt) || !got.LastRefresh.Equal(want.LastRefresh) {
			t.Fatalf("times = %v/%v, want %v/%v", got.ExpiresAt, got.LastRefresh, want.ExpiresAt, want.LastRefresh)
		}
		got.ExpiresAt, got.LastRefresh, want.ExpiresAt, want.LastRefresh = time.Time{}, time.Time{}, time.Time{}, time.Time{}
		if got != want {
			t.Fatalf("loaded %+v, want %+v", got, want)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(store.Path()))
	if err != nil || len(entries) != 1 {
		t.Fatalf("store directory = %v (%v), want only the credential file", entries, err)
	}
	if err := store.Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.Delete(); err != nil {
		t.Fatalf("second Delete: %v", err)
	}
	if _, err := store.Load(); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("Load after Delete = %v", err)
	}
}

func TestFileStoreRejectsCorruptOrForeignFiles(t *testing.T) {
	tests := map[string]string{
		"not json":      "{",
		"newer version": `{"version": 2, "tokens": {"access_token": "a"}}`,
		"no tokens":     `{"version": 1, "tokens": {}}`,
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			store := newTestStore(t, newVirtualClock(), nil)
			if err := os.MkdirAll(filepath.Dir(store.Path()), storeDirMode); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(store.Path(), []byte(content), storeFileMode); err != nil {
				t.Fatalf("write: %v", err)
			}
			_, err := store.Load()
			if name == "no tokens" {
				if !errors.Is(err, ErrNotLoggedIn) {
					t.Fatalf("Load = %v, want ErrNotLoggedIn", err)
				}
				return
			}
			if err == nil || errors.Is(err, ErrNotLoggedIn) {
				t.Fatalf("Load = %v, want a decode error", err)
			}
		})
	}
}

func TestStoreLockBreaksOnlyStaleLocks(t *testing.T) {
	clock := newVirtualClock()
	var waits []time.Duration
	store := newTestStore(t, clock, &waits)
	lockPath := store.Path() + lockSuffix
	if err := os.MkdirAll(lockPath, storeDirMode); err != nil {
		t.Fatalf("plant lock: %v", err)
	}
	// The planted lock is one minute old on the virtual clock: a live holder.
	touchLock(t, lockPath, testEpoch().Add(-time.Minute))
	unlock, err := store.Lock(t.Context())
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	// It became stale after waiting out the remaining minute of polls.
	var waited time.Duration
	for _, wait := range waits {
		waited += wait
	}
	if waited != time.Minute {
		t.Fatalf("waited %s, want exactly the 1m until the lock went stale", waited)
	}
	if err := unlock(); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock still present after unlock: %v", err)
	}
}

func TestStoreLockWaitStopsWithTheContext(t *testing.T) {
	store := NewFileStore(filepath.Join(t.TempDir(), "chatgpt.json"), WithStoreClock(newVirtualClock().Now))
	unlock, err := store.Lock(t.Context())
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	touchLock(t, store.Path()+lockSuffix, testEpoch())
	defer func() {
		if err := unlock(); err != nil {
			t.Errorf("unlock: %v", err)
		}
	}()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.Lock(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("contended Lock = %v, want context.Canceled", err)
	}
}

func TestManagerRefreshesOnlyInsideTheExpiryWindow(t *testing.T) {
	clock := newVirtualClock()
	issuer := newFakeIssuer(t)
	store := newTestStore(t, clock, nil)
	if err := store.Save(storedCredentialFixture(t, testEpoch().Add(time.Hour))); err != nil {
		t.Fatalf("Save: %v", err)
	}
	manager := NewManager(store, issuer.client(clock))

	cred, err := manager.Credential(t.Context())
	if err != nil || cred.AccessToken != "access-old" || len(issuer.forms()) != 0 {
		t.Fatalf("fresh Credential = %q, %v after %d refreshes; want the stored token, no refresh", cred.AccessToken, err, len(issuer.forms()))
	}

	clock.Advance(time.Hour - RefreshWindow)
	issuer.queueToken(issuerReply{status: http.StatusOK, body: tokenBody(t, "access-new", "refresh-new", 3600)})
	cred, err = manager.Credential(t.Context())
	if err != nil || cred.AccessToken != "access-new" {
		t.Fatalf("Credential in window = %q, %v; want a refreshed token", cred.AccessToken, err)
	}
	stored, err := store.Load()
	if err != nil || stored.RefreshToken != "refresh-new" || !stored.ExpiresAt.Equal(clock.Now().Add(time.Hour)) {
		t.Fatalf("stored after refresh = %+v, %v; want the rotated refresh token persisted", stored, err)
	}
	if _, err := os.Stat(store.Path() + lockSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refresh left its lock behind: %v", err)
	}
}

func TestManagerUsesATokenAnotherProcessRotatedWhileItWaited(t *testing.T) {
	clock := newVirtualClock()
	issuer := newFakeIssuer(t)
	path := filepath.Join(t.TempDir(), "chatgpt.json")
	other := NewFileStore(path)
	if err := other.Save(storedCredentialFixture(t, testEpoch().Add(time.Minute))); err != nil {
		t.Fatalf("Save: %v", err)
	}
	otherUnlock, err := other.Lock(t.Context())
	if err != nil {
		t.Fatalf("other Lock: %v", err)
	}
	touchLock(t, path+lockSuffix, testEpoch())
	// While this process waits for the lock, the other one finishes its
	// refresh: it saves a rotated credential and releases the lock.
	waitHook := func(ctx context.Context, d time.Duration) error {
		rotated := storedCredentialFixture(t, testEpoch().Add(time.Hour))
		rotated.AccessToken, rotated.RefreshToken = "access-other", "refresh-other"
		return errors.Join(other.Save(rotated), otherUnlock())
	}
	store := NewFileStore(path, WithStoreClock(clock.Now), WithStoreSleeper(waitHook))

	cred, err := NewManager(store, issuer.client(clock)).Credential(t.Context())
	if err != nil || cred.AccessToken != "access-other" {
		t.Fatalf("Credential = %q, %v; want the other process's rotated token", cred.AccessToken, err)
	}
	if len(issuer.forms()) != 0 {
		t.Fatalf("refresh requests = %v, want none (the rotated refresh token must not be reused)", issuer.forms())
	}
}

func TestManagerRefreshFailures(t *testing.T) {
	tests := []struct {
		name       string
		expiresIn  time.Duration
		reply      issuerReply
		wantToken  string
		wantReauth bool
	}{
		{name: "transient, token still valid", expiresIn: time.Minute, reply: issuerReply{status: http.StatusBadGateway, body: map[string]string{}}, wantToken: "access-old"},
		{name: "transient, token expired", expiresIn: -time.Minute, reply: issuerReply{status: http.StatusBadGateway, body: map[string]string{}}},
		{name: "permanent, token still valid", expiresIn: time.Minute, reply: issuerReply{status: http.StatusBadRequest, body: map[string]string{"error": "refresh_token_reused"}}, wantReauth: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := newVirtualClock()
			issuer := newFakeIssuer(t)
			issuer.queueToken(tt.reply)
			store := newTestStore(t, clock, nil)
			if err := store.Save(storedCredentialFixture(t, testEpoch().Add(tt.expiresIn))); err != nil {
				t.Fatalf("Save: %v", err)
			}
			cred, err := NewManager(store, issuer.client(clock)).Credential(t.Context())
			if errors.Is(err, ErrReauthRequired) != tt.wantReauth || cred.AccessToken != tt.wantToken {
				t.Fatalf("Credential = %q, %v; want token %q, reauth %v", cred.AccessToken, err, tt.wantToken, tt.wantReauth)
			}
			if tt.wantToken == "" && err == nil {
				t.Fatal("an expired token with a failed refresh must return an error")
			}
			if _, err := store.Load(); err != nil {
				t.Fatalf("a failed refresh must keep the stored credential for status: %v", err)
			}
		})
	}
	store := newTestStore(t, newVirtualClock(), nil)
	if _, err := NewManager(store, NewClient(Config{})).Credential(t.Context()); !errors.Is(err, ErrNotLoggedIn) || !strings.Contains(err.Error(), "yui auth chatgpt") {
		t.Fatalf("Credential with no store = %v, want ErrNotLoggedIn", err)
	}
}
