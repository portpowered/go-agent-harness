package input

import (
	"io"
	"net/http"
	"strings"

	"github.com/portpowered/go-agent-loop/pkg/messages"
)

// StdinContent holds the result of reading stdin.
// Exactly one of Text or Part is populated (never both).
// Both are zero-valued when stdin was empty or whitespace-only text.
type StdinContent struct {
	// Text is non-empty when stdin contained text/plain data.
	Text string
	// Part is non-nil when stdin contained binary media data.
	Part messages.ContentPart
}

// ReadStdinContent reads all available bytes from r and classifies them by MIME type.
// The full stream is consumed before classification, so chunked/piped data is handled correctly.
//
// Text (text/plain) is returned in StdinContent.Text, trimmed of leading/trailing whitespace.
// Whitespace-only text produces an empty StdinContent (both fields zero-valued).
//
// Binary data is classified and returned as a ContentPart in StdinContent.Part:
//   - image/* → messages.ImagePart
//   - audio/* → messages.AudioPart
//   - video/* → messages.VideoPart
//   - other   → messages.FilePart (Name: "stdin")
//
// When the content type cannot be determined (e.g. application/octet-stream),
// a FilePart is returned so callers always receive structured, typed input.
func ReadStdinContent(r io.Reader) (StdinContent, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return StdinContent{}, err
	}
	if len(data) == 0 {
		return StdinContent{}, nil
	}

	mediaType := http.DetectContentType(data)
	// Strip parameters (e.g. "text/plain; charset=utf-8" → "text/plain")
	if idx := strings.Index(mediaType, ";"); idx != -1 {
		mediaType = strings.TrimSpace(mediaType[:idx])
	}

	switch {
	case strings.HasPrefix(mediaType, "text/"):
		text := strings.TrimSpace(string(data))
		return StdinContent{Text: text}, nil
	case strings.HasPrefix(mediaType, "image/"):
		return StdinContent{Part: messages.ImagePart{Bytes: data, MediaType: mediaType}}, nil
	case strings.HasPrefix(mediaType, "audio/"):
		return StdinContent{Part: messages.AudioPart{Bytes: data, MediaType: mediaType}}, nil
	case strings.HasPrefix(mediaType, "video/"):
		return StdinContent{Part: messages.VideoPart{Bytes: data, MediaType: mediaType}}, nil
	default:
		// Includes application/octet-stream — unknown binary becomes file data.
		return StdinContent{Part: messages.FilePart{Bytes: data, Name: "stdin", MediaType: mediaType}}, nil
	}
}
