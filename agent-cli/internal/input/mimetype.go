package input

import (
	"net/http"
	"strings"
)

// Image media types the input boundary detects.
const (
	mimeJPEG = "image/jpeg"
	mimePNG  = "image/png"
	mimeGIF  = "image/gif"
	mimeWebP = "image/webp"
)

const (
	// octetStreamMediaType is the generic binary media type.
	octetStreamMediaType = "application/octet-stream"
	// sniffLimitBytes is the most bytes net/http.DetectContentType inspects.
	sniffLimitBytes = 512
)

// detectMimeTypeFromBytes detects the MIME type of data using both magic
// bytes (net/http.DetectContentType) and the file extension ext. Magic bytes
// take precedence, except when they return the generic
// "application/octet-stream" or "text/plain", in which case the
// extension-based type is preferred. WebP (which net/http does not recognise)
// is detected from its RIFF....WEBP header.
func detectMimeTypeFromBytes(data []byte, ext string) string {
	magic := detectByMagicBytes(data)
	extType := mimeTypeForExtension(strings.ToLower(ext))

	switch {
	case magic != "" && magic != octetStreamMediaType && magic != "text/plain":
		// Specific magic-byte detection (e.g. image/jpeg, image/png) wins.
		return magic
	case extType != "":
		// Extension-based type is preferred over generic detections
		// (application/octet-stream or text/plain).
		return extType
	case magic != "":
		return magic
	default:
		return octetStreamMediaType
	}
}

// detectByMagicBytes sniffs the MIME type from the raw bytes.
// It handles WebP specially because Go's http.DetectContentType does not
// recognise it (WebP is RIFF-based: bytes 0-3 = "RIFF", bytes 8-11 = "WEBP").
func detectByMagicBytes(data []byte) string {
	if isWebP(data) {
		return mimeWebP
	}

	if len(data) == 0 {
		return ""
	}

	// http.DetectContentType reads at most sniffLimitBytes bytes.
	sniff := data
	if len(sniff) > sniffLimitBytes {
		sniff = sniff[:sniffLimitBytes]
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
