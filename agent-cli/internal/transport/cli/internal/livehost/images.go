package livehost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const liveImageToolPathDescription = "Session-staged image path(s) (use one of these exact absolute paths):\n- "

const (
	liveImageStagingDirectoryMode os.FileMode = 0o700
	liveImageStagingFileMode      os.FileMode = 0o600
)

// stageLiveOpeningImages gives read_image a session-owned copy of each
// opening image and rewrites the participant capability snapshot before the
// live runner admits it. The live runtime receives image bytes, not host paths;
// only the tool definition and the short-lived staged files carry the path
// contract needed by a later provider tool call.
func stageLiveOpeningImages(request serviceSession.Request, liveRequest *runtimeSession.LiveRequest) (func() error, error) {
	if liveRequest == nil || len(request.ImagePaths) == 0 || liveRequest.Capabilities == nil ||
		!hasLiveTool(liveRequest.Capabilities.Definitions, runtimeTools.ReadImageToolID) {
		return noImageCleanup, nil
	}

	parts, err := liveOpeningImageParts(liveRequest.OpeningContentParts)
	if err != nil {
		return noImageCleanup, err
	}
	if len(request.ImagePaths) != len(parts) {
		return noImageCleanup, fmt.Errorf("stage live session images: source path count %d does not match image part count %d", len(request.ImagePaths), len(parts))
	}

	configDir, err := liveImageStagingConfigDir(request.ConfigDir)
	if err != nil {
		return noImageCleanup, fmt.Errorf("stage live session images: %w", err)
	}
	stagedPaths, cleanup, err := stageLiveImageFiles(configDir, request.ImagePaths, parts)
	if err != nil {
		return noImageCleanup, err
	}

	capabilities := *liveRequest.Capabilities
	configureStagedLiveCapabilities(&capabilities, stagedPaths)
	liveRequest.Capabilities = &capabilities
	return cleanup, nil
}

func noImageCleanup() error { return nil }

func liveOpeningImageParts(contentParts []messages.ContentPart) ([]messages.ImagePart, error) {
	parts := make([]messages.ImagePart, 0, len(contentParts))
	for _, content := range contentParts {
		part, ok := content.(messages.ImagePart)
		if !ok {
			return nil, fmt.Errorf("stage live session images: opening content part %T is not an image", content)
		}
		parts = append(parts, part)
	}
	return parts, nil
}

func stageLiveImageFiles(configDir string, sourcePaths []string, parts []messages.ImagePart) ([]string, func() error, error) {
	if err := os.MkdirAll(configDir, liveImageStagingDirectoryMode); err != nil {
		return nil, noImageCleanup, fmt.Errorf("stage live session images in %q: create config directory: %w", configDir, err)
	}
	stageDir, err := os.MkdirTemp(configDir, ".session-images-*")
	if err != nil {
		return nil, noImageCleanup, fmt.Errorf("stage live session images in %q: create staging directory: %w", configDir, err)
	}
	cleanup := func() error { return os.RemoveAll(stageDir) }

	stagedPaths := make([]string, len(parts))
	for index, part := range parts {
		path := filepath.Join(stageDir, fmt.Sprintf("image-%03d%s", index, liveImageStageExtension(sourcePaths[index], part.MediaType)))
		if err := os.WriteFile(path, part.Bytes, liveImageStagingFileMode); err != nil {
			cleanupErr := cleanup()
			return nil, noImageCleanup, errors.Join(fmt.Errorf("stage live session image %q: %w", sourcePaths[index], err), cleanupErr)
		}
		stagedPaths[index] = filepath.Clean(path)
	}
	return stagedPaths, cleanup, nil
}

func configureStagedLiveCapabilities(capabilities *runtimeSession.LiveCapabilities, stagedPaths []string) {
	if capabilities == nil {
		return
	}
	capabilities.Definitions = advertiseLiveImagePaths(capabilities.Definitions, stagedPaths)
	if capabilities.Handle != nil {
		capabilities.Handle = &stagedLiveCapabilityHandle{inner: capabilities.Handle, paths: append([]string(nil), stagedPaths...)}
		return
	}
	refresh := capabilities.RefreshDefinitions
	if refresh == nil {
		return
	}
	capabilities.RefreshDefinitions = func(ctx context.Context) ([]messages.ToolDefinition, error) {
		definitions, err := refresh(ctx)
		if err != nil {
			return nil, err
		}
		return advertiseLiveImagePaths(definitions, stagedPaths), nil
	}
}

func liveImageStagingConfigDir(configDir string) (string, error) {
	configDir = strings.TrimSpace(configDir)
	if configDir == "" {
		return "", errors.New("config directory is required")
	}
	abs, err := filepath.Abs(configDir)
	if err != nil {
		return "", fmt.Errorf("resolve config directory %q: %w", configDir, err)
	}
	return filepath.Clean(abs), nil
}

func liveImageStageExtension(sourcePath, mediaType string) string {
	if semicolon := strings.IndexByte(mediaType, ';'); semicolon >= 0 {
		mediaType = mediaType[:semicolon]
	}
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/avif":
		return ".avif"
	}

	ext := strings.ToLower(filepath.Ext(sourcePath))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".avif":
		return ext
	default:
		return ".img"
	}
}

func hasLiveTool(definitions []messages.ToolDefinition, name string) bool {
	for _, definition := range definitions {
		if definition.Name == name {
			return true
		}
	}
	return false
}

func advertiseLiveImagePaths(definitions []messages.ToolDefinition, paths []string) []messages.ToolDefinition {
	advertisement := liveImageToolPathDescription + strings.Join(paths, "\n- ")
	updated := messages.CanonicalToolDefinitions(definitions)
	for index := range updated {
		if updated[index].Name != runtimeTools.ReadImageToolID {
			continue
		}
		pathParameter := -1
		for parameterIndex := range updated[index].Parameters {
			if updated[index].Parameters[parameterIndex].Name == "path" {
				pathParameter = parameterIndex
				break
			}
		}
		if pathParameter < 0 {
			updated[index].Parameters = append(updated[index].Parameters, messages.ToolParameter{
				Name:        "path",
				Type:        "string",
				Description: advertisement,
				Required:    true,
			})
			continue
		}
		parameter := &updated[index].Parameters[pathParameter]
		prefix := strings.TrimSpace(parameter.Description)
		if prefix != "" {
			prefix += "\n"
		}
		parameter.Description = prefix + advertisement
	}
	return messages.CanonicalToolDefinitions(updated)
}

type stagedLiveCapabilityHandle struct {
	inner runtimeSession.LiveCapabilityHandle
	paths []string
}

func (h *stagedLiveCapabilityHandle) Initialize(ctx context.Context) error {
	return h.inner.Initialize(ctx)
}

func (h *stagedLiveCapabilityHandle) RefreshDefinitions(ctx context.Context) ([]messages.ToolDefinition, error) {
	definitions, err := h.inner.RefreshDefinitions(ctx)
	if err != nil {
		return nil, err
	}
	return advertiseLiveImagePaths(definitions, h.paths), nil
}

func (h *stagedLiveCapabilityHandle) Close() error {
	return h.inner.Close()
}

func (h *stagedLiveCapabilityHandle) BrowserWatch(ctx context.Context) <-chan runtimeSession.LiveCapabilityEvent {
	watcher, ok := h.inner.(runtimeSession.LiveCapabilityWatcher)
	if !ok {
		return nil
	}
	return watcher.BrowserWatch(ctx)
}
