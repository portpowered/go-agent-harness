package pathguard

import (
	"errors"
	"os"
	"path/filepath"
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
	if err := os.Symlink(external, direct); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := ValidateNoSymlink(root, direct); !errors.Is(err, errSymlink) {
		t.Fatalf("direct symlink error = %v, want symlink failure", err)
	}

	parent := filepath.Join(root, "linked")
	if err := os.Symlink(filepath.Dir(external), parent); err != nil {
		t.Skipf("parent symlink unavailable: %v", err)
	}
	if err := ValidateNoSymlink(root, filepath.Join(parent, filepath.Base(external))); !errors.Is(err, errSymlink) {
		t.Fatalf("parent symlink error = %v, want symlink failure", err)
	}
}

func TestValidateNoSymlinkRejectsSymlinkedRoot(t *testing.T) {
	target := t.TempDir()
	root := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(target, root); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := ValidateNoSymlink(root, root); !errors.Is(err, errSymlink) {
		t.Fatalf("symlinked root error = %v, want symlink failure", err)
	}
}
