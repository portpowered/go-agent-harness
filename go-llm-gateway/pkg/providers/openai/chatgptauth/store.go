package chatgptauth

import (
	"context"
	"crypto/rand"
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
	lockOwnerFile     = "owner"
	lockStagingSuffix = ".new"
	lockTakenSuffix   = ".old"
	defaultLockStale  = 2 * time.Minute
	defaultLockPoll   = 100 * time.Millisecond
	windowsGOOS       = "windows"
	storeTempPattern  = ".chatgpt-*.tmp"
	storeVersionError = "unsupported ChatGPT auth store version %d"
)

// ErrInsecureStore reports a credential file that other users can read.
var ErrInsecureStore = errors.New("ChatGPT auth store is readable by other users; run `chmod 600` on it or sign in again")

// FileStore keeps one credential in a JSON file (mode 0600, directory 0700).
// Modes are enforced outside Windows; on Windows the store relies on the ACL
// it inherits from the config directory.
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
	if err := s.ensureDir(); err != nil {
		return err
	}
	return writeFileAtomic(s.path, append(payload, '\n'))
}

func writeFileAtomic(path string, payload []byte) error {
	dir := filepath.Dir(path)
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
// function.
//
// The lock is a sibling directory holding an owner file with a random
// per-holder token. It is staged under a unique name and renamed into place,
// so it is never visible without its owner. A lock older than the stale age
// (by its modification time on the store clock) is left over from a crashed
// process and is broken by renaming it to a unique name first: only one
// waiter's rename can succeed, so two waiters never both break it. Release
// removes the lock only while it still carries the holder's token, so a
// holder whose lock was broken never deletes its successor's lock.
func (s *FileStore) Lock(ctx context.Context) (func() error, error) {
	lockPath := s.path + lockSuffix
	if err := s.ensureDir(); err != nil {
		return nil, err
	}
	token := rand.Text()
	for {
		acquired, err := tryAcquireLock(lockPath, token)
		if err != nil {
			return nil, fmt.Errorf("lock ChatGPT auth store: %w", err)
		}
		if acquired {
			return func() error { return releaseLock(lockPath, token) }, nil
		}
		if s.breakStaleLock(lockPath) {
			continue
		}
		if err := s.sleep(ctx, s.lockPoll); err != nil {
			return nil, fmt.Errorf("wait for ChatGPT auth store lock: %w", err)
		}
	}
}

// tryAcquireLock publishes a lock directory that already holds token. It
// reports false, without error, when another holder's lock is in place.
func tryAcquireLock(lockPath, token string) (bool, error) {
	staging := lockPath + "." + token + lockStagingSuffix
	if err := os.Mkdir(staging, storeDirMode); err != nil {
		return false, err
	}
	if err := os.WriteFile(filepath.Join(staging, lockOwnerFile), []byte(token), storeFileMode); err != nil {
		return false, errors.Join(err, os.RemoveAll(staging))
	}
	// Renaming a directory onto an existing, non-empty lock directory fails
	// on every supported OS; every lock holds its owner file.
	renameErr := os.Rename(staging, lockPath)
	if renameErr == nil {
		return true, nil
	}
	cleanupErr := os.RemoveAll(staging)
	// EEXIST and ENOTEMPTY both match fs.ErrExist; Windows reports access
	// denied instead, so an existing lock also counts as contention.
	if _, err := os.Lstat(lockPath); err == nil || errors.Is(renameErr, fs.ErrExist) {
		return false, cleanupErr
	}
	return false, errors.Join(renameErr, cleanupErr)
}

func releaseLock(lockPath, token string) error {
	if lockOwner(lockPath) != token {
		// Broken as stale, and possibly taken by another process since.
		return nil
	}
	if err := takeLockFrom(lockPath, token); err != nil && !errors.Is(err, errLockChangedHands) {
		return fmt.Errorf("release ChatGPT auth store lock: %w", err)
	}
	return nil
}

func (s *FileStore) breakStaleLock(lockPath string) bool {
	info, err := os.Stat(lockPath)
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if err != nil || s.now().Sub(info.ModTime()) < s.lockStale {
		return false
	}
	return takeLockFrom(lockPath, lockOwner(lockPath)) == nil
}

// errLockChangedHands reports that the lock taken away was not the one the
// caller judged, so it was put back.
var errLockChangedHands = errors.New("ChatGPT auth store lock changed hands")

// takeLockFrom removes the lock at lockPath if it still belongs to owner. It
// first renames the lock to a unique name, which only one caller can do, and
// then checks the owner of what it took. A lock that changed hands in
// between is renamed back.
func takeLockFrom(lockPath, owner string) error {
	taken := lockPath + "." + rand.Text() + lockTakenSuffix
	if err := os.Rename(lockPath, taken); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if lockOwner(taken) != owner {
		if err := os.Rename(taken, lockPath); err == nil {
			return errLockChangedHands
		}
	}
	return os.RemoveAll(taken)
}

func lockOwner(lockDir string) string {
	owner, err := os.ReadFile(filepath.Join(lockDir, lockOwnerFile))
	if err != nil {
		return ""
	}
	return string(owner)
}

// ensureDir creates the store directory with mode 0700 and, outside Windows,
// tightens a pre-existing directory that other users can read. On Windows
// file modes are not enforced: the store relies on the ACL inherited from
// the config directory (by default under the user's profile).
func (s *FileStore) ensureDir() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, storeDirMode); err != nil {
		return fmt.Errorf("create ChatGPT auth store directory: %w", err)
	}
	if !s.enforceACL {
		return nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("inspect ChatGPT auth store directory: %w", err)
	}
	if info.Mode().Perm()&insecureModeBits == 0 {
		return nil
	}
	if err := os.Chmod(dir, storeDirMode); err != nil {
		return fmt.Errorf("restrict ChatGPT auth store directory to 0700: %w", err)
	}
	return nil
}
