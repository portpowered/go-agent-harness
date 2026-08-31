package input

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// MIME types by file extension (lowercase). Used when loading files for ask input.
var mimeByExt = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
	".mp3":  "audio/mpeg",
	".wav":  "audio/wav",
	".ogg":  "audio/ogg",
	".m4a":  "audio/mp4",
	".flac": "audio/flac",
	".mp4":  "video/mp4",
	".webm": "video/webm",
	".mov":  "video/quicktime",
	".mkv":  "video/x-matroska",
}

const (
	AttachmentReasonMissing     = "missing file"
	AttachmentReasonUnreadable  = "unreadable file"
	AttachmentReasonNotRegular  = "not a regular file"
	AttachmentReasonUnsupported = "unsupported or invalid content"
)

// AttachmentError identifies a local ask attachment that could not be used.
// Path is the original command-line spelling, while Cause retains the
// underlying filesystem error for callers that need errors.Is/As.
type AttachmentError struct {
	Path   string
	Reason string
	Cause  error
}

func (e *AttachmentError) Error() string {
	return fmt.Sprintf("attachment %q: %s", e.Path, e.Reason)
}

func (e *AttachmentError) Unwrap() error { return e.Cause }

// LoadContentPart reads the file at path and returns a ContentPart (ImagePart, AudioPart, VideoPart, or FilePart)
// based on the detected MIME type. The filename (base) is preserved for FilePart name.
func LoadContentPart(path string) (messages.ContentPart, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return contentPartFromBytes(path, data), nil
}

// LoadAskContentPart validates and loads one positional ask attachment. It
// performs the regular-file check before reading, which prevents special files
// such as FIFOs from blocking the command during local preflight.
func LoadAskContentPart(path string) (messages.ContentPart, error) {
	info, err := os.Stat(path)
	if err != nil {
		reason := AttachmentReasonUnreadable
		if os.IsNotExist(err) {
			reason = AttachmentReasonMissing
		}
		return nil, &AttachmentError{Path: path, Reason: reason, Cause: err}
	}
	if !info.Mode().IsRegular() {
		return nil, &AttachmentError{Path: path, Reason: AttachmentReasonNotRegular}
	}
	if info.Mode().Perm()&0444 == 0 {
		return nil, &AttachmentError{Path: path, Reason: AttachmentReasonUnreadable}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		reason := AttachmentReasonUnreadable
		if os.IsNotExist(err) {
			reason = AttachmentReasonMissing
		}
		return nil, &AttachmentError{Path: path, Reason: reason, Cause: err}
	}
	part := contentPartFromBytes(path, data)
	if mediaType := contentPartMediaType(part); mediaType == "" || mediaType == "application/octet-stream" {
		return nil, &AttachmentError{
			Path:   path,
			Reason: fmt.Sprintf("%s (detected %q)", AttachmentReasonUnsupported, mediaType),
		}
	}
	return part, nil
}

// LoadAskContentParts validates the complete attachment set before returning
// any parts. Callers therefore never receive a partially constructed request.
func LoadAskContentParts(paths []string) ([]messages.ContentPart, error) {
	parts := make([]messages.ContentPart, 0, len(paths))
	for _, path := range paths {
		part, err := LoadAskContentPart(path)
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
	return parts, nil
}

func contentPartFromBytes(path string, data []byte) messages.ContentPart {
	name := filepath.Base(path)
	mediaType := detectMimeTypeFromBytes(data, filepath.Ext(path))

	switch {
	case strings.HasPrefix(mediaType, "image/"):
		return messages.ImagePart{Bytes: data, MediaType: mediaType}
	case strings.HasPrefix(mediaType, "audio/"):
		return messages.AudioPart{Bytes: data, MediaType: mediaType}
	case strings.HasPrefix(mediaType, "video/"):
		return messages.VideoPart{Bytes: data, MediaType: mediaType}
	default:
		return messages.FilePart{Bytes: data, Name: name, MediaType: mediaType}
	}
}

func contentPartMediaType(part messages.ContentPart) string {
	switch value := part.(type) {
	case messages.ImagePart:
		return value.MediaType
	case messages.AudioPart:
		return value.MediaType
	case messages.VideoPart:
		return value.MediaType
	case messages.FilePart:
		return value.MediaType
	default:
		return ""
	}
}
