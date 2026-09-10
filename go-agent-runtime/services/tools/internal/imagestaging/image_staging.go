// Package imagestaging owns the private filesystem and tool-surface details
// behind the public tools.ImageStaging contract.
package imagestaging

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const toolPathDescription = "Session-staged image path(s) (use one of these exact absolute paths):\n- "

type fileOps struct {
	mkdirAll  func(string, os.FileMode) error
	mkdirTemp func(string, string) (string, error)
	writeFile func(string, []byte, os.FileMode) error
	removeAll func(string) error
}

// Service is the private implementation of public.ImageStaging.
type Service struct {
	files fileOps
}

var _ public.ImageStaging = (*Service)(nil)

// New constructs an inert image stager. Filesystem effects begin only when
// Stage is called with a request containing the read_image capability.
func New() *Service {
	return &Service{files: fileOps{
		mkdirAll:  os.MkdirAll,
		mkdirTemp: os.MkdirTemp,
		writeFile: os.WriteFile,
		removeAll: os.RemoveAll,
	}}
}

func (s *Service) Stage(ctx context.Context, request public.ImageStagingRequest) (public.ImageStagingResult, error) {
	if ctx == nil {
		return public.ImageStagingResult{}, fmt.Errorf("stage session images: context is required")
	}
	if !hasReadImage(request.ToolDefinitions) {
		return noOpResult(request), nil
	}
	root, err := validateStageRequest(ctx, request)
	if err != nil {
		return public.ImageStagingResult{}, err
	}
	files := s.files
	if files.mkdirAll == nil || files.mkdirTemp == nil || files.writeFile == nil || files.removeAll == nil {
		return public.ImageStagingResult{}, fmt.Errorf("stage session images: filesystem operations are not configured")
	}
	if err := files.mkdirAll(root, 0o700); err != nil {
		return public.ImageStagingResult{}, fmt.Errorf("stage session images in %q: create config directory: %w", root, err)
	}
	stageDir, err := files.mkdirTemp(root, ".session-images-*")
	if err != nil {
		return public.ImageStagingResult{}, fmt.Errorf("stage session images in %q: create staging directory: %w", root, err)
	}
	cleanup := newCleanup(stageDir, files.removeAll)
	stagedPaths, err := writeStagedImages(ctx, stageDir, files, request)
	if err != nil {
		_ = cleanup()
		return public.ImageStagingResult{}, err
	}

	return public.ImageStagingResult{
		StagedPaths:            append([]string(nil), stagedPaths...),
		ToolDefinitions:        advertise(request.ToolDefinitions, stagedPaths),
		RefreshToolDefinitions: refreshed(request.RefreshToolDefinitions, stagedPaths),
		Cleanup:                cleanup,
	}, nil
}

func validateStageRequest(ctx context.Context, request public.ImageStagingRequest) (string, error) {
	if len(request.SourcePaths) != len(request.ImageParts) {
		return "", fmt.Errorf("stage session images: source path count %d does not match image part count %d", len(request.SourcePaths), len(request.ImageParts))
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	root := filepath.Clean(request.StagingRoot)
	if strings.TrimSpace(request.StagingRoot) == "" {
		return "", fmt.Errorf("stage session images: staging root is required")
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("stage session images: staging root %q is not absolute", request.StagingRoot)
	}
	return root, nil
}

func writeStagedImages(ctx context.Context, stageDir string, files fileOps, request public.ImageStagingRequest) ([]string, error) {
	stagedPaths := make([]string, len(request.ImageParts))
	for index, part := range request.ImageParts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := filepath.Join(stageDir, fmt.Sprintf("image-%03d%s", index, stageExtension(request.SourcePaths[index], part.MediaType)))
		if err := files.writeFile(path, part.Bytes, 0o600); err != nil {
			return nil, fmt.Errorf("stage session image %q: %w", request.SourcePaths[index], err)
		}
		stagedPaths[index] = filepath.Clean(path)
	}
	return stagedPaths, nil
}

func noOpResult(request public.ImageStagingRequest) public.ImageStagingResult {
	return public.ImageStagingResult{
		ToolDefinitions:        messages.CanonicalToolDefinitions(request.ToolDefinitions),
		RefreshToolDefinitions: request.RefreshToolDefinitions,
		Cleanup:                func() error { return nil },
	}
}

func newCleanup(path string, removeAll func(string) error) func() error {
	var once sync.Once
	var cleanupErr error
	return func() error {
		once.Do(func() { cleanupErr = removeAll(path) })
		return cleanupErr
	}
}

func hasReadImage(definitions []messages.ToolDefinition) bool {
	for _, definition := range definitions {
		if definition.Name == public.ReadImageToolID {
			return true
		}
	}
	return false
}

func stageExtension(sourcePath, mediaType string) string {
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

func advertise(definitions []messages.ToolDefinition, paths []string) []messages.ToolDefinition {
	advertisement := toolPathDescription + strings.Join(paths, "\n- ")
	updated := messages.CanonicalToolDefinitions(definitions)
	for index := range updated {
		if updated[index].Name != public.ReadImageToolID {
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

func refreshed(refresh func(context.Context) ([]messages.ToolDefinition, error), paths []string) func(context.Context) ([]messages.ToolDefinition, error) {
	if refresh == nil {
		return nil
	}
	ownedPaths := append([]string(nil), paths...)
	return func(ctx context.Context) ([]messages.ToolDefinition, error) {
		if ctx == nil {
			return nil, fmt.Errorf("refresh staged image tools: context is required")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		definitions, err := refresh(ctx)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return advertise(definitions, ownedPaths), nil
	}
}
