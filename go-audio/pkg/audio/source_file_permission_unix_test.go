//go:build unix

package audio

import (
	"os"
	"path/filepath"
	"testing"
)

// Mode bits gate opens only on Unix; Windows ignores them, so this test is
// built for Unix alone. The superuser bypasses mode bits, and the source must
// then honour the operating system's decision and open the file.
func TestFileSourceModeZeroFileOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "permission-denied.raw")
	if err := os.WriteFile(path, pcmBytes([]int16{1}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}

	source, err := NewFileSource(path, nil)
	if os.Geteuid() == 0 {
		if err != nil {
			t.Fatalf("NewFileSource() as superuser = %v, want the OS to allow the open", err)
		}
		closeForTest(t, source)
		return
	}
	assertSourceStreamError(t, err, "open", path)
}
