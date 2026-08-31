package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	configCommitLockSuffix = ".lock"
	configCommitLockWait   = 10 * time.Second
)

var (
	// ErrConfigRevisionConflict identifies an update prepared from an older
	// on-disk config revision. Callers must surface this as a conflict rather
	// than retrying against the newer source.
	ErrConfigRevisionConflict = errors.New("config revision conflict")
	// ErrConfigCommitLockUnavailable identifies a commit lock that could not
	// be acquired within the bounded wait period.
	ErrConfigCommitLockUnavailable = errors.New("config commit lock unavailable")
)

// ConfigRevision is a content fingerprint for one config path. Exists is
// explicit so an absent file cannot compare equal to a newly-created one.
// Digest is the lowercase SHA-256 of the exact bytes on disk. For an existing
// non-regular path it contains a bounded type marker instead of file content.
type ConfigRevision struct {
	Exists bool
	Digest string
}

// Equal reports whether two revisions describe the same source bytes and
// presence state.
func (r ConfigRevision) Equal(other ConfigRevision) bool {
	return r.Exists == other.Exists && r.Digest == other.Digest
}

// ConfigRevisionConflictError reports a stale source snapshot without
// revealing configuration contents or credentials.
type ConfigRevisionConflictError struct {
	Path     string
	Expected ConfigRevision
	Actual   ConfigRevision
}

func (e *ConfigRevisionConflictError) Error() string {
	if e == nil {
		return ErrConfigRevisionConflict.Error()
	}
	return fmt.Sprintf("%s for %s: expected %s, found %s", ErrConfigRevisionConflict, filepath.Clean(e.Path), formatConfigRevision(e.Expected), formatConfigRevision(e.Actual))
}

func (e *ConfigRevisionConflictError) Unwrap() error {
	if e == nil {
		return nil
	}
	return ErrConfigRevisionConflict
}

type configAtomicWriter func(path string, data []byte, mode fs.FileMode) error

// Revision returns the exact source revision currently on disk. It performs
// no config parsing and does not use ConfigStorage's resolved-value cache.
func (s *ConfigStorage) Revision() (ConfigRevision, error) {
	if s == nil {
		return ConfigRevision{}, errors.New("config storage is nil")
	}
	return configRevisionForPath(s.configPath)
}

// Commit compares expected with the current source while holding exclusive
// commit ownership, then publishes data using a private same-directory
// temporary file and an atomic rename. The lock is intentionally acquired by
// this method, after callers have finished any network probes.
func (s *ConfigStorage) Commit(expected ConfigRevision, data []byte) (err error) {
	if s == nil {
		return errors.New("config storage is nil")
	}
	path := filepath.Clean(s.configPath)
	if path == "." || strings.TrimSpace(path) == "" {
		return errors.New("config path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	lock, err := acquireConfigCommitLock(path)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, lock.release())
	}()

	actual, err := configRevisionForPath(path)
	if err != nil {
		return fmt.Errorf("read current config revision: %w", err)
	}
	if !expected.Equal(actual) {
		return &ConfigRevisionConflictError{Path: path, Expected: expected, Actual: actual}
	}

	mode, err := configFileMode(path, actual)
	if err != nil {
		return fmt.Errorf("read config permissions: %w", err)
	}
	writer := s.atomicWriter
	if writer == nil {
		writer = writeConfigAtomically
	}
	if err := writer(path, data, mode); err != nil {
		return fmt.Errorf("write config atomically: %w", err)
	}
	return nil
}

func configRevisionForPath(path string) (ConfigRevision, error) {
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ConfigRevision{}, nil
	}
	if err != nil {
		return ConfigRevision{}, err
	}
	if !info.Mode().IsRegular() {
		return ConfigRevision{Exists: true, Digest: "non-regular:" + info.Mode().String()}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ConfigRevision{}, err
	}
	digest := sha256.Sum256(data)
	return ConfigRevision{Exists: true, Digest: hex.EncodeToString(digest[:])}, nil
}

func formatConfigRevision(revision ConfigRevision) string {
	if !revision.Exists {
		return "missing"
	}
	if strings.HasPrefix(revision.Digest, "non-regular:") {
		return revision.Digest
	}
	return "sha256:" + revision.Digest
}

func configFileMode(path string, revision ConfigRevision) (fs.FileMode, error) {
	if !revision.Exists {
		return 0o600, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("path is not a regular file")
	}
	mode := info.Mode().Perm()
	if mode == 0 {
		return 0o600, nil
	}
	return mode, nil
}

func writeConfigAtomically(path string, data []byte, mode fs.FileMode) (err error) {
	temporaryPath, err := prepareConfigTemp(path, data, mode)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("rename private temporary file: %w", err)
	}
	return nil
}

// writeConfigIfAbsentAtomically creates a complete default file without
// replacing a file that another process published after the initial absence
// check. The hard-link publication is same-directory and fails atomically when
// the destination already exists.
func writeConfigIfAbsentAtomically(path string, data []byte, mode fs.FileMode) error {
	temporaryPath, err := prepareConfigTemp(path, data, mode)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := os.Link(temporaryPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		return fmt.Errorf("publish default config: %w", err)
	}
	return nil
}

func prepareConfigTemp(path string, data []byte, mode fs.FileMode) (temporaryPath string, err error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return "", fmt.Errorf("create private temporary file: %w", err)
	}
	temporaryPath = temporary.Name()
	temporaryClosed := false
	defer func() {
		if !temporaryClosed {
			closeErr := temporary.Close()
			if err == nil && closeErr != nil {
				err = fmt.Errorf("close private temporary file: %w", closeErr)
			}
		}
		if err != nil {
			_ = os.Remove(temporaryPath)
			temporaryPath = ""
		}
	}()

	// Keep the private staging file restricted while it is visible in the
	// shared directory. Apply the destination mode only immediately before
	// publication.
	if err := temporary.Chmod(0o600); err != nil {
		return "", fmt.Errorf("set private temporary file permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return "", fmt.Errorf("write private temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return "", fmt.Errorf("flush private temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close private temporary file: %w", err)
	}
	temporaryClosed = true
	if err := os.Chmod(temporaryPath, mode.Perm()); err != nil {
		return "", fmt.Errorf("set published config permissions: %w", err)
	}
	return temporaryPath, nil
}

type configCommitLock struct {
	path string
	file *os.File
}

func acquireConfigCommitLock(path string) (*configCommitLock, error) {
	lockPath := path + configCommitLockSuffix
	deadline := time.Now().Add(configCommitLockWait)
	for {
		file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return &configCommitLock{path: lockPath, file: file}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("acquire config commit lock %s: %w", lockPath, err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w: %s", ErrConfigCommitLockUnavailable, lockPath)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (l *configCommitLock) release() error {
	if l == nil || l.file == nil {
		return nil
	}
	var errs []error
	if lockInfo, err := l.file.Stat(); err == nil {
		if pathInfo, statErr := os.Stat(l.path); statErr == nil && os.SameFile(lockInfo, pathInfo) {
			if removeErr := os.Remove(l.path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				errs = append(errs, removeErr)
			}
		}
	}
	if err := l.file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
