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
// An OS advisory lock on a sibling file serializes refresh across processes, because
// refresh tokens rotate and a concurrent refresh would log one process out.
type FileStore struct {
	path       string
	sleep      Sleeper
	lockPoll   time.Duration
	enforceACL bool
}

// StoreOption configures a FileStore.
type StoreOption func(*FileStore)

// WithStoreSleeper sets how a waiter pauses between lock attempts.
func WithStoreSleeper(sleep Sleeper) StoreOption {
	return func(s *FileStore) { s.sleep = sleep }
}

// NewFileStore returns a store for the credential file at path.
func NewFileStore(path string, options ...StoreOption) *FileStore {
	s := &FileStore{
		path:       path,
		sleep:      WaitContext,
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

// StoreLock is a held store lock: an OS advisory lock on the store's
// sibling lock file.
type StoreLock struct {
	file *os.File
}

// Release releases the lock and closes its file. The lock file itself is
// kept: deleting it would let a waiter that opened the old file and a newer
// caller that created a fresh one both hold "the" lock.
func (l *StoreLock) Release() error {
	return errors.Join(unlockFile(l.file), l.file.Close())
}

// Lock takes the store's cross-process lock, waiting through the store's
// sleeper while another holder has it.
//
// The lock is an OS advisory lock (flock on Unix, LockFileEx on Windows) on
// the sibling file <store>.lock. The operating system releases it when its
// holder exits or crashes, so there is no staleness to judge and no way for
// two processes to hold it at once. The lock belongs to the open file, so
// two Lock calls in one process also exclude each other.
func (s *FileStore) Lock(ctx context.Context) (*StoreLock, error) {
	if err := s.ensureDir(); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(s.path+lockSuffix, os.O_RDWR|os.O_CREATE, storeFileMode)
	if err != nil {
		return nil, fmt.Errorf("open ChatGPT auth store lock: %w", err)
	}
	for {
		acquired, err := tryLockFile(file)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("lock ChatGPT auth store: %w", err), file.Close())
		}
		if acquired {
			return &StoreLock{file: file}, nil
		}
		if err := s.sleep(ctx, s.lockPoll); err != nil {
			return nil, errors.Join(fmt.Errorf("wait for ChatGPT auth store lock: %w", err), file.Close())
		}
	}
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
