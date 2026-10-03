//go:build unix

package filesystem

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	core "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal"
)

// These tests swap a symlink in between the path pre-check and the os.Root
// open, through the afterPolicyCheck seam, so each one replays the race
// deterministically. The pre-check resolves the path to its canonical form,
// so the attack replaces a real directory on that canonical path
// (work/benign) with a symlink to the credential store. The scope root is the
// parent of ~/.ssh (as with --allow-path ~ or --allow-path /), so os.Root
// itself contains the store and only the open-time guard can refuse the
// swapped path.

const (
	swapSecretKey      = "PRIVATE KEY"
	swapSecretKeys     = "ORIGINAL AUTHORIZED KEYS"
	swapBenignKey      = "benign key"
	swapBenignKeys     = "benign keys"
	swapLinkToSSH      = "../.ssh"
	swapLinkToHome     = ".."
	swapReadPath       = "work/benign/id_rsa"
	swapKeysPath       = "work/benign/authorized_keys"
	swapSecretImageTag = "secret-image"
)

type swapFixture struct {
	home    string
	ssh     string
	swapped string
	policy  *FilesystemPolicy
}

// newSwapFixture builds home/user with ~/.ssh and an ordinary directory
// work/benign (plus work/link, an in-root link to it), and a policy rooted at
// home/user.
func newSwapFixture(t *testing.T) *swapFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home", "user")
	ssh := filepath.Join(home, ".ssh")
	benign := filepath.Join(home, "work", "benign")
	for _, dir := range []string{ssh, benign} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string][]byte{
		filepath.Join(ssh, "id_rsa"):             []byte(swapSecretKey),
		filepath.Join(ssh, "authorized_keys"):    []byte(swapSecretKeys),
		filepath.Join(ssh, "key.png"):            append(minimalPNG(), swapSecretImageTag...),
		filepath.Join(benign, "id_rsa"):          []byte(swapBenignKey),
		filepath.Join(benign, "authorized_keys"): []byte(swapBenignKeys),
		filepath.Join(benign, "key.png"):         minimalPNG(),
	}
	for path, data := range files {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("benign", filepath.Join(home, "work", "link")); err != nil {
		t.Fatal(err)
	}
	policy, err := NewFilesystemPolicy(home)
	if err != nil {
		t.Fatalf("NewFilesystemPolicy: %v", err)
	}
	return &swapFixture{home: home, ssh: ssh, swapped: benign, policy: policy.WithHomeDir(home)}
}

// swapOn returns a seam that, on the call-th check (1-based), moves
// work/benign aside and puts a symlink to target in its place, and counts
// the checks.
func (f *swapFixture) swapOn(t *testing.T, call int, target string) (func(string), *int) {
	t.Helper()
	calls := 0
	return func(string) {
		calls++
		if calls != call {
			return
		}
		if err := os.Rename(f.swapped, f.swapped+".moved"); err != nil {
			t.Errorf("move the checked directory aside: %v", err)
			return
		}
		if err := os.Symlink(target, f.swapped); err != nil {
			t.Errorf("swap in the link: %v", err)
		}
	}, &calls
}

func (f *swapFixture) requireFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("%s = %q, %v; want %q unchanged", path, got, err, want)
	}
}

// requireSSHUnchanged proves the store holds exactly the fixture's files.
func (f *swapFixture) requireSSHUnchanged(t *testing.T) {
	t.Helper()
	f.requireFile(t, filepath.Join(f.ssh, "authorized_keys"), swapSecretKeys)
	entries, err := os.ReadDir(f.ssh)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if strings.Join(names, ",") != "authorized_keys,id_rsa,key.png" {
		t.Fatalf("~/.ssh entries = %v, want only the fixture's files", names)
	}
}

