// Package images implements session image admission: capability decisions,
// bounded validation of local image files, read_image binding, and staging.
package images

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // registers the JPEG decoder used for content validation
	_ "image/png"  // registers the PNG decoder used for content validation
	"io/fs"
	"slices"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

const (
	mimePNG  = "image/png"
	mimeJPEG = "image/jpeg"

	errNoLoader sessionturn.Error = "session image loader is not configured"
)

// DefaultMIMETypes is the MIME set used when a model declares none.
func DefaultMIMETypes() []string { return []string{mimePNG, mimeJPEG} }

// PrepareParts validates every path in order and returns independent parts.
// The first failure is returned as a typed error.
func PrepareParts(request sessionturn.ImagePartsRequest) ([]messages.ImagePart, error) {
	capabilities := request.Capabilities
	if !capabilities.SupportsImageInput {
		return nil, &sessionturn.ImageCapabilityError{Model: capabilities.Model, Capability: sessionturn.ImageInputCapability}
	}
	supported := append([]string(nil), capabilities.SupportedInputMIMETypes...)
	if len(supported) == 0 {
		supported = DefaultMIMETypes()
	}
	if request.Load == nil {
		return nil, errNoLoader
	}
	parts := make([]messages.ImagePart, 0, len(request.Paths))
	for _, path := range request.Paths {
		part, err := preparePart(path, supported, request.Load)
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
	return parts, nil
}

func preparePart(path string, supported []string, load sessionturn.ImageContentLoader) (messages.ImagePart, error) {
	if path == "" {
		return messages.ImagePart{}, missing(path, fs.ErrNotExist)
	}
	content, err := load(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return messages.ImagePart{}, missing(path, err)
		}
		return messages.ImagePart{}, unreadable(path, err)
	}
	data, mediaType, isImage := contentBytes(content)
	if len(data) == 0 {
		return messages.ImagePart{}, &sessionturn.ImageEmptyFileError{Path: path}
	}
	if !isImage || !slices.Contains(supported, mediaType) {
		return messages.ImagePart{}, unsupported(path, mediaType, supported)
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return messages.ImagePart{}, &sessionturn.ImageFileError{
			Path: path, DetectedMIME: mediaType, Kind: sessionturn.ErrImageInvalidContent, Cause: err,
			Message: fmt.Sprintf("session image %q is not valid %s content: %v", path, mediaType, err),
		}
	}
	return messages.ImagePart{Bytes: append([]byte(nil), data...), MediaType: mediaType}, nil
}

func contentBytes(content messages.ContentPart) (data []byte, mediaType string, isImage bool) {
	switch part := content.(type) {
	case messages.ImagePart:
		return part.Bytes, part.MediaType, true
	case messages.AudioPart:
		return part.Bytes, part.MediaType, false
	case messages.VideoPart:
		return part.Bytes, part.MediaType, false
	case messages.FilePart:
		return part.Bytes, part.MediaType, false
	default:
		return nil, "", false
	}
}

func missing(path string, err error) error {
	return &sessionturn.ImageFileError{
		Path: path, Kind: sessionturn.ErrImageMissingFile, Cause: err,
		Message: fmt.Sprintf("session image %q is missing: %v", path, err),
	}
}

func unreadable(path string, err error) error {
	return &sessionturn.ImageFileError{
		Path: path, Kind: sessionturn.ErrImageUnreadableFile, Cause: err,
		Message: fmt.Sprintf("session image %q cannot be read: %v", path, err),
	}
}

func unsupported(path, mediaType string, supported []string) error {
	return &sessionturn.ImageFileError{
		Path: path, DetectedMIME: mediaType, SupportedMIME: append([]string(nil), supported...),
		Kind:    sessionturn.ErrImageUnsupportedMIME,
		Message: fmt.Sprintf("session image %q has unsupported MIME type %q (supported: %s)", path, mediaType, strings.Join(supported, ", ")),
	}
}

// CapabilityError reports a model without image input.
func CapabilityError(model string) error {
	return &sessionturn.ImageCapabilityError{Model: strings.TrimSpace(model), Capability: sessionturn.ImageInputCapability}
}
