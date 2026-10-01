package pathguard

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestValidateNoSymlinkChecksComponentsAndContainment(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	regular := filepath.Join(nested, "artifact.pcm")
	if err := os.WriteFile(regular, []byte{1, 2}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateNoSymlink(root, regular); err != nil {
		t.Fatalf("regular path rejected: %v", err)
	}
	if err := ValidateNoSymlink(root, filepath.Join(root, "missing")); err != nil {
		t.Fatalf("missing path should remain available to the caller: %v", err)
	}

	external := filepath.Join(t.TempDir(), "outside.pcm")
	if err := os.WriteFile(external, []byte{3, 4}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateNoSymlink(root, external); !errors.Is(err, errOutside) {
		t.Fatalf("outside path error = %v, want containment failure", err)
	}

	direct := filepath.Join(nested, "direct.pcm")
	if !symlinkOrUnsupported(t, external, direct) {
		return
	}
	if err := ValidateNoSymlink(root, direct); !errors.Is(err, errSymlink) {
		t.Fatalf("direct symlink error = %v, want symlink failure", err)
	}

	parent := filepath.Join(root, "linked")
	if !symlinkOrUnsupported(t, filepath.Dir(external), parent) {
		return
	}
	if err := ValidateNoSymlink(root, filepath.Join(parent, filepath.Base(external))); !errors.Is(err, errSymlink) {
		t.Fatalf("parent symlink error = %v, want symlink failure", err)
	}
}

func TestValidateNoSymlinkRejectsSymlinkedRoot(t *testing.T) {
	target := t.TempDir()
	root := filepath.Join(t.TempDir(), "root-link")
	if !symlinkOrUnsupported(t, target, root) {
		return
	}
	if err := ValidateNoSymlink(root, root); !errors.Is(err, errSymlink) {
		t.Fatalf("symlinked root error = %v, want symlink failure", err)
	}
}

// symlinkOrUnsupported creates link pointing at target. Windows creates
// symlinks only with Developer Mode or the create-symbolic-link privilege;
// without it the capability is absent, so this reports false and the caller
// ends its symlink assertions. Every other platform supports symlinks, so a
// failure there is fatal.
func symlinkOrUnsupported(t *testing.T, target, link string) bool {
	t.Helper()
	err := os.Symlink(target, link)
	if err == nil {
		return true
	}
	if runtime.GOOS == "windows" {
		t.Logf("symlink capability unavailable: %v", err)
		return false
	}
	t.Fatalf("create symlink %s -> %s: %v", link, target, err)
	return false
}