func setSwapSeam(t *testing.T, tool core.Tool, seam func(string)) {
	t.Helper()
	var fs fileSystem
	switch typed := tool.(type) {
	case *ReadFileTool:
		fs = typed.fs
	case *ListDirTool:
		fs = typed.fs
	case *WriteFileTool:
		fs = typed.fs
	case *AppendFileTool:
		fs = typed.fs
	case *EditFileTool:
		fs = typed.fs
	case *ReadImageTool:
		fs = typed.fs
	}
	sandbox, ok := fs.(*sandboxFs)
	if !ok {
		t.Fatalf("%T has no policy sandbox", tool)
	}
	sandbox.afterPolicyCheck = seam
}

func requireProtectedRefusal(t *testing.T, msgs []messages.Message, reason FilesystemRefusalReason, leaked ...string) {
	t.Helper()
	if len(msgs) != 1 {
		t.Fatalf("messages = %#v, want one refusal", msgs)
	}
	content := msgs[0].TextContent()
	refusal, ok := FilesystemRefusalFromContent(content)
	if !ok || refusal.Reason != reason {
		t.Fatalf("result = %q, want a %s refusal", content, reason)
	}
	for _, secret := range leaked {
		if strings.Contains(content, secret) {
			t.Fatalf("refusal leaked %q: %q", secret, content)
		}
	}
}

func TestOpenTimeGuard_ReadFileRefusesALinkSwappedToTheCredentialStore(t *testing.T) {
	f := newSwapFixture(t)
	tool := NewReadFileToolWithPolicy(f.policy)
	seam, calls := f.swapOn(t, 1, swapLinkToSSH)
	setSwapSeam(t, tool, seam)
	msgs := mustToolExecute(t, tool, map[string]any{"path": swapReadPath})
	if *calls != 1 {
		t.Fatalf("seam ran %d times, want once", *calls)
	}
	requireProtectedRefusal(t, msgs, FilesystemRefusalSensitiveRead, swapSecretKey)
}

func TestOpenTimeGuard_ListDirRefusesALinkSwappedToTheCredentialStore(t *testing.T) {
	f := newSwapFixture(t)
	tool := NewListDirToolWithPolicy(f.policy)
	seam, _ := f.swapOn(t, 1, swapLinkToSSH)
	setSwapSeam(t, tool, seam)
	msgs := mustToolExecute(t, tool, map[string]any{"path": "work/benign"})
	requireProtectedRefusal(t, msgs, FilesystemRefusalSensitiveRead, "id_rsa", "authorized_keys")
}

func TestOpenTimeGuard_ReadImageRefusesALinkSwappedToTheCredentialStore(t *testing.T) {
	f := newSwapFixture(t)
	var prepared [][]byte
	tool := NewReadImageToolWithPolicy(f.policy, func(sources []ImageSource) ([]messages.ImagePart, error) {
		for _, source := range sources {
			prepared = append(prepared, source.Bytes)
		}
		return []messages.ImagePart{{Bytes: minimalPNG(), MediaType: imagePNGMediaType}}, nil
	})
	seam, _ := f.swapOn(t, 1, swapLinkToSSH)
	setSwapSeam(t, tool, seam)
	msgs := mustToolExecute(t, tool, map[string]any{"path": "work/benign/key.png"})
	for _, data := range prepared {
		if bytes.Contains(data, []byte(swapSecretImageTag)) {
			t.Fatal("read_image handed the credential store's image to the preparer")
		}
	}
	requireProtectedRefusal(t, msgs, FilesystemRefusalSensitiveRead)
}

func TestOpenTimeGuard_WriteFileRefusesALinkSwappedToTheCredentialStore(t *testing.T) {
	f := newSwapFixture(t)
	tool := NewWriteFileToolWithPolicy(f.policy)
	seam, _ := f.swapOn(t, 1, swapLinkToSSH)
	setSwapSeam(t, tool, seam)
	msgs := mustToolExecute(t, tool, map[string]any{"path": swapKeysPath, "content": "ssh-ed25519 ATTACKER"})
	requireProtectedRefusal(t, msgs, FilesystemRefusalSensitiveWrite)
	f.requireSSHUnchanged(t)
}

