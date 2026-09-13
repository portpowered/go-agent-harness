package service

import (
	"bytes"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiocodec"
)

func detectFormat(input []byte, hint string) (audiocodec.InputFormat, error) {
	if format, ok := formatFromHint(hint); ok {
		return format, nil
	}
	if strings.TrimSpace(hint) != "" {
		return "", newError(audiocodec.ErrorUnsupportedFormat, nil, "unsupported format hint "+hint)
	}
	if format, ok := formatFromBytes(input); ok {
		return format, nil
	}
	return "", newError(audiocodec.ErrorUnsupportedFormat, nil, "could not identify an audio format")
}

func formatFromHint(hint string) (audiocodec.InputFormat, bool) {
	hint = strings.ToLower(strings.TrimSpace(hint))
	if hint == "" {
		return "", false
	}
	if ext := filepath.Ext(hint); ext != "" && ext != "." {
		hint = ext
	}
	switch hint {
	case "wav", ".wav", "audio/wav", "audio/x-wav", "audio/wave":
		return audiocodec.FormatWAV, true
	case "mp3", ".mp3", "audio/mpeg", "audio/mp3":
		return audiocodec.FormatMP3, true
	case "flac", ".flac", "audio/flac", "audio/x-flac":
		return audiocodec.FormatFLAC, true
	case "ogg", ".ogg", "audio/ogg":
		return audiocodec.FormatOGG, true
	case "opus", ".opus", "audio/opus":
		return audiocodec.FormatOpus, true
	case "aac", ".aac", "audio/aac", "audio/x-aac":
		return audiocodec.FormatAAC, true
	case "m4a", ".m4a", "mp4", ".mp4", "audio/mp4", "audio/x-m4a":
		return audiocodec.FormatM4A, true
	case "webm", ".webm", "audio/webm", "video/webm":
		return audiocodec.FormatWebM, true
	default:
		return "", false
	}
}

func formatFromBytes(input []byte) (audiocodec.InputFormat, bool) {
	if len(input) >= 4 && bytes.Equal(input[:4], []byte("RIFF")) {
		return audiocodec.FormatWAV, true
	}
	if len(input) >= 4 && bytes.Equal(input[:4], []byte("fLaC")) {
		return audiocodec.FormatFLAC, true
	}
	if len(input) >= 4 && bytes.Equal(input[:4], []byte("OggS")) {
		if bytes.Contains(input, []byte("OpusHead")) {
			return audiocodec.FormatOpus, true
		}
		return audiocodec.FormatOGG, true
	}
	if len(input) >= 8 && bytes.Equal(input[4:8], []byte("ftyp")) {
		return audiocodec.FormatM4A, true
	}
	if len(input) >= 4 && bytes.Equal(input[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}) {
		return audiocodec.FormatWebM, true
	}
	if len(input) >= 3 && bytes.Equal(input[:3], []byte("ID3")) {
		return audiocodec.FormatMP3, true
	}
	if isMP3Sync(input) {
		return audiocodec.FormatMP3, true
	}
	if isAACSync(input) {
		return audiocodec.FormatAAC, true
	}

	switch http.DetectContentType(input) {
	case "audio/wav", "audio/x-wav":
		return audiocodec.FormatWAV, true
	case "audio/mpeg":
		return audiocodec.FormatMP3, true
	case "audio/ogg":
		return audiocodec.FormatOGG, true
	case "audio/flac":
		return audiocodec.FormatFLAC, true
	default:
		return "", false
	}
}

func isMP3Sync(input []byte) bool {
	if len(input) < 2 || input[0] != 0xff {
		return false
	}
	return input[1]&0xe0 == 0xe0 && input[1]&0x06 != 0
}

func isAACSync(input []byte) bool {
	if len(input) < 2 || input[0] != 0xff {
		return false
	}
	return input[1]&0xf6 == 0xf0
}
