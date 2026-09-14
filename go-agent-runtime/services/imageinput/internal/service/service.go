package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/imageinput"
)

// Service owns image preparation and first-turn publication. Hosts provide
// content loading and provider sessions through the public contract.
type Service struct {
	loader imageinput.ContentLoader
}

var _ imageinput.Service = (*Service)(nil)

// New creates an image-input service around an explicit host loader.
func New(loader imageinput.ContentLoader) *Service {
	return &Service{loader: loader}
}

func (service *Service) Prepare(ctx context.Context, paths []string, capabilities imageinput.Capabilities) ([]messages.ImagePart, error) {
	if !capabilities.SupportsImageInput {
		return nil, &imageinput.CapabilityError{Model: capabilities.Model, Capability: "image input"}
	}
	if service == nil || service.loader == nil {
		return nil, fmt.Errorf("prepare image input: content loader is not configured")
	}

	supported := normalizedSupportedMIMETypes(capabilities.SupportedInputMIMETypes)
	parts := make([]messages.ImagePart, 0, len(paths))
	for _, path := range paths {
		part, err := service.prepareOne(ctx, path, supported)
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
	return parts, nil
}

func (service *Service) prepareOne(ctx context.Context, path string, supported []string) (messages.ImagePart, error) {
	if err := contextError(ctx); err != nil {
		return messages.ImagePart{}, err
	}
	if path == "" {
		return messages.ImagePart{}, missingFile(path, os.ErrNotExist)
	}

	content, err := service.loader.Load(ctx, path)
	if err != nil {
		if contextErr := contextError(ctx); contextErr != nil {
			return messages.ImagePart{}, fmt.Errorf("load image %q: %w", path, errors.Join(err, contextErr))
		}
		return messages.ImagePart{}, classifyLoadError(path, err)
	}
	data, mediaType, isImage := imageContent(content)
	if len(data) == 0 {
		return messages.ImagePart{}, &imageinput.EmptyFileError{Path: path}
	}
	mediaType = normalizeMIME(mediaType)
	if !isImage || !containsMIME(supported, mediaType) {
		return messages.ImagePart{}, unsupportedMIME(path, mediaType, supported)
	}
	if err := validateDecodedImage(path, mediaType, data); err != nil {
		return messages.ImagePart{}, err
	}
	return messages.ImagePart{Bytes: cloneBytes(data), MediaType: mediaType}, nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func classifyLoadError(path string, cause error) error {
	if os.IsNotExist(cause) || errors.Is(cause, os.ErrNotExist) {
		return missingFile(path, cause)
	}
	return unreadableFile(path, cause)
}

func normalizedSupportedMIMETypes(supported []string) []string {
	if len(supported) == 0 {
		return []string{imagePNG, imageJPEG}
	}
	result := make([]string, 0, len(supported))
	seen := make(map[string]struct{}, len(supported))
	for _, mimeType := range supported {
		mimeType = normalizeMIME(mimeType)
		if mimeType == "" {
			continue
		}
		if _, ok := seen[mimeType]; ok {
			continue
		}
		seen[mimeType] = struct{}{}
		result = append(result, mimeType)
	}
	return result
}

func normalizeMIME(mimeType string) string {
	return strings.ToLower(strings.TrimSpace(mimeType))
}

func containsMIME(supported []string, mimeType string) bool {
	for _, candidate := range supported {
		if candidate == mimeType {
			return true
		}
	}
	return false
}

func cloneBytes(data []byte) []byte {
	return append([]byte(nil), data...)
}
