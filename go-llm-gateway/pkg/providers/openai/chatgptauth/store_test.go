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

func newTestStore(t *testing.T) *FileStore {
	t.Helper()
	return NewFileStore(filepath.Join(t.TempDir(), "auth", "chatgpt.json"))
}

func TestFileStoreRoundTripsACredential(t *testing.T) {
	store := newTestStore(t)
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
			store := newTestStore(t)
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

func TestStoreLockWaitStopsWithTheContext(t *testing.T) {
	store := NewFileStore(filepath.Join(t.TempDir(), "chatgpt.json"))
	unlock, err := store.Lock(t.Context())
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer func() {
		if err := unlock.Release(); err != nil {
			t.Errorf("unlock: %v", err)
		}
	}()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.Lock(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("contended Lock = %v, want context.Canceled", err)
	}
}

// rotatedAccessToken is the access token the fake issuer rotates to.
const rotatedAccessToken = "access-new"

func TestManagerRefreshesOnlyInsideTheExpiryWindow(t *testing.T) {
	clock := newVirtualClock()
	issuer := newFakeIssuer(t)
	store := newTestStore(t)
	if err := store.Save(storedCredentialFixture(t, testEpoch().Add(time.Hour))); err != nil {
		t.Fatalf("Save: %v", err)
	}
	manager := NewManager(store, issuer.client(clock))

	cred, err := manager.Credential(t.Context())
	if err != nil || cred.AccessToken != "access-old" || len(issuer.forms()) != 0 {
		t.Fatalf("fresh Credential = %q, %v after %d refreshes; want the stored token, no refresh", cred.AccessToken, err, len(issuer.forms()))
	}

	clock.Advance(time.Hour - RefreshWindow)
	issuer.queueToken(issuerReply{status: http.StatusOK, body: tokenBody(t, rotatedAccessToken, "refresh-new", 3600)})
	cred, err = manager.Credential(t.Context())
	if err != nil || cred.AccessToken != rotatedAccessToken {
		t.Fatalf("Credential in window = %q, %v; want a refreshed token", cred.AccessToken, err)
	}
	stored, err := store.Load()
	if err != nil || stored.RefreshToken != "refresh-new" || !stored.ExpiresAt.Equal(clock.Now().Add(time.Hour)) {
		t.Fatalf("stored after refresh = %+v, %v; want the rotated refresh token persisted", stored, err)
	}
	// The refresh released its lock: another holder can take it at once.
	lock, err := NewFileStore(store.Path(), WithStoreSleeper(func(context.Context, time.Duration) error {
		return errors.New("lock still held after the refresh")
	})).Lock(t.Context())
	if err != nil {
		t.Fatalf("Lock after refresh: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
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
	// While this process waits for the lock, the other one finishes its
	// refresh: it saves a rotated credential and releases the lock.
	waitHook := func(ctx context.Context, d time.Duration) error {
		rotated := storedCredentialFixture(t, testEpoch().Add(time.Hour))
		rotated.AccessToken, rotated.RefreshToken = "access-other", "refresh-other"
		return errors.Join(other.Save(rotated), otherUnlock.Release())
	}
	store := NewFileStore(path, WithStoreSleeper(waitHook))

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
			store := newTestStore(t)
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
	store := newTestStore(t)
	if _, err := NewManager(store, NewClient(Config{})).Credential(t.Context()); !errors.Is(err, ErrNotLoggedIn) || !strings.Contains(err.Error(), "yui auth chatgpt") {
		t.Fatalf("Credential with no store = %v, want ErrNotLoggedIn", err)
	}
}

func TestManagerForceRefreshReplacesARejectedToken(t *testing.T) {
	clock := newVirtualClock()
	issuer := newFakeIssuer(t)
	store := newTestStore(t)
	if err := store.Save(storedCredentialFixture(t, testEpoch().Add(time.Hour))); err != nil {
		t.Fatalf("Save: %v", err)
	}
	manager := NewManager(store, issuer.client(clock))

	issuer.queueToken(issuerReply{status: http.StatusOK, body: tokenBody(t, rotatedAccessToken, "refresh-new", 3600)})
	cred, err := manager.ForceRefresh(t.Context(), "access-old")
	if err != nil || cred.AccessToken != rotatedAccessToken || len(issuer.forms()) != 1 {
		t.Fatalf("ForceRefresh = %q, %v after %d refreshes; want one refresh to access-new", cred.AccessToken, err, len(issuer.forms()))
	}
	if stored, err := store.Load(); err != nil || stored.RefreshToken != "refresh-new" {
		t.Fatalf("stored = %+v, %v; want the rotated refresh token", stored, err)
	}

	// A second caller still holding the old token gets the rotated one
	// without another refresh (the refresh token must not be reused).
	cred, err = manager.ForceRefresh(t.Context(), "access-old")
	if err != nil || cred.AccessToken != rotatedAccessToken || len(issuer.forms()) != 1 {
		t.Fatalf("second ForceRefresh = %q, %v after %d refreshes; want the stored token, no refresh", cred.AccessToken, err, len(issuer.forms()))
	}

	issuer.queueToken(issuerReply{status: http.StatusBadRequest, body: map[string]string{"error": "refresh_token_reused"}})
	if _, err := manager.ForceRefresh(t.Context(), rotatedAccessToken); !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("ForceRefresh with a dead refresh token = %v, want ErrReauthRequired", err)
	}
	if _, err := NewManager(newTestStore(t), issuer.client(clock)).ForceRefresh(t.Context(), "x"); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("ForceRefresh with no login = %v, want ErrNotLoggedIn", err)
	}
}
