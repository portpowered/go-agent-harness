package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigStorageCommitRejectsStaleRevision(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	original := []byte("model:\n  provider: openrouter\n")
	newer := []byte("model:\n  provider: local\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	storage := NewConfigStorage(path)
	expected, err := storage.Revision()
	if err != nil {
		t.Fatalf("read expected revision: %v", err)
	}
	if err := os.WriteFile(path, newer, 0o600); err != nil {
		t.Fatalf("write newer config: %v", err)
	}

	err = storage.Commit(expected, []byte("candidate"))
	if err == nil {
		t.Fatal("expected stale revision conflict")
	}
	if !errors.Is(err, ErrConfigRevisionConflict) {
		t.Fatalf("error = %v, want revision conflict", err)
	}
	var conflict *ConfigRevisionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %v, want typed revision conflict", err)
	}
	if conflict.Path != path {
		t.Fatalf("conflict path = %q, want %q", conflict.Path, path)
	}
	if !strings.Contains(err.Error(), filepath.Clean(path)) {
		t.Fatalf("error = %v, want config path", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read newer config: %v", err)
	}
	if string(got) != string(newer) {
		t.Fatalf("stale commit changed config to %q, want %q", got, newer)
	}
	assertNoConfigCommitArtifacts(t, dir, path)
}

func TestConfigStorageCommitPreservesPermissionsAndPublishesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(path, []byte("before\n"), 0o640); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	storage := NewConfigStorage(path)
	expected, err := storage.Revision()
	if err != nil {
		t.Fatalf("read expected revision: %v", err)
	}
	want := []byte("after\n")
	if err := storage.Commit(expected, want); err != nil {
		t.Fatalf("commit config: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read committed config: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("committed config = %q, want %q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat committed config: %v", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o640 {
		t.Fatalf("config mode = %o, want 640", gotMode)
	}
	assertNoConfigCommitArtifacts(t, dir, path)
}

func TestConfigStorageCommitFailureCleansLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	storage := NewConfigStorage(path)
	storage.atomicWriter = func(string, []byte, fs.FileMode) error {
		return errors.New("injected atomic write failure")
	}
	expected, err := storage.Revision()
	if err != nil {
		t.Fatalf("read expected revision: %v", err)
	}
	if err := storage.Commit(expected, []byte("candidate\n")); err == nil || !strings.Contains(err.Error(), "injected atomic write failure") {
		t.Fatalf("commit error = %v, want injected write failure", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read unchanged config: %v", err)
	}
	if string(got) != "before\n" {
		t.Fatalf("failed commit changed config to %q", got)
	}
	assertNoConfigCommitArtifacts(t, dir, path)
}

func TestWriteConfigAtomicallyCleansTemporaryFileOnRenameFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("create destination directory: %v", err)
	}

	if err := writeConfigAtomically(path, []byte("candidate\n"), 0o600); err == nil {
		t.Fatal("expected rename failure")
	}
	assertNoConfigCommitArtifacts(t, dir, path)
}

func assertNoConfigCommitArtifacts(t *testing.T, dir, path string) {
	t.Helper()
	if _, err := os.Stat(path + configCommitLockSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("commit lock stat error = %v, want absent", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "."+filepath.Base(path)+".tmp-*"))
	if err != nil {
		t.Fatalf("glob private config artifacts: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("private config artifacts remain: %v", matches)
	}
}
