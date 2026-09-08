package filesystem

import (
	"context"
	"encoding/binary"
	"errors"
	core "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestFilesystemValidationAndMediaErrorContracts(t *testing.T) {
	workspace := t.TempDir()
	insidePath := filepath.Join(workspace, "inside.txt")
	if err := os.WriteFile(insidePath, []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspace, "listing-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outsidePath, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertValidationPaths(t, workspace, insidePath, outsideDir, outsidePath)
	assertValidationSymlinks(t, workspace, insidePath, outsideDir)
	assertValidationSandbox(t, workspace)
	assertValidationMediaAndReadTools(t, workspace)
}

func assertValidationPaths(t *testing.T, workspace, insidePath, outsideDir, outsidePath string) {
	t.Helper()
	if got, err := validatePath("inside.txt", workspace, true); err != nil || got != insidePath {
		t.Fatalf("valid restricted path = %q, %v; want %q", got, err, insidePath)
	}
	if got, err := validatePath("inside.txt", workspace, false); err != nil || got != insidePath {
		t.Fatalf("valid unrestricted path = %q, %v; want %q", got, err, insidePath)
	}
	if _, err := validatePath("", "", true); err == nil || err.Error() != workspaceUndefinedMessage {
		t.Fatalf("empty workspace error = %v", err)
	}
	relative := filepath.Join("..", filepath.Base(outsideDir), "outside.txt")
	for _, path := range []string{relative, outsidePath} {
		if _, err := validatePath(path, workspace, true); err == nil || !strings.Contains(err.Error(), "outside the workspace") {
			t.Fatalf("escape validation for %q = %v", path, err)
		}
	}
	if got, err := validatePath(filepath.Join("missing", "child.txt"), workspace, true); err != nil || got != filepath.Join(workspace, "missing", "child.txt") {
		t.Fatalf("missing descendant validation = %q, %v", got, err)
	}
	if !isWithinWorkspace(insidePath, workspace) || isWithinWorkspace(outsidePath, workspace) {
		t.Fatal("workspace containment result is incorrect")
	}
	assertSafeRelativePaths(t, workspace, insidePath, outsidePath)
}

func assertSafeRelativePaths(t *testing.T, workspace, insidePath, outsidePath string) {
	t.Helper()
	if got, err := getSafeRelPath(workspace, "inside.txt"); err != nil || got != "inside.txt" {
		t.Fatalf("relative safe path = %q, %v", got, err)
	}
	if got, err := getSafeRelPath(workspace, insidePath); err != nil || got != "inside.txt" {
		t.Fatalf("absolute safe path = %q, %v", got, err)
	}
	if _, err := getSafeRelPath("", "inside.txt"); err == nil || err.Error() != workspaceUndefinedMessage {
		t.Fatalf("empty safe-path workspace error = %v", err)
	}
	if _, err := getSafeRelPath(workspace, outsidePath); err == nil || !strings.Contains(err.Error(), "path escapes workspace") {
		t.Fatalf("absolute safe-path escape error = %v", err)
	}
}

func assertValidationSymlinks(t *testing.T, workspace, insidePath, outsideDir string) {
	t.Helper()
	insideLink := filepath.Join(workspace, "inside-link.txt")
	if err := os.Symlink(insidePath, insideLink); err == nil {
		if _, err := validatePath(insideLink, workspace, true); err != nil {
			t.Fatalf("in-workspace symlink validation error = %v", err)
		}
	} else {
		t.Logf("in-workspace symlink capability unavailable on %s: %v", runtime.GOOS, err)
	}
	externalLink := filepath.Join(workspace, "external-link.txt")
	if err := os.Symlink(outsideDir, externalLink); err == nil {
		assertExternalLinkValidation(t, workspace, externalLink)
	} else {
		t.Logf("external symlink capability unavailable on %s: %v", runtime.GOOS, err)
	}
}

func assertExternalLinkValidation(t *testing.T, workspace, link string) {
	t.Helper()
	if _, err := validatePath(link, workspace, true); err == nil || !strings.Contains(err.Error(), "symlink resolves outside workspace") {
		t.Fatalf("external symlink validation error = %v", err)
	}
	_, err := validatePath(filepath.Join(link, "new.txt"), workspace, true)
	if err == nil || !strings.Contains(err.Error(), "symlink resolves outside workspace") {
		t.Fatalf("external symlink ancestor validation error = %v", err)
	}
}

func assertValidationSandbox(t *testing.T, workspace string) {
	t.Helper()
	if _, err := (&sandboxFs{}).ReadFile("inside.txt"); err == nil || err.Error() != workspaceUndefinedMessage {
		t.Fatalf("empty sandbox error = %v", err)
	}
	missingWorkspace := filepath.Join(t.TempDir(), "missing-workspace")
	if _, err := (&sandboxFs{workspace: missingWorkspace}).ReadFile("inside.txt"); err == nil || !strings.Contains(err.Error(), "failed to open workspace") {
		t.Fatalf("missing sandbox workspace error = %v", err)
	}
}

func assertValidationMediaAndReadTools(t *testing.T, workspace string) {
	t.Helper()
	if got, mediaType, err := imageToNative("picture.bmp", minimalPNG()); err != nil || mediaType != imageJPEGMediaType || len(got) == 0 {
		t.Fatalf("default image conversion = %d bytes, %q, %v", len(got), mediaType, err)
	}
	if _, _, err := imageToNative("picture.png", []byte("not an image")); err == nil || !strings.Contains(err.Error(), "decode image") {
		t.Fatalf("invalid image error = %v", err)
	}
	ctx := context.Background()
	textPath := filepath.Join(workspace, "text.txt")
	if err := os.WriteFile(textPath, []byte("text result"), 0o644); err != nil {
		t.Fatal(err)
	}
	readTool := NewReadFileTool("", false)
	assertReadToolContracts(t, ctx, readTool, textPath, workspace)
	t.Run("audio read when ffmpeg is available", func(t *testing.T) { assertAudioReadContract(t, ctx, readTool, workspace) })
	assertWriteAndListContracts(t, ctx, workspace, textPath)
}

func assertReadToolContracts(t *testing.T, ctx context.Context, readTool core.Tool, textPath, workspace string) {
	t.Helper()
	msgs, err := readTool.Execute(ctx, map[string]any{"path": textPath})
	requireToolText(t, msgs, err, "text result")
	msgs, err = readTool.Execute(ctx, map[string]any{})
	requireToolText(t, msgs, err, "path is required")
	invalidImagePath := filepath.Join(workspace, "invalid.png")
	if err := os.WriteFile(invalidImagePath, []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	msgs, err = readTool.Execute(ctx, map[string]any{"path": invalidImagePath})
	requireToolTextContains(t, msgs, err, "read image: decode image")
}

func assertAudioReadContract(t *testing.T, ctx context.Context, readTool core.Tool, workspace string) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skipf("ffmpeg is required for WAV conversion assertion: %v", err)
	}
	wavPath := filepath.Join(workspace, "tone.wav")
	if err := os.WriteFile(wavPath, minimalWAV(), 0o644); err != nil {
		t.Fatal(err)
	}
	msgs, err := readTool.Execute(ctx, map[string]any{"path": wavPath})
	if err != nil || len(msgs) != 1 || len(msgs[0].ContentParts) != 1 {
		t.Fatalf("audio read result = %#v, %v", msgs, err)
	}
	audio, ok := msgs[0].ContentParts[0].(messages.AudioPart)
	if !ok || audio.MediaType != "audio/pcm" || len(audio.Bytes) == 0 {
		t.Fatalf("audio result = %#v; want non-empty PCM audio", msgs[0].ContentParts[0])
	}
}

