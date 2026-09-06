package filesystem

import (
	"context"
	"encoding/json"
	"errors"
	core "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestFilesystemPolicy_ProtectsSystemReadsAndSymlinkAliases(t *testing.T) {
	primary := t.TempDir()
	systemRoot, systemFile := protectedSystemFixture(t)
	narrowPolicy, err := NewFilesystemPolicy(primary)
	if err != nil {
		t.Fatalf("NewFilesystemPolicy without system allowlist: %v", err)
	}
	msgs, err := NewReadFileToolWithPolicy(narrowPolicy).Execute(context.Background(), map[string]any{"path": systemFile})
	got := requireToolTextContains(t, msgs, err, ErrFilesystemAccessDenied.Error())
	if strings.Contains(got, filepath.Base(systemFile)) {
		t.Fatalf("protected read denial disclosed the requested system path: %q", got)
	}

	policy, err := NewFilesystemPolicy(primary, systemRoot)
	if err != nil {
		t.Fatalf("NewFilesystemPolicy: %v", err)
	}

	readTool := NewReadFileToolWithPolicy(policy)
	listTool := NewListDirToolWithPolicy(policy)
	for _, tc := range []struct {
		name string
		path string
		run  func(string) ([]messages.Message, error)
	}{
		{
			name: "system file",
			path: systemFile,
			run: func(path string) ([]messages.Message, error) {
				return readTool.Execute(context.Background(), map[string]any{"path": path})
			},
		},
		{
			name: "system directory",
			path: systemRoot,
			run: func(path string) ([]messages.Message, error) {
				return listTool.Execute(context.Background(), map[string]any{"path": path})
			},
		},
		{
			name: "system lexical traversal",
			path: filepath.Join(systemRoot, "..", filepath.Base(systemRoot), filepath.Base(systemFile)),
			run: func(path string) ([]messages.Message, error) {
				return readTool.Execute(context.Background(), map[string]any{"path": path})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msgs, err := tc.run(tc.path)
			got := requireToolTextContains(t, msgs, err, ErrFilesystemAccessDenied.Error())
			if strings.Contains(got, "hosts") || strings.Contains(got, "secret") {
				t.Fatalf("protected read diagnostic contains path/content-derived text: %q", got)
			}
			if !errors.Is((&sandboxFs{workspace: primary, additionalWorkspaces: []string{systemRoot}, protectedReadRoots: policy.ProtectedReadRoots()}).authorizeRead(tc.path), ErrProtectedFilesystemRead) {
				t.Fatalf("authorizeRead(%q) did not preserve protected-read identity", tc.path)
			}
		})
	}

	linkPath := filepath.Join(primary, "system-alias")
	if err := os.Symlink(systemRoot, linkPath); err != nil {
		t.Skipf("symlinks unavailable on %s: %v", runtime.GOOS, err)
	}
	aliasPath := filepath.Join(linkPath, filepath.Base(systemFile))
	msgs, err = readTool.Execute(context.Background(), map[string]any{"path": aliasPath})
	got = requireToolTextContains(t, msgs, err, ErrFilesystemAccessDenied.Error())
	if strings.Contains(got, filepath.Base(systemFile)) {
		t.Fatalf("symlink denial disclosed the protected target name: %q", got)
	}
}

