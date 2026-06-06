package input

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-loop/pkg/messages"
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

// LoadContentPart reads the file at path and returns a ContentPart (ImagePart, AudioPart, VideoPart, or FilePart)
// based on the detected MIME type. The filename (base) is preserved for FilePart name.
func LoadContentPart(path string) (messages.ContentPart, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	name := filepath.Base(path)
	mediaType := detectMimeTypeFromBytes(data, filepath.Ext(path))

	switch {
	case strings.HasPrefix(mediaType, "image/"):
		return messages.ImagePart{Bytes: data, MediaType: mediaType}, nil
	case strings.HasPrefix(mediaType, "audio/"):
		return messages.AudioPart{Bytes: data, MediaType: mediaType}, nil
	case strings.HasPrefix(mediaType, "video/"):
		return messages.VideoPart{Bytes: data, MediaType: mediaType}, nil
	default:
		return messages.FilePart{Bytes: data, Name: name, MediaType: mediaType}, nil
	}
}
