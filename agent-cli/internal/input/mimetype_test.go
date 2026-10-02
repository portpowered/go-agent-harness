package input

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Minimal valid file headers for each format.
const (
	// PNG: 8-byte signature.
	pngHeader = "\x89PNG\r\n\x1a\n"

	// JPEG: SOI marker + JFIF APP0 marker start.
	jpegHeader = "\xff\xd8\xff\xe0"

	// GIF: GIF89a header.
	gifHeader = "GIF89a"

	// TIFF little-endian: II + magic 42.
	tiffHeader = "II\x2a\x00"

	// PDF: %PDF-1.4 header.
	pdfHeader = "%PDF-1.4"
)

// webpHeader is a WebP header: RIFF....WEBP (12 bytes; bytes 4-7 are the file
// size, which can be zero for the test).
func webpHeader() []byte {
	return []byte{'R', 'I', 'F', 'F', 0x00, 0x00, 0x00, 0x00, 'W', 'E', 'B', 'P'}
}

func TestDetectMimeTypeFromBytes_MagicBytes(t *testing.T) {
	tests := []struct {
		name     string
		header   []byte
		ext      string
		expected string
	}{
		{name: "PNG by magic bytes", header: []byte(pngHeader), ext: ".png", expected: "image/png"},
		{name: "JPEG by magic bytes", header: []byte(jpegHeader), ext: ".jpg", expected: "image/jpeg"},
		{name: "GIF by magic bytes", header: []byte(gifHeader), ext: ".gif", expected: "image/gif"},
		{name: "WebP by magic bytes", header: webpHeader(), ext: ".webp", expected: "image/webp"},
		{name: "PDF by magic bytes", header: []byte(pdfHeader), ext: ".pdf", expected: "application/pdf"},
		// A .txt extension with PNG magic bytes is detected as PNG.
		{name: "magic bytes take precedence", header: []byte(pngHeader), ext: ".txt", expected: "image/png"},
		// Unrecognized bytes fall back to the extension.
		{name: "extension fallback", header: []byte{0x00, 0x01, 0x02, 0x03}, ext: ".webp", expected: "image/webp"},
		{name: "WebP without extension", header: webpHeader(), ext: ".bin", expected: "image/webp"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, detectMimeTypeFromBytes(tt.header, tt.ext))
		})
	}
}

func TestDetectMimeTypeFromBytes_TIFF(t *testing.T) {
	// Go's http.DetectContentType does not detect TIFF; only check the result is reasonable.
	assert.NotEmpty(t, detectMimeTypeFromBytes([]byte(tiffHeader), ".tiff"))
}

func TestDetectMimeTypeFromBytes_UnknownType(t *testing.T) {
	// Non-text control characters that match no known signature or extension.
	data := make([]byte, 64)
	for i := range data {
		data[i] = byte(i)
	}
	assert.Equal(t, "application/octet-stream", detectMimeTypeFromBytes(data, ".xyz"))
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
		{name: "webp magic no ext", data: webpHeader(), ext: "", expected: "image/webp"},
		{name: "webp magic wrong ext", data: webpHeader(), ext: ".jpg", expected: "image/webp"},
		{name: "png magic correct ext", data: []byte(pngHeader), ext: ".png", expected: "image/png"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detectMimeTypeFromBytes(tt.data, tt.ext)
			assert.Equal(t, tt.expected, result)
		})
	}
}