func TestFilesystemPolicy_ProtectsCredentialReadsAndDoesNotOverrideOrdinaryReads(t *testing.T) {
	primary := t.TempDir()
	systemRoot := protectedSystemRoot(t)
	home := os.Getenv("HOME")
	if strings.TrimSpace(home) == "" {
		t.Skip("HOME is unavailable")
	}
	credentialRoot := filepath.Join(home, ".ssh")
	policy, err := NewFilesystemPolicy(primary, systemRoot, home)
	if err != nil {
		t.Fatalf("NewFilesystemPolicy: %v", err)
	}
	readTool := NewReadFileToolWithPolicy(policy)
	msgs, err := readTool.Execute(context.Background(), map[string]any{"path": credentialRoot})
	got := requireToolTextContains(t, msgs, err, ErrFilesystemAccessDenied.Error())
	if strings.Contains(got, filepath.Base(credentialRoot)) || strings.Contains(got, "secret") {
		t.Fatalf("credential denial disclosed protected-path or content-derived text: %q", got)
	}

	listTool := NewListDirToolWithPolicy(policy)
	msgs, err = listTool.Execute(context.Background(), map[string]any{"path": credentialRoot})
	got = requireToolTextContains(t, msgs, err, ErrFilesystemAccessDenied.Error())
	if strings.Contains(got, filepath.Base(credentialRoot)) || strings.Contains(got, "secret") {
		t.Fatalf("credential directory denial disclosed protected-path or content-derived text: %q", got)
	}

	imagePreparerCalled := false
	imageTool := NewReadImageToolWithPolicy(policy, func([]string) ([]messages.ImagePart, error) {
		imagePreparerCalled = true
		return []messages.ImagePart{{Bytes: []byte("credential image bytes"), MediaType: imagePNGMediaType}}, nil
	})
	msgs, err = imageTool.Execute(context.Background(), map[string]any{"path": credentialRoot})
	if err != nil {
		t.Fatalf("protected credential read_image returned Go error: %v", err)
	}
	if imagePreparerCalled {
		t.Fatal("protected credential read_image invoked the image preparer")
	}
	var imageResult ReadImageResult
	if len(msgs) != 1 || json.Unmarshal([]byte(msgs[0].TextContent()), &imageResult) != nil {
		t.Fatalf("protected credential read_image result = %#v, want a decodable error envelope", msgs)
	}
	if imageResult.Status != ReadImageResultStatusError || !strings.Contains(imageResult.Error, ErrFilesystemAccessDenied.Error()) {
		t.Fatalf("protected credential read_image result = %#v, want protected-read error", imageResult)
	}
	if imageResult.MIMEType != "" || imageResult.ByteLength != 0 || imageResult.SHA256 != "" || imageResult.TypedProjection != "" || strings.Contains(imageResult.Error, "credential image bytes") {
		t.Fatalf("protected credential read_image leaked image data or metadata: %#v", imageResult)
	}

	ordinaryPath := filepath.Join(primary, "ordinary.txt")
	if err := os.WriteFile(ordinaryPath, []byte("ordinary content"), 0o600); err != nil {
		t.Fatal(err)
	}
	msgs, err = readTool.Execute(context.Background(), map[string]any{"path": ordinaryPath})
	requireToolText(t, msgs, err, "ordinary content")

	msgs, err = listTool.Execute(context.Background(), map[string]any{"path": primary})
	requireToolText(t, msgs, err, "FILE: ordinary.txt\n")
}

func TestReadImageToolWithPolicy_RejectsProtectedPathBeforePreparer(t *testing.T) {
	primary := t.TempDir()
	systemRoot, systemFile := protectedSystemFixture(t)
	policy, err := NewFilesystemPolicy(primary, systemRoot)
	if err != nil {
		t.Fatalf("NewFilesystemPolicy: %v", err)
	}

	called := false
	tool := NewReadImageToolWithPolicy(policy, func([]string) ([]messages.ImagePart, error) {
		called = true
		return []messages.ImagePart{{Bytes: []byte("secret image bytes"), MediaType: imagePNGMediaType}}, nil
	})
	msgs, err := tool.Execute(context.Background(), map[string]any{"path": systemFile})
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	if called {
		t.Fatal("protected read_image path invoked the image preparer")
	}
	if len(msgs) != 1 || msgs[0].Role != messages.RoleTool {
		t.Fatalf("messages = %#v, want one tool error message", msgs)
	}
	var result ReadImageResult
	if err := json.Unmarshal([]byte(msgs[0].TextContent()), &result); err != nil {
		t.Fatalf("decode result envelope: %v", err)
	}
	if result.Status != ReadImageResultStatusError || !strings.Contains(result.Error, ErrFilesystemAccessDenied.Error()) {
		t.Fatalf("result = %#v, want protected-read error", result)
	}
	if result.MIMEType != "" || result.ByteLength != 0 || result.SHA256 != "" || result.TypedProjection != "" || strings.Contains(result.Error, "secret image bytes") {
		t.Fatalf("protected read_image result leaked image data or metadata: %#v", result)
	}
}

