package filesystem

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// homeCredentialFixture builds <base>/home/user with ~/.ssh/id_rsa and an
// ordinary file next to it, and returns the base, home and key paths.
func homeCredentialFixture(t *testing.T) (base, home, key string) {
	t.Helper()
	base = t.TempDir()
	home = filepath.Join(base, "home", "user")
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	key = filepath.Join(home, ".ssh", "id_rsa")
	if err := os.WriteFile(key, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "notes.txt"), []byte("ordinary notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	return base, home, key
}

func filesystemRoot(path string) string {
	return filepath.VolumeName(path) + string(filepath.Separator)
}

// TestFilesystemPolicy_HomeCredentialStoresRefusedUnderBroadRoots proves that
// --allow-path / and --allow-path <parent of HOME> cannot expose ~/.ssh through
// read_file, list_dir, read_image or a symlink in the workdir.
func TestFilesystemPolicy_HomeCredentialStoresRefusedUnderBroadRoots(t *testing.T) {
	base, home, key := homeCredentialFixture(t)
	for _, test := range []struct {
		name string
		root string
	}{
		{name: "allow-path filesystem root", root: filesystemRoot(base)},
		{name: "allow-path parent of HOME", root: base},
	} {
		t.Run(test.name, func(t *testing.T) {
			primary := t.TempDir()
			unprotected, err := NewFilesystemPolicy(primary, test.root)
			if err != nil {
				t.Fatalf("NewFilesystemPolicy: %v", err)
			}
			msgs, err := NewReadFileToolWithPolicy(unprotected).Execute(context.Background(), map[string]any{"path": key})
			requireToolText(t, msgs, err, "PRIVATE KEY")

			policy := unprotected.WithHomeDir(home)
			assertHomeCredentialRefused(t, policy, key)

			link := filepath.Join(primary, "key-link")
			if err := os.Symlink(key, link); err != nil {
				t.Logf("symlink setup unavailable: %v", err)
			} else {
				msgs, err := NewReadFileToolWithPolicy(policy).Execute(context.Background(), map[string]any{"path": link})
				got := requireToolTextContains(t, msgs, err, ErrFilesystemAccessDenied.Error())
				if strings.Contains(got, "PRIVATE KEY") {
					t.Fatalf("symlinked credential read leaked content: %q", got)
				}
			}

			msgs, err = NewReadFileToolWithPolicy(policy).Execute(context.Background(), map[string]any{"path": filepath.Join(home, "notes.txt")})
			requireToolText(t, msgs, err, "ordinary notes")
		})
	}
}

func assertHomeCredentialRefused(t *testing.T, policy *FilesystemPolicy, key string) {
	t.Helper()
	ctx := context.Background()
	msgs, err := NewReadFileToolWithPolicy(policy).Execute(ctx, map[string]any{"path": key})
	if got := requireToolTextContains(t, msgs, err, ErrFilesystemAccessDenied.Error()); strings.Contains(got, "PRIVATE KEY") {
		t.Fatalf("read_file leaked the credential: %q", got)
	}
	msgs, err = NewListDirToolWithPolicy(policy).Execute(ctx, map[string]any{"path": filepath.Dir(key)})
	if got := requireToolTextContains(t, msgs, err, ErrFilesystemAccessDenied.Error()); strings.Contains(got, "id_rsa") {
		t.Fatalf("list_dir leaked the credential directory: %q", got)
	}
	preparerCalled := false
	msgs, err = NewReadImageToolWithPolicy(policy, func([]string) ([]messages.ImagePart, error) {
		preparerCalled = true
		return nil, nil
	}).Execute(ctx, map[string]any{"path": key})
	if err != nil {
		t.Fatalf("read_image returned Go error: %v", err)
	}
	var result ReadImageResult
	if preparerCalled || len(msgs) != 1 || json.Unmarshal([]byte(msgs[0].TextContent()), &result) != nil ||
		result.Status != ReadImageResultStatusError || !strings.Contains(result.Error, ErrFilesystemAccessDenied.Error()) {
		t.Fatalf("read_image of a home credential = %#v (preparer called %t), want a protected-read refusal", msgs, preparerCalled)
	}
	if err := policy.AuthorizeRead(key); err == nil {
		t.Fatal("AuthorizeRead allowed a home credential")
	}
}

func TestFilesystemPolicy_WithHomeDirProtectsHomeOutsideScopeAndEmptyHome(t *testing.T) {
	_, home, key := homeCredentialFixture(t)
	primary := t.TempDir()
	policy, err := NewFilesystemPolicy(primary)
	if err != nil {
		t.Fatal(err)
	}
	if policy.WithHomeDir("") != policy || (*FilesystemPolicy)(nil).WithHomeDir(home) != nil {
		t.Fatal("an empty home or nil policy must be returned unchanged")
	}
	scoped := policy.WithHomeDir(home)
	if !slices.Contains(scoped.ProtectedReadRoots(), filepath.Join(home, ".ssh")) {
		t.Fatalf("protected roots %v do not include the home .ssh", scoped.ProtectedReadRoots())
	}
	msgs, err := NewReadFileToolWithPolicy(scoped).Execute(context.Background(), map[string]any{"path": key})
	requireToolTextContains(t, msgs, err, `"reason":"sensitive_read"`)
}

// TestFilesystemPolicy_ScopeRootInsideCredentialStoreStaysProtected proves
// --allow-path ~/.ssh/keys cannot authorize reading ~/.ssh/keys/k.
func TestFilesystemPolicy_ScopeRootInsideCredentialStoreStaysProtected(t *testing.T) {
	_, home, _ := homeCredentialFixture(t)
	keys := filepath.Join(home, ".ssh", "keys")
	if err := os.Mkdir(keys, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keys, "k"), []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := NewFilesystemPolicy(t.TempDir(), keys)
	if err != nil {
		t.Fatal(err)
	}
	policy = policy.WithHomeDir(home)
	msgs, err := NewReadFileToolWithPolicy(policy).Execute(context.Background(), map[string]any{"path": filepath.Join(keys, "k")})
	if got := requireToolTextContains(t, msgs, err, ErrFilesystemAccessDenied.Error()); strings.Contains(got, "PRIVATE KEY") {
		t.Fatalf("read below a scope root inside ~/.ssh leaked the key: %q", got)
	}
	msgs, err = NewWriteFileToolWithPolicy(policy).Execute(context.Background(), map[string]any{"path": filepath.Join(keys, "planted"), "content": "x"})
	requireToolTextContains(t, msgs, err, `"reason":"sensitive_write"`)
}

// TestFilesystemPolicy_WritesIntoCredentialStoresRefused proves write_file,
// append_file and edit_file cannot plant or change ~/.ssh/authorized_keys or
// create ~/.aws/credentials, under a broad root, under --allow-path $HOME and
// directly under a scope root with no home injected.
func TestFilesystemPolicy_WritesIntoCredentialStoresRefused(t *testing.T) {
	base, home, _ := homeCredentialFixture(t)
	authorized := filepath.Join(home, ".ssh", "authorized_keys")
	if err := os.WriteFile(authorized, []byte("ssh-ed25519 OWNER"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		policy *FilesystemPolicy
	}{
		{name: "allow-path filesystem root", policy: mustPolicy(t, t.TempDir(), filesystemRoot(base)).WithHomeDir(home)},
		{name: "allow-path HOME", policy: mustPolicy(t, t.TempDir(), home).WithHomeDir(home)},
		{name: "scope root without home", policy: mustPolicy(t, home)},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy := test.policy
			ctx := context.Background()
			for _, call := range []struct {
				tool interface {
					Execute(context.Context, map[string]any) ([]messages.Message, error)
				}
				args map[string]any
			}{
				{NewWriteFileToolWithPolicy(policy), map[string]any{"path": authorized, "content": "ssh-ed25519 ATTACKER"}},
				{NewAppendFileToolWithPolicy(policy), map[string]any{"path": authorized, "content": "ssh-ed25519 ATTACKER"}},
				{NewEditFileToolWithPolicy(policy), map[string]any{"path": authorized, "old_text": "OWNER", "new_text": "ATTACKER"}},
				{NewWriteFileToolWithPolicy(policy), map[string]any{"path": filepath.Join(home, ".ssh", "id_new"), "content": "planted"}},
				{NewWriteFileToolWithPolicy(policy), map[string]any{"path": filepath.Join(home, ".aws", "credentials"), "content": "planted"}},
			} {
				msgs, err := call.tool.Execute(ctx, call.args)
				requireToolTextContains(t, msgs, err, ErrFilesystemAccessDenied.Error())
			}
			if got, err := os.ReadFile(authorized); err != nil || string(got) != "ssh-ed25519 OWNER" {
				t.Fatalf("authorized_keys = %q, %v; want it unchanged", got, err)
			}
			for _, planted := range []string{filepath.Join(home, ".ssh", "id_new"), filepath.Join(home, ".aws")} {
				if _, err := os.Lstat(planted); !os.IsNotExist(err) {
					t.Fatalf("%s was created (err %v)", planted, err)
				}
			}
			notes := filepath.Join(home, "notes.txt")
			msgs, err := NewWriteFileToolWithPolicy(policy).Execute(ctx, map[string]any{"path": notes, "content": "updated notes"})
			if err != nil || len(msgs) != 1 || strings.Contains(msgs[0].TextContent(), ErrFilesystemAccessDenied.Error()) {
				t.Fatalf("ordinary write under HOME = %#v, %v", msgs, err)
			}
		})
	}
}

