package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/portpowered/go-agent-loop/pkg/messages"
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
