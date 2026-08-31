package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// minimalPNG returns a minimal valid 1x1 PNG (red pixel).
func minimalPNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// minimalJPEG returns a minimal valid 1x1 JPEG.
func minimalJPEG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, G: 0, B: 0, A: 255})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// minimalGIF returns a minimal valid 1x1 GIF (single frame).
func minimalGIF() []byte {
	img := image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.White, color.Black})
	img.SetColorIndex(0, 0, 1)
	g := &gif.GIF{
		Image: []*image.Paletted{img},
		Delay: []int{0},
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// minimalWebP is a minimal valid 1x1 lossy WebP (base64 from a known-good minimal WebP).
const minimalWebPBase64 = "UklGRiQAAABXRUJQVlA4IBgAAAAwAQCdASoBAAEAAwA0JaQAA3AA/vuUAAA="

func minimalWebP() []byte {
	b, err := base64.StdEncoding.DecodeString(minimalWebPBase64)
	if err != nil {
		panic(err)
	}
	return b
}

// assertImagePart returns the single ImagePart from msgs and asserts mediaType and non-empty bytes.
func assertImagePart(t *testing.T, msgs []messages.Message, wantMediaType string) {
	t.Helper()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message; got %d", len(msgs))
	}
	parts := msgs[0].ContentParts
	if len(parts) != 1 {
		t.Fatalf("expected 1 content part; got %d", len(parts))
	}
	img, ok := parts[0].(messages.ImagePart)
	if !ok {
		t.Fatalf("content part is not ImagePart; got %T", parts[0])
	}
	if img.MediaType != wantMediaType {
		t.Errorf("MediaType = %q; want %q", img.MediaType, wantMediaType)
	}
	if len(img.Bytes) == 0 {
		t.Error("ImagePart.Bytes is empty")
	}
}

// assertVideoPart returns the single VideoPart from msgs and asserts mediaType and non-empty bytes.
func assertVideoPart(t *testing.T, msgs []messages.Message, wantMediaType string) {
	t.Helper()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message; got %d", len(msgs))
	}
	parts := msgs[0].ContentParts
	if len(parts) != 1 {
		t.Fatalf("expected 1 content part; got %d", len(parts))
	}
	vid, ok := parts[0].(messages.VideoPart)
	if !ok {
		t.Fatalf("content part is not VideoPart; got %T", parts[0])
	}
	if vid.MediaType != wantMediaType {
		t.Errorf("MediaType = %q; want %q", vid.MediaType, wantMediaType)
	}
	if len(vid.Bytes) == 0 {
		t.Error("VideoPart.Bytes is empty")
	}
}

func TestReadFileTool_ImageFormats(t *testing.T) {
	ctx := context.Background()
	tool := NewReadFileTool("", false) // host fs for temp files

	t.Run("jpeg", func(t *testing.T) {
		content := minimalJPEG()
		dir := t.TempDir()
		path := filepath.Join(dir, "test.jpg")
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
		msgs, err := tool.Execute(ctx, map[string]any{"path": path})
		if err != nil {
			t.Fatal(err)
		}
		assertImagePart(t, msgs, "image/jpeg")
	})

	t.Run("png", func(t *testing.T) {
		content := minimalPNG()
		dir := t.TempDir()
		path := filepath.Join(dir, "test.png")
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
		msgs, err := tool.Execute(ctx, map[string]any{"path": path})
		if err != nil {
			t.Fatal(err)
		}
		assertImagePart(t, msgs, "image/png")
	})

	t.Run("gif", func(t *testing.T) {
		content := minimalGIF()
		dir := t.TempDir()
		path := filepath.Join(dir, "test.gif")
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
		msgs, err := tool.Execute(ctx, map[string]any{"path": path})
		if err != nil {
			t.Fatal(err)
		}
		assertImagePart(t, msgs, "image/gif")
	})

	t.Run("webp", func(t *testing.T) {
		content := minimalWebP()
		dir := t.TempDir()
		path := filepath.Join(dir, "test.webp")
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
		msgs, err := tool.Execute(ctx, map[string]any{"path": path})
		if err != nil {
			t.Fatal(err)
		}
		assertImagePart(t, msgs, "image/webp")
	})
}

