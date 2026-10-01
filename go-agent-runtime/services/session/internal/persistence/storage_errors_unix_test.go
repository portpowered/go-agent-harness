//go:build unix

package session

import (
	"os"
	"testing"
)

// Windows permission bits do not reliably prevent writes, so the read-only
// directory contract is exercised only on unix; non-directory failures cover
// write errors on every platform.
func TestStorage_ErrorPaths_unwritable_storage_root(t *testing.T) {
	st := NewStorage(t.TempDir())
	if err := os.MkdirAll(st.sessionsDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Chmod(st.sessionsDir, 0500); err != nil {
		t.Fatalf("Chmod read-only sessions dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(st.sessionsDir, 0700); err != nil {
			t.Errorf("restore directory permissions: %v", err)
		}
	})
	err := st.Save("permission-failure", nil)
	if os.Geteuid() == 0 {
		// The superuser writes through permission bits.
		if err != nil {
			t.Fatalf("Save as superuser into read-only dir: %v", err)
		}
		return
	}
	requirePathError(t, err, "write session permission-failure:")
}
