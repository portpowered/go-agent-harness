package config

import (
	"errors"
	"io"
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
	writeConfigFixture(t, path, original, 0o600)

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
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat newer config: %v", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Fatalf("stale commit changed config mode to %o, want 600", gotMode)
	}
	assertNoConfigCommitArtifacts(t, dir, path)
}

func TestConfigStorageCommitPreservesPermissionsAndPublishesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	writeConfigFixture(t, path, []byte("before\n"), 0o640)

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

func TestConfigStorageCommitPreservesExplicitModesAndAtomicReplacement(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode fs.FileMode
	}{
		{name: "group-readable", mode: 0o640},
		{name: "private", mode: 0o600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertAtomicConfigReplacement(t, tc.mode)
		})
	}
}

func assertAtomicConfigReplacement(t *testing.T, mode fs.FileMode) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	before := []byte("before\n")
	writeConfigFixture(t, path, before, mode)

	oldFile, err := os.Open(path)
	if err != nil {
		t.Fatalf("open old config: %v", err)
	}
	defer func() {
		if err := oldFile.Close(); err != nil {
			t.Errorf("close old config: %v", err)
		}
	}()

	storage := NewConfigStorage(path)
	expected, err := storage.Revision()
	if err != nil {
		t.Fatalf("read expected revision: %v", err)
	}
	want := []byte("after\n")
	if err := storage.Commit(expected, want); err != nil {
		t.Fatalf("commit config: %v", err)
	}

	assertPublishedConfig(t, path, want, mode)
	if _, err := oldFile.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("rewind old config descriptor: %v", err)
	}
	oldBytes, err := io.ReadAll(oldFile)
	if err != nil {
		t.Fatalf("read old config descriptor: %v", err)
	}
	if string(oldBytes) != string(before) {
		t.Fatalf("old config descriptor = %q, want %q; publication was not atomic", oldBytes, before)
	}
	assertNoConfigCommitArtifacts(t, dir, path)
}

func assertPublishedConfig(t *testing.T, path string, want []byte, mode fs.FileMode) {
	t.Helper()
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
	if gotMode := info.Mode().Perm(); gotMode != mode.Perm() {
		t.Fatalf("committed config mode = %o, want %o", gotMode, mode.Perm())
	}
}

func TestConfigStorageCommitCreatesPrivateDefaultFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	storage := NewConfigStorage(path)
	expected, err := storage.Revision()
	if err != nil {
		t.Fatalf("read missing revision: %v", err)
	}
	want := []byte("new\n")
	if err := storage.Commit(expected, want); err != nil {
		t.Fatalf("commit missing config: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read created config: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("created config = %q, want %q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat created config: %v", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Fatalf("created config mode = %o, want 600", gotMode)
	}
	assertNoConfigCommitArtifacts(t, dir, path)
}

func TestConfigStorageCommitFailureCleansLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	writeConfigFixture(t, path, []byte("before\n"), 0o640)

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
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat unchanged config: %v", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o640 {
		t.Fatalf("failed commit changed config mode to %o, want 640", gotMode)
	}
	assertNoConfigCommitArtifacts(t, dir, path)

	storage.atomicWriter = nil
	expected, err = storage.Revision()
	if err != nil {
		t.Fatalf("read revision after failed commit: %v", err)
	}
	if err := storage.Commit(expected, []byte("after\n")); err != nil {
		t.Fatalf("commit after injected failure: %v", err)
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read recovered config: %v", err)
	}
	if string(got) != "after\n" {
		t.Fatalf("recovered config = %q, want after", got)
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

func writeConfigFixture(t *testing.T, path string, data []byte, mode fs.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode.Perm()); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if err := os.Chmod(path, mode.Perm()); err != nil {
		t.Fatalf("establish config mode %o: %v", mode.Perm(), err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat seeded config: %v", err)
	}
	if got := info.Mode().Perm(); got != mode.Perm() {
		t.Fatalf("seed config mode = %o, want %o", got, mode.Perm())
	}
}
