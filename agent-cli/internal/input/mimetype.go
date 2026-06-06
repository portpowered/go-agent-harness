package input

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// DetectMimeType detects the MIME type of the file at path using both magic
// bytes (first 512 bytes via net/http.DetectContentType) and file extension.
// When both methods produce a result, magic bytes take precedence — except when
// magic bytes return the generic "application/octet-stream", in which case the
// extension-based type is preferred.
//
// Go's http.DetectContentType does not recognise WebP. This function adds a
// special case: if the first 12 bytes contain the RIFF header with a WEBP
// signature at bytes 8–11, "image/webp" is returned directly.
func DetectMimeType(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return detectMimeTypeFromBytes(data, filepath.Ext(path)), nil
}

// detectMimeTypeFromBytes is the pure logic, separated for testability.
func detectMimeTypeFromBytes(data []byte, ext string) string {
	magic := detectByMagicBytes(data)
	extType := mimeByExt[strings.ToLower(ext)]

	switch {
	case magic != "" && magic != "application/octet-stream" && magic != "text/plain":
		// Specific magic-byte detection (e.g. image/jpeg, image/png) wins.
		return magic
	case extType != "":
		// Extension-based type is preferred over generic detections
		// (application/octet-stream or text/plain).
		return extType
	case magic != "":
		return magic
	default:
		return "application/octet-stream"
	}
}

// detectByMagicBytes sniffs the MIME type from the raw bytes.
// It handles WebP specially because Go's http.DetectContentType does not
// recognise it (WebP is RIFF-based: bytes 0-3 = "RIFF", bytes 8-11 = "WEBP").
func detectByMagicBytes(data []byte) string {
	if isWebP(data) {
		return "image/webp"
	}

	if len(data) == 0 {
		return ""
	}

	// http.DetectContentType reads at most 512 bytes.
	sniff := data
	if len(sniff) > 512 {
		sniff = sniff[:512]
	}
	detected := http.DetectContentType(sniff)

	// Strip parameters (e.g. "text/plain; charset=utf-8" → "text/plain").
	if idx := strings.Index(detected, ";"); idx != -1 {
		detected = strings.TrimSpace(detected[:idx])
	}
	return detected
}

// isWebP returns true if data starts with a RIFF header containing a WEBP
// signature: bytes 0-3 = "RIFF", bytes 8-11 = "WEBP".
func isWebP(data []byte) bool {
	return len(data) >= 12 &&
		string(data[0:4]) == "RIFF" &&
		string(data[8:12]) == "WEBP"
}