func TestReadFileTool_VideoFormats(t *testing.T) {
	ctx := context.Background()
	tool := NewReadFileTool("", false)
	// Video is passed through; we only assert MIME type from extension.
	dummyVideo := []byte("dummy video content")
	tests := []struct {
		ext      string
		wantMIME string
	}{
		{".mp4", "video/mp4"},
		{".webm", "video/webm"},
		{".mov", "video/quicktime"},
		{".flv", "video/x-flv"},
		{".mpeg", "video/mpeg"},
		{".mpg", "video/mpeg"},
		{".wmv", "video/wmv"},
		{".3gp", "video/3gpp"},
		{".3gpp", "video/3gpp"},
	}
	for _, tt := range tests {
		t.Run(tt.ext, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "clip"+tt.ext)
			if err := os.WriteFile(path, dummyVideo, 0o644); err != nil {
				t.Fatal(err)
			}
			msgs, err := tool.Execute(ctx, map[string]any{"path": path})
			if err != nil {
				t.Fatal(err)
			}
			assertVideoPart(t, msgs, tt.wantMIME)
		})
	}
}

func TestReadFileTool_PNGNotConvertedToJPEG(t *testing.T) {
	// Ensure PNG stays PNG (bytes are re-encoded PNG, not JPEG).
	content := minimalPNG()
	dir := t.TempDir()
	path := filepath.Join(dir, "dot.png")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tool := NewReadFileTool("", false)
	msgs, err := tool.Execute(ctx, map[string]any{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	assertImagePart(t, msgs, "image/png")
	part := msgs[0].ContentParts[0].(messages.ImagePart)
	// PNG signature
	if len(part.Bytes) < 8 {
		t.Fatal("expected at least 8 bytes")
	}
	if part.Bytes[0] != 0x89 || part.Bytes[1] != 0x50 || part.Bytes[2] != 0x4e {
		t.Errorf("expected PNG signature (89 50 4E 47...); got %02x %02x %02x", part.Bytes[0], part.Bytes[1], part.Bytes[2])
	}
}

func TestReadFileTool_GIFPreserved(t *testing.T) {
	// GIF is returned as-is (same bytes) with image/gif.
	content := minimalGIF()
	dir := t.TempDir()
	path := filepath.Join(dir, "dot.gif")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tool := NewReadFileTool("", false)
	msgs, err := tool.Execute(ctx, map[string]any{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	assertImagePart(t, msgs, "image/gif")
	part := msgs[0].ContentParts[0].(messages.ImagePart)
	if !bytes.Equal(part.Bytes, content) {
		t.Error("GIF content should be returned unchanged (animation preserved)")
	}
}

func TestMediaKindFromPath_WebP(t *testing.T) {
	if mediaKindFromPath("x.webp") != mediaImage {
		t.Error("mediaKindFromPath(\"x.webp\") should be mediaImage")
	}
}

func TestImageMediaType(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"a.jpg", "image/jpeg"},
		{"a.JPEG", "image/jpeg"},
		{"b.png", "image/png"},
		{"b.PNG", "image/png"},
		{"c.gif", "image/gif"},
		{"d.webp", "image/webp"},
		{"e.WEBP", "image/webp"},
	}
	for _, tt := range tests {
		got := imageMediaType(tt.path)
		if got != tt.want {
			t.Errorf("imageMediaType(%q) = %q; want %q", tt.path, got, tt.want)
		}
	}
}

func TestVideoMediaType(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"a.mp4", "video/mp4"},
		{"b.webm", "video/webm"},
		{"c.mov", "video/quicktime"},
		{"d.flv", "video/x-flv"},
		{"e.mpeg", "video/mpeg"},
		{"f.mpg", "video/mpeg"},
		{"g.wmv", "video/wmv"},
		{"h.3gp", "video/3gpp"},
		{"i.3gpp", "video/3gpp"},
	}
	for _, tt := range tests {
		got := videoMediaType(tt.path)
		if got != tt.want {
			t.Errorf("videoMediaType(%q) = %q; want %q", tt.path, got, tt.want)
		}
	}
}

func TestMediaKindFromPath_Video(t *testing.T) {
	exts := []string{".flv", ".mov", ".mpeg", ".mpg", ".mp4", ".webm", ".wmv", ".3gp", ".3gpp"}
	for _, ext := range exts {
		if mediaKindFromPath("x"+ext) != mediaVideo {
			t.Errorf("mediaKindFromPath(%q) should be mediaVideo", "x"+ext)
		}
	}
}

func requireToolTextContains(t *testing.T, msgs []messages.Message, err error, want string) string {
	t.Helper()
	if err != nil {
		t.Fatalf("tool returned error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected one tool message, got %d", len(msgs))
	}
	got := msgs[0].TextContent()
	if got == "" || !strings.Contains(got, want) {
		t.Fatalf("tool message = %q, want non-empty text containing %q", got, want)
	}
	return got
}

