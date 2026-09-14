package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const (
	maxImageBytes      = sessionturn.MaxImageBytes
	maxImageCount      = sessionturn.MaxImageCount
	maxImageTotalBytes = sessionturn.MaxImageTotalBytes
)

func (s *Service) ResolveImageCapabilities(request sessionturn.ImageCapabilityRequest) (sessionturn.ImageCapabilities, error) {
	provider := strings.TrimSpace(request.Provider)
	if !strings.EqualFold(provider, "openai") {
		return sessionturn.ImageCapabilities{}, imageCapabilityError(request.Model)
	}
	model := strings.TrimSpace(request.Model)
	if model == "" {
		if request.ModelProvided {
			return sessionturn.ImageCapabilities{}, imageCapabilityError(model)
		}
		model = providers.OpenAIRealtimeDefaultModel
	}
	if request.ModelCatalog == nil {
		return sessionturn.ImageCapabilities{}, errors.New("session turn image model catalog is not configured")
	}
	realtimeModel, ok := request.ModelCatalog.LookupRealtimeModel(provider, model)
	if !ok || !realtimeModel.SupportsImageInput {
		return sessionturn.ImageCapabilities{}, imageCapabilityError(model)
	}
	if request.ConfiguredModel != nil && !configuredModelSupportsImageInput(request.ConfiguredModel) {
		return sessionturn.ImageCapabilities{}, imageCapabilityError(model)
	}
	supported := []string(nil)
	if request.ConfiguredModel != nil {
		supported = append(supported, request.ConfiguredModel.SupportedInputMIMETypes...)
	}
	return sessionturn.ImageCapabilities{
		Model:                   model,
		SupportsImageInput:      true,
		SupportedInputMIMETypes: supported,
	}, nil
}

func imageCapabilityError(model string) error {
	return &sessionturn.ImageCapabilityError{Model: strings.TrimSpace(model), Capability: "image input"}
}

func configuredModelSupportsImageInput(model *sessionturn.ImageModelMetadata) bool {
	return model != nil && (slices.Contains(model.InputModalities, "image") ||
		(len(model.InputModalities) == 0 && slices.ContainsFunc(model.SupportedInputMIMETypes, func(mime string) bool {
			return strings.HasPrefix(mime, "image/")
		})))
}

func (s *Service) PrepareImageParts(paths []string, capabilities sessionturn.ImageCapabilities) ([]messages.ImagePart, error) {
	return prepareImageParts(paths, capabilities)
}

func prepareImageParts(paths []string, capabilities sessionturn.ImageCapabilities) ([]messages.ImagePart, error) {
	if !capabilities.SupportsImageInput {
		return nil, &sessionturn.ImageCapabilityError{Model: capabilities.Model, Capability: "image input"}
	}
	if len(paths) > maxImageCount {
		return nil, fmt.Errorf("%w: got %d images, maximum is %d", sessionturn.ErrImageCountLimit, len(paths), maxImageCount)
	}
	supported := append([]string(nil), capabilities.SupportedInputMIMETypes...)
	if len(supported) == 0 {
		supported = []string{"image/png", "image/jpeg"}
	}
	parts := make([]messages.ImagePart, 0, len(paths))
	totalBytes := 0
	for _, path := range paths {
		part, err := readImagePart(path, supported)
		if err != nil {
			return nil, err
		}
		if totalBytes > maxImageTotalBytes-len(part.Bytes) {
			return nil, fmt.Errorf("%w: maximum is %d bytes", sessionturn.ErrImageAggregateLimit, maxImageTotalBytes)
		}
		totalBytes += len(part.Bytes)
		parts = append(parts, part)
	}
	return parts, nil
}

func (s *Service) PrepareImage(ctx context.Context, request sessionturn.ImagePreparationRequest) (sessionturn.ImagePreparationResult, error) {
	if ctx == nil {
		return sessionturn.ImagePreparationResult{}, errors.New("session image preparation context is required")
	}
	if err := ctx.Err(); err != nil {
		return sessionturn.ImagePreparationResult{}, err
	}
	capabilities := request.Capabilities
	if !capabilities.SupportsImageInput && request.CapabilityRequest != nil {
		resolved, err := s.ResolveImageCapabilities(*request.CapabilityRequest)
		if err != nil {
			return sessionturn.ImagePreparationResult{}, err
		}
		capabilities = resolved
	}
	parts, err := prepareImageParts(request.SourcePaths, capabilities)
	if err != nil {
		return sessionturn.ImagePreparationResult{}, err
	}
	result := sessionturn.ImagePreparationResult{
		Parts:                  cloneImageParts(parts),
		Capabilities:           capabilities,
		ToolExecutor:           request.ToolExecutor,
		ToolDefinitions:        messages.CanonicalToolDefinitions(request.ToolDefinitions),
		RefreshToolDefinitions: request.RefreshToolDefinitions,
		Cleanup:                func() error { return nil },
	}
	if !hasReadImageTool(result.ToolDefinitions) {
		return result, nil
	}
	if len(request.SourcePaths) != len(parts) {
		return sessionturn.ImagePreparationResult{}, fmt.Errorf("stage session images: source path count %d does not match image part count %d", len(request.SourcePaths), len(parts))
	}
	staged, err := s.StageImageTools(ctx, tools.ImageStagingRequest{
		StagingRoot:            request.StagingRoot,
		SourcePaths:            request.SourcePaths,
		ImageParts:             parts,
		ToolDefinitions:        result.ToolDefinitions,
		RefreshToolDefinitions: request.RefreshToolDefinitions,
	})
	if err != nil {
		return sessionturn.ImagePreparationResult{}, err
	}
	result.ToolDefinitions = messages.CanonicalToolDefinitions(staged.ToolDefinitions)
	result.RefreshToolDefinitions = staged.RefreshToolDefinitions
	if staged.Cleanup != nil {
		result.Cleanup = staged.Cleanup
	}
	return result, nil
}

func hasReadImageTool(definitions []messages.ToolDefinition) bool {
	for _, definition := range definitions {
		if definition.Name == tools.ReadImageToolID {
			return true
		}
	}
	return false
}

func readImagePart(path string, supported []string) (part messages.ImagePart, retErr error) {
	if path == "" {
		return messages.ImagePart{}, imageFileError(sessionturn.ErrImageMissingFile, path, "", nil, "session image file is missing")
	}
	file, err := os.Open(path)
	if err != nil {
		kind := sessionturn.ErrImageUnreadableFile
		if os.IsNotExist(err) {
			kind = sessionturn.ErrImageMissingFile
			return messages.ImagePart{}, imageFileError(kind, path, "", err, fmt.Sprintf("session image %q is missing: %v", path, err))
		}
		return messages.ImagePart{}, imageFileError(kind, path, "", err, fmt.Sprintf("session image %q cannot be read: %v", path, err))
	}
	defer func() { retErr = errors.Join(retErr, file.Close()) }()
	data, err := io.ReadAll(io.LimitReader(file, maxImageBytes+1))
	if err != nil {
		return messages.ImagePart{}, imageFileError(sessionturn.ErrImageUnreadableFile, path, "", err, fmt.Sprintf("session image %q cannot be read: %v", path, err))
	}
	if len(data) > maxImageBytes {
		return messages.ImagePart{}, imageFileError(sessionturn.ErrImageInvalidContent, path, "", nil, fmt.Sprintf("session image %q exceeds the %d-byte limit", path, maxImageBytes))
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
