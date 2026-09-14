package service

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

func (s *Service) PrepareImageParts(paths []string, capabilities sessionturn.ImageCapabilities) ([]messages.ImagePart, error) {
	if !capabilities.SupportsImageInput {
		return nil, &sessionturn.ImageCapabilityError{Model: capabilities.Model, Capability: "image input"}
	}
	supported := append([]string(nil), capabilities.SupportedInputMIMETypes...)
	if len(supported) == 0 {
		supported = []string{"image/png", "image/jpeg"}
	}
	parts := make([]messages.ImagePart, 0, len(paths))
	for _, path := range paths {
		part, err := readImagePart(path, supported)
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
	return parts, nil
}

func readImagePart(path string, supported []string) (messages.ImagePart, error) {
	if path == "" {
		return messages.ImagePart{}, imageFileError(sessionturn.ErrImageMissingFile, path, "", nil, "session image file is missing")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		kind := sessionturn.ErrImageUnreadableFile
		if os.IsNotExist(err) {
			kind = sessionturn.ErrImageMissingFile
			return messages.ImagePart{}, imageFileError(kind, path, "", err, fmt.Sprintf("session image %q is missing: %v", path, err))
		}
		return messages.ImagePart{}, imageFileError(kind, path, "", err, fmt.Sprintf("session image %q cannot be read: %v", path, err))
	}
	if len(data) == 0 {
		return messages.ImagePart{}, &sessionturn.ImageEmptyFileError{Path: path}
	}
	mediaType := http.DetectContentType(data)
	if mediaType == "application/octet-stream" || mediaType == "text/plain; charset=utf-8" {
		if candidate := mime.TypeByExtension(filepath.Ext(path)); candidate != "" {
			mediaType = candidate
		}
	}
	if !strings.HasPrefix(mediaType, "image/") || !slices.Contains(supported, mediaType) {
		return messages.ImagePart{}, imageFileError(sessionturn.ErrImageUnsupportedMIME, path, mediaType, nil, fmt.Sprintf("session image %q has unsupported MIME type %q (supported: %s)", path, mediaType, strings.Join(supported, ", ")))
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return messages.ImagePart{}, imageFileError(sessionturn.ErrImageInvalidContent, path, mediaType, err, fmt.Sprintf("session image %q is not valid %s content: %v", path, mediaType, err))
	}
	return messages.ImagePart{Bytes: append([]byte(nil), data...), MediaType: mediaType}, nil
}

func imageFileError(kind error, path, mediaType string, cause error, message string) *sessionturn.ImageFileError {
	return &sessionturn.ImageFileError{Path: path, DetectedMIME: mediaType, Cause: cause, Kind: kind, Message: message}
}

func (s *Service) SendImageTurn(ctx context.Context, target messages.Session, text string, parts []messages.ImagePart, requestResponse bool) error {
	return sendImageTurn(ctx, target, text, parts, requestResponse)
}

func sendImageTurn(ctx context.Context, target messages.Session, text string, parts []messages.ImagePart, requestResponse bool) error {
	content := make([]messages.ContentPart, 0, len(parts)+1)
	if text != "" {
		content = append(content, messages.TextPart{Text: text})
	}
	for _, part := range parts {
		content = append(content, messages.ImagePart{Bytes: append([]byte(nil), part.Bytes...), MediaType: part.MediaType})
	}
	message := messages.Message{Role: messages.RoleUser, ContentParts: content}
	if requestResponse {
		sender, ok := target.(sessionturn.CompleteMessageSender)
		if !ok || !sender.SendMessage(ctx, message) {
			return sessionturn.ErrImageSend
		}
		return nil
	}
	sender, ok := target.(sessionturn.CompleteMessageWithoutResponseSender)
	if !ok || !sender.SendMessageWithoutResponse(ctx, message) {
		return sessionturn.ErrImageSend
	}
	return nil
}