// Append and edit read, then write; each phase has its own check, so the
// swap lands before the read or before the write.
func TestOpenTimeGuard_AppendFileRefusesALinkSwappedToTheCredentialStore(t *testing.T) {
	for _, test := range []struct {
		name   string
		call   int
		reason FilesystemRefusalReason
	}{
		{name: "before the read", call: 1, reason: FilesystemRefusalSensitiveRead},
		{name: "before the write", call: 2, reason: FilesystemRefusalSensitiveWrite},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newSwapFixture(t)
			tool := NewAppendFileToolWithPolicy(f.policy)
			seam, _ := f.swapOn(t, test.call, swapLinkToSSH)
			setSwapSeam(t, tool, seam)
			msgs := mustToolExecute(t, tool, map[string]any{"path": swapKeysPath, "content": "\nssh-ed25519 ATTACKER"})
			requireProtectedRefusal(t, msgs, test.reason, swapSecretKeys)
			f.requireSSHUnchanged(t)
		})
	}
}

func TestOpenTimeGuard_EditFileRefusesALinkSwappedToTheCredentialStore(t *testing.T) {
	for _, test := range []struct {
		name    string
		call    int
		oldText string
		reason  FilesystemRefusalReason
	}{
		{name: "before the read", call: 1, oldText: swapSecretKeys, reason: FilesystemRefusalSensitiveRead},
		{name: "before the write", call: 2, oldText: swapBenignKeys, reason: FilesystemRefusalSensitiveWrite},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newSwapFixture(t)
			tool := NewEditFileToolWithPolicy(f.policy)
			seam, _ := f.swapOn(t, test.call, swapLinkToSSH)
			setSwapSeam(t, tool, seam)
			msgs := mustToolExecute(t, tool, map[string]any{"path": swapKeysPath, "old_text": test.oldText, "new_text": "ssh-ed25519 ATTACKER"})
			requireProtectedRefusal(t, msgs, test.reason, swapSecretKeys)
			f.requireSSHUnchanged(t)
		})
	}
}

func TestOpenTimeGuard_WriteFileCreatesNoDirectoryInTheCredentialStore(t *testing.T) {
	f := newSwapFixture(t)
	tool := NewWriteFileToolWithPolicy(f.policy)
	seam, _ := f.swapOn(t, 1, swapLinkToSSH)
	setSwapSeam(t, tool, seam)
	msgs := mustToolExecute(t, tool, map[string]any{"path": "work/benign/new/dir/file.txt", "content": "planted"})
	requireProtectedRefusal(t, msgs, FilesystemRefusalSensitiveWrite)
	f.requireSSHUnchanged(t)
}

// A swap to the home directory must not create a protected root that does
// not exist yet: a file (~/.netrc), or a directory whose parent the write
// itself creates (~/.config, then ~/.config/gh).
func TestOpenTimeGuard_WriteFileCreatesNoMissingProtectedRoot(t *testing.T) {
	for _, test := range []struct {
		path    string
		created string
	}{
		{path: "work/benign/.netrc", created: ".netrc"},
		{path: "work/benign/.config/gh/hosts.yml", created: filepath.Join(".config", "gh")},
		{path: "work/benign/.ssh/authorized_keys", created: ".ssh"},
	} {
		t.Run(test.path, func(t *testing.T) {
			f := newSwapFixture(t)
			if err := os.RemoveAll(f.ssh); err != nil {
				t.Fatal(err)
			}
			tool := NewWriteFileToolWithPolicy(f.policy)
			seam, _ := f.swapOn(t, 1, swapLinkToHome)
			setSwapSeam(t, tool, seam)
			msgs := mustToolExecute(t, tool, map[string]any{"path": test.path, "content": "machine example.com password planted"})
			if refusal, ok := FilesystemRefusalFromContent(msgs[0].TextContent()); !ok || refusal.Reason != FilesystemRefusalSensitiveWrite {
				t.Fatalf("result = %q, want a protected-write refusal", msgs[0].TextContent())
			}
			if _, err := os.Lstat(filepath.Join(f.home, test.created)); !os.IsNotExist(err) {
				t.Fatalf("~/%s exists after the refused write: %v", test.created, err)
			}
		})
	}
}

