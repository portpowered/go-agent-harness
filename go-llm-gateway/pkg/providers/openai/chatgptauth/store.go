package chatgptauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const (
	storeVersion      = 1
	storeDirMode      = 0o700
	storeFileMode     = 0o600
	insecureModeBits  = 0o077
	lockSuffix        = ".lock"
	defaultLockStale  = 2 * time.Minute
	defaultLockPoll   = 100 * time.Millisecond
	windowsGOOS       = "windows"
	storeTempPattern  = ".chatgpt-*.tmp"
	storeVersionError = "unsupported ChatGPT auth store version %d"
)

// ErrInsecureStore reports a credential file that other users can read.
var ErrInsecureStore = errors.New("ChatGPT auth store is readable by other users; run `chmod 600` on it or sign in again")

// FileStore keeps one credential in a JSON file (mode 0600, directory 0700).
// A sibling lock directory serializes refresh across processes, because
// refresh tokens rotate and a concurrent refresh would log one process out.
type FileStore struct {
	path       string
	now        func() time.Time
	sleep      Sleeper
	lockStale  time.Duration
	lockPoll   time.Duration
	enforceACL bool
}

// StoreOption configures a FileStore.
type StoreOption func(*FileStore)

// WithStoreClock sets the clock used to judge stale locks.
func WithStoreClock(now func() time.Time) StoreOption {
	return func(s *FileStore) { s.now = now }
}

// WithStoreSleeper sets how a waiter pauses between lock attempts.
func WithStoreSleeper(sleep Sleeper) StoreOption {
	return func(s *FileStore) { s.sleep = sleep }
}

// NewFileStore returns a store for the credential file at path.
func NewFileStore(path string, options ...StoreOption) *FileStore {
	s := &FileStore{
		path:       path,
		now:        time.Now,
		sleep:      WaitContext,
		lockStale:  defaultLockStale,
		lockPoll:   defaultLockPoll,
		enforceACL: runtime.GOOS != windowsGOOS,
	}
	for _, option := range options {
		option(s)
	}
	return s
}

// Path is the credential file path.
func (s *FileStore) Path() string { return s.path }

type storedTokens struct {
	IDToken      string `json:"id_token,omitempty"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

type storedCredential struct {
	Version     int          `json:"version"`
	Issuer      string       `json:"issuer"`
	ClientID    string       `json:"client_id"`
	AccountID   string       `json:"account_id,omitempty"`
	Email       string       `json:"email,omitempty"`
	PlanType    string       `json:"plan_type,omitempty"`
	Tokens      storedTokens `json:"tokens"`
	ExpiresAt   *time.Time   `json:"expires_at,omitempty"`
	LastRefresh time.Time    `json:"last_refresh"`
}

// Load reads the stored credential. It returns ErrNotLoggedIn when there is
// none and ErrInsecureStore when the file is readable by other users.
func (s *FileStore) Load() (Credential, error) {
	info, err := os.Stat(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return Credential{}, ErrNotLoggedIn
	}
	if err != nil {
		return Credential{}, fmt.Errorf("read ChatGPT auth store: %w", err)
	}
	if s.enforceACL && info.Mode().Perm()&insecureModeBits != 0 {
		return Credential{}, ErrInsecureStore
	}
	payload, err := os.ReadFile(s.path)
	if err != nil {
		return Credential{}, fmt.Errorf("read ChatGPT auth store: %w", err)
	}
	var stored storedCredential
	if err := json.Unmarshal(payload, &stored); err != nil {
		return Credential{}, fmt.Errorf("decode ChatGPT auth store %s: %w", s.path, err)
	}
	if stored.Version != storeVersion {
		return Credential{}, fmt.Errorf(storeVersionError, stored.Version)
	}
	if stored.Tokens.AccessToken == "" && stored.Tokens.RefreshToken == "" {
		return Credential{}, ErrNotLoggedIn
	}
	return stored.credential(), nil
}

func (s storedCredential) credential() Credential {
	cred := Credential{
		Issuer:       s.Issuer,
		ClientID:     s.ClientID,
		IDToken:      s.Tokens.IDToken,
		AccessToken:  s.Tokens.AccessToken,
		RefreshToken: s.Tokens.RefreshToken,
		AccountID:    s.AccountID,
		Email:        s.Email,
		PlanType:     s.PlanType,
		LastRefresh:  s.LastRefresh,
	}
	if s.ExpiresAt != nil {
		cred.ExpiresAt = *s.ExpiresAt
	}
	return cred
}

// Save atomically replaces the stored credential: it writes a 0600 temporary
// file in the store directory, syncs it, then renames it over the old one.
func (s *FileStore) Save(cred Credential) error {
	stored := storedCredential{
		Version:     storeVersion,
		Issuer:      cred.Issuer,
		ClientID:    cred.ClientID,
		AccountID:   cred.AccountID,
		Email:       cred.Email,
		PlanType:    cred.PlanType,
		Tokens:      storedTokens{IDToken: cred.IDToken, AccessToken: cred.AccessToken, RefreshToken: cred.RefreshToken},
		LastRefresh: cred.LastRefresh,
	}
	if !cred.ExpiresAt.IsZero() {
		expires := cred.ExpiresAt
		stored.ExpiresAt = &expires
	}
	payload, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return fmt.Errorf("encode ChatGPT auth store: %w", err)
	}
	return writeFileAtomic(s.path, append(payload, '\n'))
}

func writeFileAtomic(path string, payload []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, storeDirMode); err != nil {
		return fmt.Errorf("create ChatGPT auth store directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, storeTempPattern)
	if err != nil {
		return fmt.Errorf("create ChatGPT auth store: %w", err)
	}
	tmpPath := tmp.Name()
	writeErr := writeAndSync(tmp, payload)
	if writeErr == nil {
		writeErr = os.Rename(tmpPath, path)
	}
	if writeErr != nil {
		return errors.Join(fmt.Errorf("write ChatGPT auth store: %w", writeErr), removeIfExists(tmpPath))
	}
	return nil
}

func writeAndSync(file *os.File, payload []byte) error {
	err := file.Chmod(storeFileMode)
	if err == nil {
		_, err = file.Write(payload)
	}
	if err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}

// Delete removes the stored credential. A missing file is not an error.
func (s *FileStore) Delete() error {
	if err := removeIfExists(s.path); err != nil {
		return fmt.Errorf("delete ChatGPT auth store: %w", err)
	}
	return nil
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Lock takes the store's cross-process lock and returns its release
// function. The lock is a sibling directory, created atomically; a lock
// older than the stale age (by its modification time on the store clock) is
// left over from a crashed process and is broken.
func (s *FileStore) Lock(ctx context.Context) (func() error, error) {
	lockPath := s.path + lockSuffix
	if err := os.MkdirAll(filepath.Dir(s.path), storeDirMode); err != nil {
		return nil, fmt.Errorf("create ChatGPT auth store directory: %w", err)
	}
	for {
		err := os.Mkdir(lockPath, storeDirMode)
		if err == nil {
			return func() error { return removeIfExists(lockPath) }, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("lock ChatGPT auth store: %w", err)
		}
		if s.breakStaleLock(lockPath) {
			continue
		}
		if err := s.sleep(ctx, s.lockPoll); err != nil {
			return nil, fmt.Errorf("wait for ChatGPT auth store lock: %w", err)
		}
	}
}

func (s *FileStore) breakStaleLock(lockPath string) bool {
	info, err := os.Stat(lockPath)
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if err != nil || s.now().Sub(info.ModTime()) < s.lockStale {
		return false
	}
	return removeIfExists(lockPath) == nil
}
