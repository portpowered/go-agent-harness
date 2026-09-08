package filesystem

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	core "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal"
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
		assertImagePart(t, msgs, imageJPEGMediaType)
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
		assertImagePart(t, msgs, imagePNGMediaType)
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
	assertImagePart(t, msgs, imagePNGMediaType)
	part, ok := msgs[0].ContentParts[0].(messages.ImagePart)
	if !ok {
		t.Fatalf("first content part = %T, want messages.ImagePart", msgs[0].ContentParts[0])
	}
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
	part, ok := msgs[0].ContentParts[0].(messages.ImagePart)
	if !ok {
		t.Fatalf("first content part = %T, want messages.ImagePart", msgs[0].ContentParts[0])
	}
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
		{"a.jpg", imageJPEGMediaType},
		{"a.JPEG", imageJPEGMediaType},
		{"b.png", imagePNGMediaType},
		{"b.PNG", imagePNGMediaType},
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
	fixture := newRealFilesystemFixture(t)
	readTool := NewReadFileTool(fixture.workspace, true)
	writeTool := NewWriteFileTool(fixture.workspace, true)
	editTool := NewEditFileTool(fixture.workspace, true)
	appendTool := NewAppendFileTool(fixture.workspace, true)
	ctx := context.Background()
	t.Run("restricted read/write/edit/append success", func(t *testing.T) {
		assertRestrictedMutationSuccess(t, ctx, fixture, readTool, writeTool, editTool, appendTool)
	})
	listTool := NewListDirTool(fixture.workspace, true)
	assertRestrictedReadAndValidation(t, ctx, fixture, readTool, listTool)
	assertRestrictedOutsidePaths(t, ctx, fixture, readTool, writeTool, editTool, appendTool)
	t.Run("external symlink", func(t *testing.T) {
		assertRestrictedExternalSymlink(t, ctx, fixture, readTool)
	})
	t.Run("permission denied", func(t *testing.T) {
		assertRestrictedPermissionDenied(t, fixture.workspace)
	})
}

type realFilesystemFixture struct {
	workspace, outsideDir, insidePath, outsidePath string
}