// The pre-check hands the guard a canonical path, so the guard meets a
// symlink only when one is swapped in. Called directly, it keeps os.Root's
// symlink behavior: in-root links are followed, a write through a link to an
// existing file is in place, a dangling link is replaced, and absolute,
// escaping, looping and over-long chains of links fail.
func TestOpenTimeGuard_FollowsInRootSymlinksLikeOSRoot(t *testing.T) {
	f := newSwapFixture(t)
	work := filepath.Join(f.home, "work")
	links := map[string]string{fmt.Sprintf("hop-%d", maxSymlinkHops+1): "benign/id_rsa", "dangling": "missing.txt", "absolute": filepath.Join(work, "benign", "id_rsa"), "escape": "../../../outside", "loop-a": "loop-b", "loop-b": "loop-a", "chain": "link/id_rsa", "to-ssh": "../.ssh/id_rsa"}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(work, name)); err != nil {
			t.Fatal(err)
		}
	}
	for hop := range maxSymlinkHops + 1 {
		if err := os.Symlink(fmt.Sprintf("hop-%d", hop+1), filepath.Join(work, fmt.Sprintf("hop-%d", hop))); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(f.home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeSandboxRoot(root) })
	sandbox := newSandboxFs(f.policy)
	guard := newOpenGuard(root, snapshotProtectedIdentities(sandbox.protectedRoots()), sandbox.protectedRoots(), sandbox.protectedWriteDenial)
	if got, err := guard.readFile("work/chain"); err != nil || string(got) != swapBenignKey {
		t.Fatalf("read through a link chain = %q, %v", got, err)
	}
	if err := guard.writeFile("work/chain", []byte("rewritten")); err != nil {
		t.Fatalf("write through a link chain: %v", err)
	}
	if info, err := os.Lstat(filepath.Join(work, "chain")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("write through a link replaced the link: %v, %v", info, err)
	}
	f.requireFile(t, filepath.Join(work, "benign", "id_rsa"), "rewritten")
	if err := guard.writeFile("work/dangling", []byte("now a file")); err != nil {
		t.Fatalf("write over a dangling link: %v", err)
	}
	if info, err := os.Lstat(filepath.Join(work, "dangling")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("dangling link after write = %v, %v; want a regular file", info, err)
	}
	for _, path := range []string{"work/absolute", "work/escape", "work/loop-a", "work/hop-0"} {
		if _, err := guard.readFile(path); err == nil {
			t.Fatalf("read of %s succeeded, want an error", path)
		}
		if err := guard.writeFile(path, []byte("x")); err == nil {
			t.Fatalf("write of %s succeeded, want an error", path)
		}
	}
	if _, err := guard.readFile("work/to-ssh"); !errors.Is(err, ErrProtectedFilesystemWrite) {
		t.Fatalf("read through a link into ~/.ssh = %v, want the protected refusal", err)
	}
	if err := guard.writeFile("work/to-ssh", []byte("x")); !errors.Is(err, ErrProtectedFilesystemWrite) {
		t.Fatalf("write through a link into ~/.ssh = %v, want the protected refusal", err)
	}
	f.requireSSHUnchanged(t)
	if entries, err := guard.readDir("work/link"); err != nil || len(entries) != 3 || entries[0].Name() != "authorized_keys" {
		t.Fatalf("list through an in-root link = %v, %v", entries, err)
	}
	if err := guard.writeFile("work/new/nested/file.txt", []byte("nested")); err != nil {
		t.Fatalf("write creating parents: %v", err)
	}
	f.requireFile(t, filepath.Join(work, "new", "nested", "file.txt"), "nested")
	if err := guard.writeFile(".", []byte("x")); err == nil {
		t.Fatal("write of the root itself succeeded")
	}
}
