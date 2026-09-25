// Package imagestage gives read_image session-owned copies of a live
// request's opening images and advertises their exact paths on the tool.
package imagestage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const pathAdvertisement = "Session-staged image path(s) (use one of these exact absolute paths):\n- "

const (
	extensionPNG  = ".png"
	extensionJPEG = ".jpg"
	extensionGIF  = ".gif"
	extensionWebP = ".webp"
	extensionAVIF = ".avif"
)

const (
	stagingDirectoryMode os.FileMode = 0o700
	stagingFileMode      os.FileMode = 0o600
)

// Stager is the stateless opening-image staging service.
type Stager struct{}

// New returns the opening-image staging service.
func New() *Stager { return &Stager{} }

var _ session.LiveImageStager = (*Stager)(nil)

// StageOpeningImages rewrites the participant capability snapshot before the
// live runner admits it. The live runtime receives image bytes, not host
// paths; only the tool definition and the short-lived staged files carry the
// path contract needed by a later provider tool call.
func (*Stager) StageOpeningImages(request session.LiveImageStageRequest, liveRequest *session.LiveRequest) (func() error, error) {
	if liveRequest == nil || len(request.SourcePaths) == 0 || liveRequest.Capabilities == nil ||
		!hasTool(liveRequest.Capabilities.Definitions, tools.ReadImageToolID) {
		return noCleanup, nil
	}
	parts, err := openingImageParts(liveRequest.OpeningContentParts)
	if err != nil {
		return noCleanup, err
	}
	if len(request.SourcePaths) != len(parts) {
		return noCleanup, fmt.Errorf("stage live session images: source path count %d does not match image part count %d", len(request.SourcePaths), len(parts))
	}
	directory, err := stagingParent(request.Directory)
	if err != nil {
		return noCleanup, fmt.Errorf("stage live session images: %w", err)
	}
	stagedPaths, cleanup, err := stageFiles(directory, request.SourcePaths, parts)
	if err != nil {
		return noCleanup, err
	}
	capabilities := *liveRequest.Capabilities
	advertiseStagedPaths(&capabilities, stagedPaths)
	liveRequest.Capabilities = &capabilities
	return cleanup, nil
}

func noCleanup() error { return nil }

func openingImageParts(contentParts []messages.ContentPart) ([]messages.ImagePart, error) {
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

func stageFiles(parent string, sourcePaths []string, parts []messages.ImagePart) ([]string, func() error, error) {
	if err := os.MkdirAll(parent, stagingDirectoryMode); err != nil {
		return nil, noCleanup, fmt.Errorf("stage live session images in %q: create config directory: %w", parent, err)
	}
	stageDir, err := os.MkdirTemp(parent, ".session-images-*")
	if err != nil {
		return nil, noCleanup, fmt.Errorf("stage live session images in %q: create staging directory: %w", parent, err)
	}
	cleanup := func() error { return os.RemoveAll(stageDir) }
	stagedPaths := make([]string, len(parts))
	for index, part := range parts {
		path := filepath.Join(stageDir, fmt.Sprintf("image-%03d%s", index, stageExtension(sourcePaths[index], part.MediaType)))
		if err := os.WriteFile(path, part.Bytes, stagingFileMode); err != nil {
			cleanupErr := cleanup()
			return nil, noCleanup, errors.Join(fmt.Errorf("stage live session image %q: %w", sourcePaths[index], err), cleanupErr)
		}
		stagedPaths[index] = filepath.Clean(path)
	}
	return stagedPaths, cleanup, nil
}

func stagingParent(directory string) (string, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return "", errors.New("config directory is required")
	}
	abs, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("resolve config directory %q: %w", directory, err)
	}
	return filepath.Clean(abs), nil
}

func stageExtension(sourcePath, mediaType string) string {
	if semicolon := strings.IndexByte(mediaType, ';'); semicolon >= 0 {
		mediaType = mediaType[:semicolon]
	}
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/png":
		return extensionPNG
	case "image/jpeg":
		return extensionJPEG
	case "image/gif":
		return extensionGIF
	case "image/webp":
		return extensionWebP
	case "image/avif":
		return extensionAVIF
	}
	ext := strings.ToLower(filepath.Ext(sourcePath))
	switch ext {
	case extensionPNG, extensionJPEG, ".jpeg", extensionGIF, extensionWebP, extensionAVIF:
		return ext
	default:
		return ".img"
	}
}

func hasTool(definitions []messages.ToolDefinition, name string) bool {
	for _, definition := range definitions {
		if definition.Name == name {
			return true
		}
	}
	return false
}

func advertiseStagedPaths(capabilities *session.LiveCapabilities, stagedPaths []string) {
	capabilities.Definitions = advertisePaths(capabilities.Definitions, stagedPaths)
	if capabilities.Handle != nil {
		capabilities.Handle = &stagedHandle{inner: capabilities.Handle, paths: append([]string(nil), stagedPaths...)}
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
		return advertisePaths(definitions, stagedPaths), nil
	}
}

func advertisePaths(definitions []messages.ToolDefinition, paths []string) []messages.ToolDefinition {
	advertisement := pathAdvertisement + strings.Join(paths, "\n- ")
	updated := messages.CanonicalToolDefinitions(definitions)
	for index := range updated {
		if updated[index].Name == tools.ReadImageToolID {
			advertisePathParameter(&updated[index], advertisement)
		}
	}
	return messages.CanonicalToolDefinitions(updated)
}

func advertisePathParameter(definition *messages.ToolDefinition, advertisement string) {
	for index := range definition.Parameters {
		parameter := &definition.Parameters[index]
		if parameter.Name != "path" {
			continue
		}
		prefix := strings.TrimSpace(parameter.Description)
		if prefix != "" {
			prefix += "\n"
		}
		parameter.Description = prefix + advertisement
		return
	}
	definition.Parameters = append(definition.Parameters, messages.ToolParameter{
		Name: "path", Type: "string", Description: advertisement, Required: true,
	})
}

type stagedHandle struct {
	inner session.LiveCapabilityHandle
	paths []string
}

func (h *stagedHandle) Initialize(ctx context.Context) error { return h.inner.Initialize(ctx) }

func (h *stagedHandle) RefreshDefinitions(ctx context.Context) ([]messages.ToolDefinition, error) {
	definitions, err := h.inner.RefreshDefinitions(ctx)
	if err != nil {
		return nil, err
	}
	return advertisePaths(definitions, h.paths), nil
}

func (h *stagedHandle) Close() error { return h.inner.Close() }

func (h *stagedHandle) BrowserWatch(ctx context.Context) <-chan session.LiveCapabilityEvent {
	watcher, ok := h.inner.(session.LiveCapabilityWatcher)
	if !ok {
		return nil
	}
	return watcher.BrowserWatch(ctx)
}