func TestReadImageToolWithPolicy_PreservesOrdinaryImageResult(t *testing.T) {
	primary := t.TempDir()
	imagePath := filepath.Join(primary, "ordinary.png")
	imageBytes := minimalPNG()
	if err := os.WriteFile(imagePath, imageBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := NewFilesystemPolicy(primary)
	if err != nil {
		t.Fatalf("NewFilesystemPolicy: %v", err)
	}
	tool := NewReadImageToolWithPolicy(policy, func(paths []string) ([]messages.ImagePart, error) {
		if len(paths) != 1 || paths[0] != imagePath {
			t.Fatalf("preparer paths = %#v, want %q", paths, imagePath)
		}
		return []messages.ImagePart{{Bytes: append([]byte(nil), imageBytes...), MediaType: imagePNGMediaType}}, nil
	})
	msgs, err := tool.Execute(context.Background(), map[string]any{"path": imagePath})
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	if len(msgs) != 1 || msgs[0].TextContent() == "" || !strings.Contains(msgs[0].TextContent(), ReadImageResultStatusSuccess) {
		t.Fatalf("ordinary image result = %#v, want successful envelope", msgs)
	}
	if _, ok := msgs[0].ContentParts[1].(messages.ImagePart); !ok {
		t.Fatalf("ordinary image parts = %#v, want typed image projection", msgs[0].ContentParts)
	}
}

func TestResolveFilesystemPolicyCanonicalizesAndValidatesAdditionalRoots(t *testing.T) {
	parent := t.TempDir()
	workdir := filepath.Join(parent, "workdir")
	additional := filepath.Join(parent, "allowed")
	if err := os.Mkdir(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(additional, 0o755); err != nil {
		t.Fatal(err)
	}

	policy, err := ResolveFilesystemPolicy(workdir, "../allowed", additional)
	if err != nil {
		t.Fatalf("ResolveFilesystemPolicy: %v", err)
	}
	canonicalWorkdir, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		t.Fatalf("canonicalize workdir: %v", err)
	}
	canonicalAdditional, err := filepath.EvalSymlinks(additional)
	if err != nil {
		t.Fatalf("canonicalize additional root: %v", err)
	}
	if policy.PrimaryRoot() != canonicalWorkdir {
		t.Fatalf("primary root = %q, want canonical %q", policy.PrimaryRoot(), canonicalWorkdir)
	}
	if got := policy.AdditionalRoots(); len(got) != 1 || got[0] != canonicalAdditional {
		t.Fatalf("additional roots = %#v, want one canonical %q", got, canonicalAdditional)
	}

	file := filepath.Join(parent, "not-a-directory")
	if err := os.WriteFile(file, []byte("not a root"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		workdir string
		allow   []string
	}{
		{name: "missing workdir", workdir: filepath.Join(parent, "missing")},
		{name: "file workdir", workdir: file},
		{name: "missing additional", workdir: workdir, allow: []string{filepath.Join(parent, "missing-allowed")}},
		{name: "file additional", workdir: workdir, allow: []string{file}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveFilesystemPolicy(tc.workdir, tc.allow...)
			if err == nil || !errors.Is(err, ErrInvalidFilesystemRoot) {
				t.Fatalf("ResolveFilesystemPolicy error = %v, want ErrInvalidFilesystemRoot", err)
			}
		})
	}
}

func TestFilesystemPolicyAppliesToReadsListsAndAllMutationTools(t *testing.T) {
	primary := t.TempDir()
	additional := t.TempDir()
	outside := t.TempDir()
	primaryFile := filepath.Join(primary, "primary.txt")
	additionalFile := filepath.Join(additional, "additional.txt")
	outsideFile := filepath.Join(outside, "outside.txt")
	if err := os.WriteFile(primaryFile, []byte("primary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(additionalFile, []byte("additional"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outsideFile, []byte("SENTINEL-CONTENT"), 0o600); err != nil {
		t.Fatal(err)
	}

	policy, err := NewFilesystemPolicy(primary, additional)
	if err != nil {
		t.Fatalf("NewFilesystemPolicy: %v", err)
	}
	readTool := NewReadFileToolWithPolicy(policy)
	listTool := NewListDirToolWithPolicy(policy)
	writeTool := NewWriteFileToolWithPolicy(policy)
	editTool := NewEditFileToolWithPolicy(policy)
	appendTool := NewAppendFileToolWithPolicy(policy)

	requireToolTextContains(t, mustToolExecute(t, readTool, map[string]any{"path": "primary.txt"}), nil, "primary")
	requireToolTextContains(t, mustToolExecute(t, readTool, map[string]any{"path": additionalFile}), nil, "additional")
	requireToolTextContains(t, mustToolExecute(t, listTool, map[string]any{"path": additional}), nil, "FILE: additional.txt\n")

	additionalWrite := filepath.Join(additional, "new.txt")
	requireToolTextContains(t, mustToolExecute(t, writeTool, map[string]any{"path": additionalWrite, "content": "new"}), nil, "File written")
	requireToolTextContains(t, mustToolExecute(t, editTool, map[string]any{"path": additionalFile, "old_text": "additional", "new_text": "edited"}), nil, "File edited")
	requireToolTextContains(t, mustToolExecute(t, appendTool, map[string]any{"path": additionalFile, "content": "-appended"}), nil, "Appended")
	if got, err := os.ReadFile(additionalFile); err != nil || string(got) != editedAppendedContent {
		t.Fatalf("additional mutation content = %q, %v", got, err)
	}

	deniedParent := filepath.Join(outside, "not-created", "nested")
	deniedTarget := filepath.Join(deniedParent, "denied.txt")
	for _, tc := range []struct {
		name string
		tool core.Tool
		args map[string]any
	}{
		{name: "read", tool: readTool, args: map[string]any{"path": outsideFile}},
		{name: "list", tool: listTool, args: map[string]any{"path": outside}},
		{name: "write", tool: writeTool, args: map[string]any{"path": deniedTarget, "content": "must not write"}},
		{name: "edit", tool: editTool, args: map[string]any{"path": outsideFile, "old_text": "outside", "new_text": "changed"}},
		{name: "append", tool: appendTool, args: map[string]any{"path": outsideFile, "content": "must not append"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := requireToolTextContains(t, mustToolExecute(t, tc.tool, tc.args), nil, "path escapes workspace")
			if strings.Contains(got, "must not") || strings.Contains(got, "SENTINEL-CONTENT") {
				// The denial must not echo mutation content or the protected file's
				// contents; the generic path category is sufficient for this story.
				t.Fatalf("denial text = %q, want no request/content leak", got)
			}
		})
	}
	if got, err := os.ReadFile(outsideFile); err != nil || string(got) != "SENTINEL-CONTENT" {
		t.Fatalf("outside sentinel = %q, %v; want unchanged", got, err)
	}
	if _, err := os.Stat(deniedParent); !os.IsNotExist(err) {
		t.Fatalf("denied parent = %v, want absent", err)
	}
}

func TestFilesystemPolicyResolvesSymlinksBeforeEveryFilesystemOperation(t *testing.T) {
	fixture := newSymlinkFixture(t)
	policy, err := NewFilesystemPolicy(fixture.primary)
	if err != nil {
		t.Fatalf("NewFilesystemPolicy: %v", err)
	}
	readTool := NewReadFileToolWithPolicy(policy)
	listTool := NewListDirToolWithPolicy(policy)
	writeTool := NewWriteFileToolWithPolicy(policy)
	editTool := NewEditFileToolWithPolicy(policy)
	appendTool := NewAppendFileToolWithPolicy(policy)
	assertEscapingSymlinkOperations(t, fixture, policy, readTool, listTool, writeTool, editTool, appendTool)
	assertSafeSymlinkOperations(t, fixture, readTool, listTool, writeTool, editTool, appendTool)
}

type symlinkFixture struct {
	primary, outside, insideDir, insideFile, outsideFile               string
	escapingFile, escapingDir, danglingEscapingDir, danglingOutsideDir string
	insideFileLink, insideDirLink                                      string
}

func newSymlinkFixture(t *testing.T) symlinkFixture {
	t.Helper()
	fixture := symlinkFixture{primary: t.TempDir(), outside: t.TempDir()}
	fixture.insideDir = filepath.Join(fixture.primary, "inside-dir")
	fixture.insideFile = filepath.Join(fixture.primary, "inside.txt")
	fixture.outsideFile = filepath.Join(fixture.outside, "outside.txt")
	if err := os.Mkdir(fixture.insideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSymlinkFixtureFile(t, filepath.Join(fixture.insideDir, "entry.txt"), "inside entry")
	writeSymlinkFixtureFile(t, fixture.insideFile, "inside")
	writeSymlinkFixtureFile(t, fixture.outsideFile, "OUTSIDE-SENTINEL")
	fixture.escapingFile = filepath.Join(fixture.primary, "escaping-file")
	fixture.escapingDir = filepath.Join(fixture.primary, "escaping-dir")
	fixture.danglingEscapingDir = filepath.Join(fixture.primary, "dangling-escaping-dir")
	fixture.danglingOutsideDir = filepath.Join(fixture.outside, "future-outside-dir")
	fixture.insideFileLink = filepath.Join(fixture.primary, "inside-file-link")
	fixture.insideDirLink = filepath.Join(fixture.primary, "inside-dir-link")
	for _, link := range [][2]string{
		{fixture.escapingFile, fixture.outsideFile}, {fixture.escapingDir, fixture.outside},
		{fixture.danglingEscapingDir, fixture.danglingOutsideDir}, {fixture.insideFileLink, fixture.insideFile},
		{fixture.insideDirLink, fixture.insideDir},
	} {
		if err := os.Symlink(link[1], link[0]); err != nil {
			t.Skipf("symlinks unavailable on %s: %v", runtime.GOOS, err)
		}
	}
	return fixture
}

func writeSymlinkFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertEscapingSymlinkOperations(t *testing.T, f symlinkFixture, policy *FilesystemPolicy, readTool, listTool, writeTool, editTool, appendTool core.Tool) {
	t.Helper()
	if err := policy.AuthorizeRead(f.escapingFile); !errors.Is(err, ErrFilesystemAccessDenied) {
		t.Fatalf("escaping file authorization error = %v, want ErrFilesystemAccessDenied", err)
	}
	if err := policy.AuthorizeRead(filepath.Join(f.escapingDir, "new", "created.txt")); !errors.Is(err, ErrFilesystemAccessDenied) {
		t.Fatalf("escaping descendant authorization error = %v, want ErrFilesystemAccessDenied", err)
	}
	denied := []struct {
		name string
		tool core.Tool
		args map[string]any
	}{
		{name: "read escaping file link", tool: readTool, args: map[string]any{"path": f.escapingFile}},
		{name: "list escaping directory link", tool: listTool, args: map[string]any{"path": f.escapingDir}},
		{name: "write below escaping directory link", tool: writeTool, args: map[string]any{"path": filepath.Join(f.escapingDir, "new", "created.txt"), "content": "must not write"}},
		{name: "write below dangling escaping directory link", tool: writeTool, args: map[string]any{"path": filepath.Join(f.danglingEscapingDir, "created.txt"), "content": "must not write"}},
		{name: "edit escaping file link", tool: editTool, args: map[string]any{"path": f.escapingFile, "old_text": "OUTSIDE-SENTINEL", "new_text": "changed"}},
		{name: "append escaping file link", tool: appendTool, args: map[string]any{"path": f.escapingFile, "content": "must not append"}},
	}
	for _, tc := range denied {
		t.Run(tc.name, func(t *testing.T) { assertSymlinkDenied(t, tc) })
	}
	assertSymlinkTargetsUnchanged(t, f)
}

func assertSymlinkDenied(t *testing.T, tc struct {
	name string
	tool core.Tool
	args map[string]any
}) {
	t.Helper()
	msgs, err := tc.tool.Execute(context.Background(), tc.args)
	got := requireToolTextContains(t, msgs, err, "path escapes workspace")
	if strings.Contains(got, "OUTSIDE-SENTINEL") || strings.Contains(got, "must not") {
		t.Fatalf("symlink denial leaked protected content or request data: %q", got)
	}
}

func assertSymlinkTargetsUnchanged(t *testing.T, f symlinkFixture) {
	t.Helper()
	if got, err := os.ReadFile(f.outsideFile); err != nil || string(got) != "OUTSIDE-SENTINEL" {
		t.Fatalf("escaping symlink target = %q, %v; want unchanged", got, err)
	}
	if _, err := os.Stat(filepath.Join(f.outside, "new", "created.txt")); !os.IsNotExist(err) {
		t.Fatalf("escaping symlink write target = %v, want absent", err)
	}
	if _, err := os.Stat(filepath.Join(f.danglingOutsideDir, "created.txt")); !os.IsNotExist(err) {
		t.Fatalf("dangling escaping symlink write target = %v, want absent", err)
	}
}

func assertSafeSymlinkOperations(t *testing.T, f symlinkFixture, readTool, listTool, writeTool, editTool, appendTool core.Tool) {
	t.Helper()
	if got := requireToolTextContains(t, mustToolExecute(t, readTool, map[string]any{"path": f.insideFileLink}), nil, "inside"); got == "" {
		t.Fatal("in-root file symlink returned an empty result")
	}
	requireToolTextContains(t, mustToolExecute(t, listTool, map[string]any{"path": f.insideDirLink}), nil, "FILE: entry.txt\n")
	requireToolTextContains(t, mustToolExecute(t, writeTool, map[string]any{"path": filepath.Join(f.insideDirLink, "created.txt"), "content": "created through safe link"}), nil, "File written")
	requireToolTextContains(t, mustToolExecute(t, editTool, map[string]any{"path": f.insideFileLink, "old_text": "inside", "new_text": "edited through safe link"}), nil, "File edited")
	requireToolTextContains(t, mustToolExecute(t, appendTool, map[string]any{"path": f.insideFileLink, "content": " and appended"}), nil, "Appended")
	if got, err := os.ReadFile(f.insideFile); err != nil || string(got) != "edited through safe link and appended" {
		t.Fatalf("in-root symlink file content = %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(f.insideDir, "created.txt")); err != nil || string(got) != "created through safe link" {
		t.Fatalf("in-root symlink directory content = %q, %v", got, err)
	}
}

func mustToolExecute(t *testing.T, tool core.Tool, args map[string]any) []messages.Message {
	t.Helper()
	msgs, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("tool %q returned Go error: %v", tool.Name(), err)
	}
	return msgs
}

func protectedSystemFixture(t *testing.T) (root, file string) {
	t.Helper()
	if runtime.GOOS == windowsPlatform {
		root = os.Getenv("WINDIR")
		if root == "" {
			t.Skip("WINDIR is not set")
		}
		file = filepath.Join(root, "win.ini")
		if _, err := os.Stat(file); err != nil {
			file = filepath.Join(root, "System32", "drivers", "etc", "hosts")
		}
	} else {
		root = "/etc"
		file = filepath.Join(root, "hosts")
	}
	if _, err := os.Stat(root); err != nil {
		t.Skipf("platform system root %q is unavailable: %v", root, err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Skipf("platform system fixture %q is unavailable: %v", file, err)
	}
	return filepath.Clean(root), filepath.Clean(file)
}

func protectedSystemRoot(t *testing.T) string {
	t.Helper()
	root, _ := protectedSystemFixture(t)
	return root
}