func assertWriteAndListContracts(t *testing.T, ctx context.Context, workspace, textPath string) {
	t.Helper()
	writeTool := NewWriteFileTool("", false)
	msgs, err := writeTool.Execute(ctx, map[string]any{})
	requireToolText(t, msgs, err, "path is required")
	msgs, err = writeTool.Execute(ctx, map[string]any{"path": textPath})
	requireToolText(t, msgs, err, "content is required")
	msgs, err = writeTool.Execute(ctx, map[string]any{"path": workspace, "content": "not a file"})
	requireToolTextContains(t, msgs, err, "failed to replace original file")
	assertInvalidHostWrites(t, workspace)
	listTool := NewListDirTool("", false)
	msgs, err = listTool.Execute(ctx, map[string]any{"path": workspace})
	listText := requireToolTextContains(t, msgs, err, "FILE: text.txt")
	if !strings.Contains(listText, "DIR:  ") {
		t.Fatal("directory listing did not identify a directory")
	}
	msgs, err = listTool.Execute(ctx, map[string]any{})
	requireToolTextContains(t, msgs, err, "FILE:")
	msgs, err = listTool.Execute(ctx, map[string]any{"path": filepath.Join(workspace, "missing-dir")})
	requireToolTextContains(t, msgs, err, "failed to read directory:")
}

func assertInvalidHostWrites(t *testing.T, workspace string) {
	t.Helper()
	if err := (&hostFs{}).WriteFile(filepath.Join(workspace, "bad\x00", "file"), []byte("invalid")); err == nil || !strings.Contains(err.Error(), "failed to create parent directories") {
		t.Fatalf("host invalid-parent write error = %v", err)
	}
	if err := (&hostFs{}).WriteFile(filepath.Join(workspace, "bad\x00"), []byte("invalid")); err == nil || !strings.Contains(err.Error(), "failed to write temp file") {
		t.Fatalf("host invalid-file write error = %v", err)
	}
}

