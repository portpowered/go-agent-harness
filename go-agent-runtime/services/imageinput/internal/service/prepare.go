package service

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/imageinput"
)

const (
	imagePNG  = "image/png"
	imageJPEG = "image/jpeg"
)

func imageContent(content messages.ContentPart) (data []byte, mediaType string, isImage bool) {
	switch part := content.(type) {
	case messages.ImagePart:
		return part.Bytes, part.MediaType, true
	case *messages.ImagePart:
		if part == nil {
			return nil, "", true
		}
		return part.Bytes, part.MediaType, true
	case messages.AudioPart:
		return part.Bytes, part.MediaType, false
	case *messages.AudioPart:
		if part == nil {
			return nil, "", false
		}
		return part.Bytes, part.MediaType, false
	case messages.VideoPart:
		return part.Bytes, part.MediaType, false
	case *messages.VideoPart:
		if part == nil {
			return nil, "", false
		}
		return part.Bytes, part.MediaType, false
	case messages.FilePart:
		return part.Bytes, part.MediaType, false
	case *messages.FilePart:
		if part == nil {
			return nil, "", false
		}
		return part.Bytes, part.MediaType, false
	default:
		return nil, "", false
	}
}

func validateDecodedImage(path, mediaType string, data []byte) error {
	_, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return invalidContent(path, mediaType, fmt.Errorf("decode %s content: %w", mediaType, err))
	}
	if expected := expectedImageFormat(mediaType); expected != "" && format != expected {
		return invalidContent(path, mediaType, fmt.Errorf("decoded format %q does not match %s", format, mediaType))
	}
	return nil
}

func expectedImageFormat(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case imagePNG:
		return "png"
	case imageJPEG:
		return "jpeg"
	default:
		return ""
	}
}

func missingFile(path string, cause error) error {
	return &imageinput.MissingFileError{InputError: inputError(
		imageinput.ErrMissingFile,
		path,
		"",
		fmt.Sprintf("image input %q is missing: %v", path, cause),
		nil,
		cause,
	)}
}

func unreadableFile(path string, cause error) error {
	return &imageinput.UnreadableFileError{InputError: inputError(
		imageinput.ErrUnreadableFile,
		path,
		"",
		fmt.Sprintf("image input %q cannot be read: %v", path, cause),
		nil,
		cause,
	)}
}

func unsupportedMIME(path, mediaType string, supported []string) error {
	return &imageinput.UnsupportedMIMEError{InputError: inputError(
		imageinput.ErrUnsupportedMIME,
		path,
		mediaType,
		fmt.Sprintf("image input %q has unsupported MIME type %q (supported: %s)", path, mediaType, strings.Join(supported, ", ")),
		supported,
		nil,
	)}
}

func invalidContent(path, mediaType string, cause error) error {
	return &imageinput.InvalidContentError{InputError: inputError(
		imageinput.ErrInvalidContent,
		path,
		mediaType,
		fmt.Sprintf("image input %q is not valid %s content: %v", path, mediaType, cause),
		nil,
		cause,
	)}
}

func inputError(kind imageinput.ErrorKind, path, mediaType, message string, supported []string, cause error) imageinput.InputError {
	return imageinput.InputError{
		Path:          path,
		DetectedMIME:  mediaType,
		SupportedMIME: append([]string(nil), supported...),
		Cause:         cause,
		Err:           cause,
		Kind:          kind,
		Message:       message,
	}
}
