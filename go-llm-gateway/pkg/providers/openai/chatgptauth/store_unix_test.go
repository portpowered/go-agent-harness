//go:build !windows

package chatgptauth

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileStoreWritesOwnerOnlyFilesAndRefusesReadableOnes(t *testing.T) {
	store := newTestStore(t, newVirtualClock(), nil)
	if err := store.Save(storedCredentialFixture(t, testEpoch().Add(time.Hour))); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fileInfo, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	dirInfo, err := os.Stat(filepath.Dir(store.Path()))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if fileInfo.Mode().Perm() != storeFileMode || dirInfo.Mode().Perm() != storeDirMode {
		t.Fatalf("modes = file %v, dir %v; want 0600 and 0700", fileInfo.Mode().Perm(), dirInfo.Mode().Perm())
	}
	if err := os.Chmod(store.Path(), 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, err := store.Load(); !errors.Is(err, ErrInsecureStore) {
		t.Fatalf("Load of a world-readable store = %v, want ErrInsecureStore", err)
	}
	if err := store.Save(storedCredentialFixture(t, time.Time{})); err != nil {
		t.Fatalf("re-Save: %v", err)
	}
	if _, err := store.Load(); err != nil {
		t.Fatalf("Load after re-Save restored 0600 = %v", err)
	}
}

func TestFileStoreTightensALooseStoreDirectory(t *testing.T) {
	store := newTestStore(t, newVirtualClock(), nil)
	dir := filepath.Dir(store.Path())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := store.Save(storedCredentialFixture(t, time.Time{})); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != storeDirMode {
		t.Fatalf("store directory mode = %v, want 0700", info.Mode().Perm())
	}
}