func newRealFilesystemFixture(t *testing.T) realFilesystemFixture {
	t.Helper()
	f := realFilesystemFixture{workspace: t.TempDir(), outsideDir: t.TempDir()}
	f.insidePath = filepath.Join(f.workspace, "inside.txt")
	f.outsidePath = filepath.Join(f.outsideDir, "outside.txt")
	if err := os.WriteFile(f.insidePath, []byte("inside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.outsidePath, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(f.workspace, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

func assertRestrictedMutationSuccess(t *testing.T, ctx context.Context, f realFilesystemFixture, readTool, writeTool, editTool, appendTool core.Tool) {
	t.Helper()
	msgs, err := readTool.Execute(ctx, map[string]any{"path": f.insidePath})
	requireToolText(t, msgs, err, "inside\n")
	writePath := filepath.Join(f.workspace, "written.txt")
	msgs, err = writeTool.Execute(ctx, map[string]any{"path": writePath, "content": "written"})
	requireToolText(t, msgs, err, "File written: "+writePath)
	assertFileContent(t, writePath, "written", "restricted write")
	msgs, err = editTool.Execute(ctx, map[string]any{"path": f.insidePath, "old_text": "inside", "new_text": "edited"})
	requireToolText(t, msgs, err, "File edited: "+f.insidePath)
	assertFileContent(t, f.insidePath, "edited\n", "restricted edit")
	msgs, err = appendTool.Execute(ctx, map[string]any{"path": f.insidePath, "content": "appended"})
	requireToolText(t, msgs, err, "Appended to "+f.insidePath)
	assertFileContent(t, f.insidePath, "edited\nappended", "restricted append")
}

func assertFileContent(t *testing.T, path, want, operation string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("%s persisted %q, %v; want %q", operation, got, err, want)
	}
}

func assertRestrictedReadAndValidation(t *testing.T, ctx context.Context, f realFilesystemFixture, readTool, listTool core.Tool) {
	t.Helper()
	msgs, err := listTool.Execute(ctx, map[string]any{"path": f.workspace})
	requireToolText(t, msgs, err, "FILE: inside.txt\nDIR:  nested\nFILE: written.txt\n")
	missing := filepath.Join(f.workspace, filepath.Base(f.workspace)+"-missing.txt")
	msgs, err = readTool.Execute(ctx, map[string]any{"path": missing})
	requireToolTextContains(t, msgs, err, "failed to read file: file not found")
	if _, err := (&sandboxFs{workspace: f.workspace}).ReadFile(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing-path error identity = %T %v; want errors.Is(fs.ErrNotExist)", err, err)
	}
	assertDirectoryReadError(t, filepath.Join(f.workspace, "nested"))
	if _, err := (&sandboxFs{workspace: f.workspace}).ReadDir(f.workspace); err != nil {
		t.Fatalf("restricted directory listing error = %v", err)
	}
	if _, err := (&sandboxFs{workspace: f.workspace}).ReadDir(missing); err == nil {
		t.Fatal("restricted missing directory unexpectedly succeeded")
	}
	assertInvalidSandboxWrites(t, f.workspace)
}

func assertDirectoryReadError(t *testing.T, path string) {
	t.Helper()
	_, err := (&hostFs{}).ReadFile(path)
	if err == nil || !strings.Contains(err.Error(), "failed to read file:") {
		t.Fatalf("directory-as-file error = %v", err)
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("directory-as-file error identity = %T %v; want errors.As(*os.PathError)", err, err)
	}
}

func assertInvalidSandboxWrites(t *testing.T, workspace string) {
	t.Helper()
	fs := &sandboxFs{workspace: workspace}
	if err := fs.WriteFile("bad\x00/file", []byte("invalid")); err == nil || !strings.Contains(err.Error(), "failed to create parent directories") {
		t.Fatalf("restricted invalid-parent write error = %v", err)
	}
	if err := fs.WriteFile("bad\x00", []byte("invalid")); err == nil || !strings.Contains(err.Error(), "failed to write to temp file") {
		t.Fatalf("restricted invalid-file write error = %v", err)
	}
}

func assertRestrictedOutsidePaths(t *testing.T, ctx context.Context, f realFilesystemFixture, readTool, writeTool, editTool, appendTool core.Tool) {
	t.Helper()
	relativeEscape := filepath.Join("..", filepath.Base(f.outsideDir), "outside.txt")
	for _, path := range []string{relativeEscape, f.outsidePath} {
		t.Run(path, func(t *testing.T) {
			msgs, err := readTool.Execute(ctx, map[string]any{"path": path})
			requireToolText(t, msgs, err, "path escapes workspace: "+path)
		})
	}
	for _, tool := range []core.Tool{editTool, writeTool, appendTool} {
		msgs, err := executeOutsideTool(ctx, tool, f.outsidePath)
		requireToolText(t, msgs, err, "path escapes workspace: "+f.outsidePath)
	}
}

func executeOutsideTool(ctx context.Context, tool core.Tool, path string) ([]messages.Message, error) {
	switch tool.Name() {
	case "edit_file":
		return tool.Execute(ctx, map[string]any{"path": path, "old_text": "outside", "new_text": "changed"})
	case "append_file":
		return tool.Execute(ctx, map[string]any{"path": path, "content": "changed"})
	default:
		return tool.Execute(ctx, map[string]any{"path": path, "content": "changed"})
	}
}

func assertRestrictedExternalSymlink(t *testing.T, ctx context.Context, f realFilesystemFixture, readTool core.Tool) {
	t.Helper()
	linkPath := filepath.Join(f.workspace, "external-link.txt")
	if err := os.Symlink(f.outsidePath, linkPath); err != nil {
		t.Skipf("external symlink capability unavailable on %s: %v", runtime.GOOS, err)
	}
	if _, err := validatePath(linkPath, f.workspace, true); err == nil || !strings.Contains(err.Error(), "symlink resolves outside workspace") {
		t.Fatalf("external symlink validation error = %v", err)
	}
	msgs, err := readTool.Execute(ctx, map[string]any{"path": linkPath})
	got := requireToolTextContains(t, msgs, err, "access denied")
	if !strings.Contains(got, "outside") && !strings.Contains(got, "escapes") {
		t.Fatalf("symlink rejection message = %q, want outside-root evidence", got)
	}
}

func assertRestrictedPermissionDenied(t *testing.T, workspace string) {
	t.Helper()
	if runtime.GOOS == windowsPlatform {
		t.Skip("Windows chmod does not reliably deny reads without ACL changes")
	}
	path := filepath.Join(workspace, "permission.txt")
	if err := os.WriteFile(path, []byte("protected"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Errorf("restore permission file mode: %v", err)
		}
	})
	_, err := (&hostFs{}).ReadFile(path)
	if err == nil {
		t.Skipf("chmod did not produce a permission error on %s", runtime.GOOS)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Skipf("chmod produced a different filesystem error on %s: %v", runtime.GOOS, err)
	}
	if !strings.Contains(err.Error(), "failed to read file: access denied") {
		t.Fatalf("permission error = %q, want access-denied contract", err)
	}
}
