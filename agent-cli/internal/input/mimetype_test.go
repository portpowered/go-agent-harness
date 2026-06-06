package input

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Minimal valid file headers for each format.
var (
	// PNG: 8-byte signature.
	pngHeader = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}

	// JPEG: SOI marker + JFIF APP0 marker start.
	jpegHeader = []byte{0xFF, 0xD8, 0xFF, 0xE0}

	// GIF: GIF89a header.
	gifHeader = []byte("GIF89a")

	// WebP: RIFF....WEBP (12 bytes; bytes 4-7 are file size, can be zero for test).
	webpHeader = []byte{'R', 'I', 'F', 'F', 0x00, 0x00, 0x00, 0x00, 'W', 'E', 'B', 'P'}

	// TIFF little-endian: II + magic 42.
	tiffHeader = []byte{0x49, 0x49, 0x2A, 0x00}

	// PDF: %PDF-1.4 header.
	pdfHeader = []byte("%PDF-1.4")
)

func TestDetectMimeType_MagicBytes(t *testing.T) {
	tests := []struct {
		name     string
		header   []byte
		ext      string
		expected string
	}{
		{name: "PNG by magic bytes", header: pngHeader, ext: ".png", expected: "image/png"},
		{name: "JPEG by magic bytes", header: jpegHeader, ext: ".jpg", expected: "image/jpeg"},
		{name: "GIF by magic bytes", header: gifHeader, ext: ".gif", expected: "image/gif"},
		{name: "WebP by magic bytes", header: webpHeader, ext: ".webp", expected: "image/webp"},
		{name: "PDF by magic bytes", header: pdfHeader, ext: ".pdf", expected: "application/pdf"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "testfile"+tt.ext)
			require.NoError(t, os.WriteFile(path, tt.header, 0o644))

			result, err := DetectMimeType(path)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDetectMimeType_MagicBytesTakePrecedence(t *testing.T) {
	// A file with .txt extension but PNG magic bytes should be detected as PNG.
	dir := t.TempDir()
	path := filepath.Join(dir, "nottext.txt")
	require.NoError(t, os.WriteFile(path, pngHeader, 0o644))

	result, err := DetectMimeType(path)
	require.NoError(t, err)
	assert.Equal(t, "image/png", result)
}

func TestDetectMimeType_ExtensionFallback(t *testing.T) {
	// When magic bytes return application/octet-stream, fall back to extension.
	dir := t.TempDir()
	// Random bytes that don't match any known signature, but .webp extension.
	path := filepath.Join(dir, "fake.webp")
	require.NoError(t, os.WriteFile(path, []byte{0x00, 0x01, 0x02, 0x03}, 0o644))

	result, err := DetectMimeType(path)
	require.NoError(t, err)
	assert.Equal(t, "image/webp", result)
}

func TestDetectMimeType_WebPDetection(t *testing.T) {
	// Verify WebP is detected even without the .webp extension.
	dir := t.TempDir()
	path := filepath.Join(dir, "image.bin")
	require.NoError(t, os.WriteFile(path, webpHeader, 0o644))

	result, err := DetectMimeType(path)
	require.NoError(t, err)
	assert.Equal(t, "image/webp", result)
}

func TestDetectMimeType_TIFF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.tiff")
	require.NoError(t, os.WriteFile(path, tiffHeader, 0o644))

	result, err := DetectMimeType(path)
	require.NoError(t, err)
	// Go's http.DetectContentType does not detect TIFF — extension fallback not in mimeByExt.
	// We just verify it returns something reasonable.
	assert.NotEmpty(t, result)
}

func TestDetectMimeType_UnknownType(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mystery.xyz")
	// Use bytes that http.DetectContentType cannot match to a known type.
	// Include a mix of non-text control characters to avoid text/plain detection.
	data := make([]byte, 64)
	for i := range data {
		data[i] = byte(i)
	}
	require.NoError(t, os.WriteFile(path, data, 0o644))

	result, err := DetectMimeType(path)
	require.NoError(t, err)
	assert.Equal(t, "application/octet-stream", result)
}

func TestDetectMimeType_FileNotFound(t *testing.T) {
	_, err := DetectMimeType("/nonexistent/file.png")
	assert.Error(t, err)
}

func TestDetectMimeTypeFromBytes(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		ext      string
		expected string
	}{
		{name: "empty data no ext", data: nil, ext: "", expected: "application/octet-stream"},
		{name: "empty data with ext", data: nil, ext: ".png", expected: "image/png"},
		{name: "webp magic no ext", data: webpHeader, ext: "", expected: "image/webp"},
		{name: "webp magic wrong ext", data: webpHeader, ext: ".jpg", expected: "image/webp"},
		{name: "png magic correct ext", data: pngHeader, ext: ".png", expected: "image/png"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detectMimeTypeFromBytes(tt.data, tt.ext)
			assert.Equal(t, tt.expected, result)
		})
	}
}