func TestWriteFileToolSupportsOSMaximumFilename(t *testing.T) {
	for _, restricted := range []bool{false, true} {
		t.Run(map[bool]string{false: "unrestricted", true: "workspace-restricted"}[restricted], func(t *testing.T) {
			assertMaximumFilenameMode(t, restricted)
		})
	}
}

func assertMaximumFilenameMode(t *testing.T, restricted bool) {
	t.Helper()
	workspace := t.TempDir()
	destinationDir := filepath.Join(workspace, "destination")
	if err := os.Mkdir(destinationDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(destinationDir, discoverMaximumFilenameComponent(t, destinationDir))
	tool := NewWriteFileTool(workspace, restricted)
	assertMaximumFilenameWrite(t, tool, path, destinationDir, "maximum filename content")
	assertMaximumFilenameWrite(t, tool, path, destinationDir, "replacement content")
	assertMaximumFilenameRenameFailure(t, workspace, destinationDir, restricted)
}

func assertMaximumFilenameWrite(t *testing.T, tool core.Tool, path, destinationDir, content string) {
	t.Helper()
	msgs, err := tool.Execute(context.Background(), map[string]any{"path": path, "content": content})
	gotMessage := requireToolTextContains(t, msgs, err, "File written: "+path)
	if gotMessage != "File written: "+path {
		t.Fatalf("write success message = %q, want %q", gotMessage, "File written: "+path)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != content {
		t.Fatalf("maximum-length write persisted %q, %v; want %q", got, err, content)
	}
	assertNoWriteFileTempArtifacts(t, destinationDir)
}

func assertMaximumFilenameRenameFailure(t *testing.T, workspace, destinationDir string, restricted bool) {
	t.Helper()
	var filesystem fileSystem = &hostFs{}
	if restricted {
		filesystem = &sandboxFs{workspace: workspace}
	}
	failureTarget := filepath.Join(destinationDir, "rename-target")
	if err := os.Mkdir(failureTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	err := filesystem.WriteFile(failureTarget, []byte("must not replace directory"))
	if err == nil || (!strings.Contains(err.Error(), "rename") && !strings.Contains(err.Error(), "replace")) {
		t.Fatalf("rename failure = %v, want a rename/replace error", err)
	}
	assertNoWriteFileTempArtifacts(t, destinationDir)
}

func discoverMaximumFilenameComponent(t *testing.T, dir string) string {
	t.Helper()
	accepts := func(length int) bool {
		name := strings.Repeat("n", length)
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("probe"), 0o644); err == nil {
			if err := os.Remove(path); err != nil {
				t.Fatalf("remove filename-length probe %d: %v", length, err)
			}
			return true
		} else if isFilenameComponentTooLong(err) {
			return false
		} else {
			t.Fatalf("probe filename length %d: %v", length, err)
			return false
		}
	}

	low, high := 0, 1
	for accepts(high) {
		low = high
		high *= 2
		if high > 1<<16 {
			t.Skip("filesystem did not expose a filename-component limit during probing")
		}
	}
	for high-low > 1 {
		middle := low + (high-low)/2
		if accepts(middle) {
			low = middle
		} else {
			high = middle
		}
	}
	return strings.Repeat("n", low)
}

func isFilenameComponentTooLong(err error) bool {
	if errors.Is(err, syscall.ENAMETOOLONG) {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "file name too long") ||
		strings.Contains(lower, "filename too long") ||
		strings.Contains(lower, "path too long")
}

func assertNoWriteFileTempArtifacts(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read destination directory: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), writeFileTempPrefix) {
			t.Fatalf("temporary write artifact %q remains in destination directory", entry.Name())
		}
	}
}

func minimalWAV() []byte {
	data := make([]byte, 320)
	wav := make([]byte, 44+len(data))
	copy(wav[0:4], "RIFF")
	binary.LittleEndian.PutUint32(wav[4:8], uint32(len(wav)-8))
	copy(wav[8:12], "WAVE")
	copy(wav[12:16], "fmt ")
	binary.LittleEndian.PutUint32(wav[16:20], 16)
	binary.LittleEndian.PutUint16(wav[20:22], 1)
	binary.LittleEndian.PutUint16(wav[22:24], 1)
	binary.LittleEndian.PutUint32(wav[24:28], 16000)
	binary.LittleEndian.PutUint32(wav[28:32], 32000)
	binary.LittleEndian.PutUint16(wav[32:34], 2)
	binary.LittleEndian.PutUint16(wav[34:36], 16)
	copy(wav[36:40], "data")
	binary.LittleEndian.PutUint32(wav[40:44], uint32(len(data)))
	copy(wav[44:], data)
	return wav
}