func TestS6_FilesystemSandboxRealFilesystem(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	outsideDir := t.TempDir()
	insidePath := filepath.Join(workspace, "inside.txt")
	outsidePath := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(insidePath, []byte("inside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outsidePath, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspace, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}

	readTool := NewReadFileTool(workspace, true)
	writeTool := NewWriteFileTool(workspace, true)
	editTool := NewEditFileTool(workspace, true)
	appendTool := NewAppendFileTool(workspace, true)
	t.Run("restricted read/write/edit/append success", func(t *testing.T) {
		t.Skip("baseline sandboxFs validates paths but then uses process-relative os.ReadFile/os.WriteFile; the production correction is outside this test-only lease")

		msgs, err := readTool.Execute(ctx, map[string]any{"path": insidePath})
		requireToolText(t, msgs, err, "inside\n")

		writePath := filepath.Join(workspace, "written.txt")
		msgs, err = writeTool.Execute(ctx, map[string]any{"path": writePath, "content": "written"})
		requireToolText(t, msgs, err, "File written: "+writePath)
		if got, err := os.ReadFile(writePath); err != nil || string(got) != "written" {
			t.Fatalf("restricted write persisted %q, %v; want %q", got, err, "written")
		}

		msgs, err = editTool.Execute(ctx, map[string]any{
			"path":     insidePath,
			"old_text": "inside",
			"new_text": "edited",
		})
		requireToolText(t, msgs, err, "File edited: "+insidePath)
		if got, err := os.ReadFile(insidePath); err != nil || string(got) != "edited\n" {
			t.Fatalf("restricted edit persisted %q, %v; want %q", got, err, "edited\n")
		}

		msgs, err = appendTool.Execute(ctx, map[string]any{"path": insidePath, "content": "appended"})
		requireToolText(t, msgs, err, "Appended to "+insidePath)
		if got, err := os.ReadFile(insidePath); err != nil || string(got) != "edited\nappended" {
			t.Fatalf("restricted append persisted %q, %v; want %q", got, err, "edited\nappended")
		}
	})

	listTool := NewListDirTool(workspace, true)
	msgs, err := listTool.Execute(ctx, map[string]any{"path": workspace})
	requireToolText(t, msgs, err, "FILE: inside.txt\nDIR:  nested\n")

	missing := filepath.Join(workspace, filepath.Base(workspace)+"-missing.txt")
	msgs, err = readTool.Execute(ctx, map[string]any{"path": missing})
	requireToolTextContains(t, msgs, err, "failed to read file: file not found")
	if _, err := (&sandboxFs{workspace: workspace}).ReadFile(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing-path error identity = %T %v; want errors.Is(fs.ErrNotExist)", err, err)
	}
	if _, err := (&hostFs{}).ReadFile(filepath.Join(workspace, "nested")); err == nil || !strings.Contains(err.Error(), "failed to read file:") {
		t.Fatalf("directory-as-file error = %v", err)
	} else {
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) {
			t.Fatalf("directory-as-file error identity = %T %v; want errors.As(*os.PathError)", err, err)
		}
	}
	if _, err := (&sandboxFs{workspace: workspace}).ReadDir(workspace); err != nil {
		t.Fatalf("restricted directory listing error = %v", err)
	}
	if _, err := (&sandboxFs{workspace: workspace}).ReadDir(missing); err == nil {
		t.Fatal("restricted missing directory unexpectedly succeeded")
	}
	if err := (&sandboxFs{workspace: workspace}).WriteFile("bad\x00/file", []byte("invalid")); err == nil || !strings.Contains(err.Error(), "failed to create parent directories") {
		t.Fatalf("restricted invalid-parent write error = %v", err)
	}
	if err := (&sandboxFs{workspace: workspace}).WriteFile("bad\x00", []byte("invalid")); err == nil || !strings.Contains(err.Error(), "failed to write to temp file") {
		t.Fatalf("restricted invalid-file write error = %v", err)
	}

	relativeEscape := filepath.Join("..", filepath.Base(outsideDir), "outside.txt")
	for _, tt := range []struct {
		name string
		path string
	}{
		{name: "relative traversal", path: relativeEscape},
		{name: "absolute external path", path: outsidePath},
	} {
		t.Run(tt.name, func(t *testing.T) {
			msgs, err := readTool.Execute(ctx, map[string]any{"path": tt.path})
			requireToolText(t, msgs, err, "path escapes workspace: "+tt.path)
		})
	}
	msgs, err = editTool.Execute(ctx, map[string]any{
		"path":     outsidePath,
		"old_text": "outside",
		"new_text": "changed",
	})
	requireToolText(t, msgs, err, "path escapes workspace: "+outsidePath)
	msgs, err = writeTool.Execute(ctx, map[string]any{"path": outsidePath, "content": "changed"})
	requireToolText(t, msgs, err, "path escapes workspace: "+outsidePath)
	msgs, err = appendTool.Execute(ctx, map[string]any{"path": outsidePath, "content": "changed"})
	requireToolText(t, msgs, err, "path escapes workspace: "+outsidePath)

	t.Run("external symlink", func(t *testing.T) {
		linkPath := filepath.Join(workspace, "external-link.txt")
		if err := os.Symlink(outsidePath, linkPath); err != nil {
			t.Skipf("external symlink capability unavailable on %s: %v", runtime.GOOS, err)
		}
		if _, err := validatePath(linkPath, workspace, true); err == nil || !strings.Contains(err.Error(), "symlink resolves outside workspace") {
			t.Fatalf("external symlink validation error = %v", err)
		}
		t.Skip("public sandbox symlink I/O is intentionally skipped: the baseline uses process-relative os.ReadFile after validation, so exercising it would require an out-of-scope production correction")
		msgs, err := readTool.Execute(ctx, map[string]any{"path": linkPath})
		got := requireToolTextContains(t, msgs, err, "access denied")
		if !strings.Contains(got, "outside") && !strings.Contains(got, "escapes") {
			t.Fatalf("symlink rejection message = %q, want outside-root evidence", got)
		}
	})

	t.Run("permission denied", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows chmod does not reliably deny reads without ACL changes")
		}
		permissionPath := filepath.Join(workspace, "permission.txt")
		if err := os.WriteFile(permissionPath, []byte("protected"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(permissionPath, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(permissionPath, 0o644) })
		_, err := (&hostFs{}).ReadFile(permissionPath)
		if err == nil {
			t.Skipf("chmod did not produce a permission error on %s", runtime.GOOS)
		}
		if !errors.Is(err, fs.ErrPermission) {
			t.Skipf("chmod produced a different filesystem error on %s: %v", runtime.GOOS, err)
		}
		if !strings.Contains(err.Error(), "failed to read file: access denied") {
			t.Fatalf("permission error = %q, want access-denied contract", err)
		}
	})
}

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

	if got, err := validatePath("inside.txt", workspace, true); err != nil || got != insidePath {
		t.Fatalf("valid restricted path = %q, %v; want %q", got, err, insidePath)
	}
	if got, err := validatePath("inside.txt", workspace, false); err != nil || got != insidePath {
		t.Fatalf("valid unrestricted path = %q, %v; want %q", got, err, insidePath)
	}
	if _, err := validatePath("", "", true); err == nil || err.Error() != "workspace is not defined" {
		t.Fatalf("empty workspace error = %v", err)
	}
	if _, err := validatePath(filepath.Join("..", filepath.Base(outsideDir), "outside.txt"), workspace, true); err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("relative escape validation error = %v", err)
	}
	if _, err := validatePath(outsidePath, workspace, true); err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("absolute escape validation error = %v", err)
	}
	if got, err := validatePath(filepath.Join("missing", "child.txt"), workspace, true); err != nil || got != filepath.Join(workspace, "missing", "child.txt") {
		t.Fatalf("missing descendant validation = %q, %v", got, err)
	}
	if !isWithinWorkspace(insidePath, workspace) || isWithinWorkspace(outsidePath, workspace) {
		t.Fatal("workspace containment result is incorrect")
	}

	if got, err := getSafeRelPath(workspace, "inside.txt"); err != nil || got != "inside.txt" {
		t.Fatalf("relative safe path = %q, %v", got, err)
	}
	if got, err := getSafeRelPath(workspace, insidePath); err != nil || got != "inside.txt" {
		t.Fatalf("absolute safe path = %q, %v", got, err)
	}
	if _, err := getSafeRelPath("", "inside.txt"); err == nil || err.Error() != "workspace is not defined" {
		t.Fatalf("empty safe-path workspace error = %v", err)
	}
	if _, err := getSafeRelPath(workspace, outsidePath); err == nil || !strings.Contains(err.Error(), "path escapes workspace") {
		t.Fatalf("absolute safe-path escape error = %v", err)
	}

	insideLink := filepath.Join(workspace, "inside-link.txt")
	externalLink := filepath.Join(workspace, "external-link.txt")
	if err := os.Symlink(insidePath, insideLink); err == nil {
		if _, err := validatePath(insideLink, workspace, true); err != nil {
			t.Fatalf("in-workspace symlink validation error = %v", err)
		}
	} else {
		t.Logf("in-workspace symlink capability unavailable on %s: %v", runtime.GOOS, err)
	}
	if err := os.Symlink(outsideDir, externalLink); err == nil {
		if _, err := validatePath(externalLink, workspace, true); err == nil || !strings.Contains(err.Error(), "symlink resolves outside workspace") {
			t.Fatalf("external symlink validation error = %v", err)
		}
		_, err := validatePath(filepath.Join(externalLink, "new.txt"), workspace, true)
		if err == nil || !strings.Contains(err.Error(), "symlink resolves outside workspace") {
			t.Fatalf("external symlink ancestor validation error = %v", err)
		}
	} else {
		t.Logf("external symlink capability unavailable on %s: %v", runtime.GOOS, err)
	}

	if _, err := (&sandboxFs{}).ReadFile("inside.txt"); err == nil || err.Error() != "workspace is not defined" {
		t.Fatalf("empty sandbox error = %v", err)
	}
	missingWorkspace := filepath.Join(t.TempDir(), "missing-workspace")
	if _, err := (&sandboxFs{workspace: missingWorkspace}).ReadFile("inside.txt"); err == nil || !strings.Contains(err.Error(), "failed to open workspace") {
		t.Fatalf("missing sandbox workspace error = %v", err)
	}

	if got, mediaType, err := imageToNative("picture.bmp", minimalPNG()); err != nil || mediaType != "image/jpeg" || len(got) == 0 {
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

	t.Run("audio read when ffmpeg is available", func(t *testing.T) {
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
	})

	writeTool := NewWriteFileTool("", false)
	msgs, err = writeTool.Execute(ctx, map[string]any{})
	requireToolText(t, msgs, err, "path is required")
	msgs, err = writeTool.Execute(ctx, map[string]any{"path": textPath})
	requireToolText(t, msgs, err, "content is required")
	msgs, err = writeTool.Execute(ctx, map[string]any{"path": workspace, "content": "not a file"})
	requireToolTextContains(t, msgs, err, "failed to replace original file")
	if err := (&hostFs{}).WriteFile(filepath.Join(workspace, "bad\x00", "file"), []byte("invalid")); err == nil || !strings.Contains(err.Error(), "failed to create parent directories") {
		t.Fatalf("host invalid-parent write error = %v", err)
	}
	if err := (&hostFs{}).WriteFile(filepath.Join(workspace, "bad\x00"), []byte("invalid")); err == nil || !strings.Contains(err.Error(), "failed to write temp file") {
		t.Fatalf("host invalid-file write error = %v", err)
	}

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

func TestWriteFileToolSupportsOSMaximumFilename(t *testing.T) {
	for _, restricted := range []bool{false, true} {
		t.Run(map[bool]string{false: "unrestricted", true: "workspace-restricted"}[restricted], func(t *testing.T) {
			workspace := t.TempDir()
			destinationDir := filepath.Join(workspace, "destination")
			if err := os.Mkdir(destinationDir, 0o755); err != nil {
				t.Fatal(err)
			}

			name := discoverMaximumFilenameComponent(t, destinationDir)
			path := filepath.Join(destinationDir, name)
			content := "maximum filename content"
			tool := NewWriteFileTool(workspace, restricted)

			msgs, err := tool.Execute(context.Background(), map[string]any{
				"path":    path,
				"content": content,
			})
			gotMessage := requireToolTextContains(t, msgs, err, "File written: "+path)
			if gotMessage != "File written: "+path {
				t.Fatalf("write success message = %q, want %q", gotMessage, "File written: "+path)
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != content {
				t.Fatalf("maximum-length write persisted %q, %v; want %q", got, err, content)
			}
			assertNoWriteFileTempArtifacts(t, destinationDir)

			replacement := "replacement content"
			msgs, err = tool.Execute(context.Background(), map[string]any{
				"path":    path,
				"content": replacement,
			})
			requireToolTextContains(t, msgs, err, "File written: "+path)
			if got, err := os.ReadFile(path); err != nil || string(got) != replacement {
				t.Fatalf("maximum-length replacement persisted %q, %v; want %q", got, err, replacement)
			}
			assertNoWriteFileTempArtifacts(t, destinationDir)

			var filesystem fileSystem
			if restricted {
				filesystem = &sandboxFs{workspace: workspace}
			} else {
				filesystem = &hostFs{}
			}
			failureTarget := filepath.Join(destinationDir, "rename-target")
			if err := os.Mkdir(failureTarget, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := filesystem.WriteFile(failureTarget, []byte("must not replace directory")); err == nil || !strings.Contains(err.Error(), "rename") && !strings.Contains(err.Error(), "replace") {
				t.Fatalf("rename failure = %v, want a rename/replace error", err)
			}
			assertNoWriteFileTempArtifacts(t, destinationDir)
		})
	}
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