func mustPolicy(t *testing.T, primary string, additional ...string) *FilesystemPolicy {
	t.Helper()
	policy, err := NewFilesystemPolicy(primary, additional...)
	if err != nil {
		t.Fatalf("NewFilesystemPolicy: %v", err)
	}
	return policy
}

// TestFilesystemPolicy_MixedCaseCredentialSpellings proves that on a
// case-insensitive filesystem ~/.SSH, ~/.Aws and a mixed-case Keychains
// spelling are refused through read, list and a symlink, both under a broad
// root and directly under a scope root.
func TestFilesystemPolicy_MixedCaseCredentialSpellings(t *testing.T) {
	base, home, _ := homeCredentialFixture(t)
	for _, store := range []string{filepath.Join(home, ".aws"), filepath.Join(home, "Library", "Keychains")} {
		if err := os.MkdirAll(store, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(store, "credentials"), []byte("SECRET"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	spellings := []string{
		filepath.Join(home, ".SSH", "id_rsa"),
		filepath.Join(home, ".Aws", "credentials"),
		filepath.Join(home, "library", "KEYCHAINS", "credentials"),
	}
	_, statErr := os.Stat(spellings[0])
	caseInsensitiveFS := statErr == nil
	policies := map[string]*FilesystemPolicy{
		"allow-path parent of HOME":  mustPolicy(t, t.TempDir(), base).WithHomeDir(home),
		"allow-path filesystem root": mustPolicy(t, t.TempDir(), filesystemRoot(base)).WithHomeDir(home),
		"scope root HOME":            mustPolicy(t, home),
	}
	for name, policy := range policies {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			for _, spelling := range spellings {
				assertMixedCaseSpellingRefused(t, policy, spelling, caseInsensitiveFS)
			}
			link := filepath.Join(policy.PrimaryRoot(), "upper-ssh")
			if err := os.Symlink(filepath.Join(home, ".SSH"), link); err == nil {
				msgs, err := NewReadFileToolWithPolicy(policy).Execute(ctx, map[string]any{"path": filepath.Join(link, "id_rsa")})
				if err != nil || len(msgs) != 1 || strings.Contains(msgs[0].TextContent(), "PRIVATE KEY") {
					t.Fatalf("read through a mixed-case symlink = %#v, %v", msgs, err)
				}
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// assertMixedCaseSpellingRefused reads and lists one mixed-case spelling. It
// never leaks a credential; on a case-insensitive filesystem, where the
// spelling names the real store, both operations are refused.
func assertMixedCaseSpellingRefused(t *testing.T, policy *FilesystemPolicy, spelling string, caseInsensitiveFS bool) {
	t.Helper()
	ctx := context.Background()
	msgs, err := NewReadFileToolWithPolicy(policy).Execute(ctx, map[string]any{"path": spelling})
	if err != nil || len(msgs) != 1 {
		t.Fatalf("read %s = %#v, %v", spelling, msgs, err)
	}
	got := msgs[0].TextContent()
	if strings.Contains(got, "PRIVATE KEY") || strings.Contains(got, "SECRET") {
		t.Fatalf("read %s leaked a credential: %q", spelling, got)
	}
	if caseInsensitiveFS && !strings.Contains(got, ErrFilesystemAccessDenied.Error()) {
		t.Fatalf("read %s = %q, want a protected refusal", spelling, got)
	}
	msgs, err = NewListDirToolWithPolicy(policy).Execute(ctx, map[string]any{"path": filepath.Dir(spelling)})
	if err == nil && len(msgs) == 1 && caseInsensitiveFS && !strings.Contains(msgs[0].TextContent(), ErrFilesystemAccessDenied.Error()) {
		t.Fatalf("list %s = %q, want a protected refusal", filepath.Dir(spelling), msgs[0].TextContent())
	}
}

func TestIsWithinProtectedRootFoldsCaseOnCaseInsensitivePlatforms(t *testing.T) {
	root := filepath.Join(string(filepath.Separator)+"home", "user", ".ssh")
	if !isWithinProtectedRoot(filepath.Join(root, "id_rsa"), root) {
		t.Fatal("an exact spelling must be within its protected root")
	}
	mixed := filepath.Join(string(filepath.Separator)+"home", "user", ".SSH", "id_rsa")
	if got := isWithinProtectedRoot(mixed, root); got != caseInsensitivePaths() {
		t.Fatalf("isWithinProtectedRoot(%q) = %t, want %t on %s", mixed, got, caseInsensitivePaths(), runtime.GOOS)
	}
	if isWithinProtectedRoot(filepath.Join(string(filepath.Separator)+"home", "user"), root) {
		t.Fatal("a parent of the protected root is not within it")
	}
	dir := t.TempDir()
	if !hasProtectedAncestor(filepath.Join(dir, "missing", "child"), []string{dir}) {
		t.Fatal("an existing protected ancestor must be found by file identity")
	}
	if hasProtectedAncestor(dir, []string{filepath.Join(dir, "absent")}) {
		t.Fatal("a missing protected root cannot match")
	}
}

func TestFilesystemPolicy_ProtectsMacOSKeychains(t *testing.T) {
	roots := unixProtectedReadRoots()
	for _, want := range []string{"/Library/Keychains", "/var/root/Library/Keychains"} {
		if !slices.Contains(roots, want) {
			t.Errorf("protected roots %v do not include %s", roots, want)
		}
	}
}
