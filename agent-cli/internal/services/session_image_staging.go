package services

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const sessionImageToolPathDescription = "Session-staged image path(s) (use one of these exact absolute paths):\n- "

// prepareSessionImageToolAccess gives read_image a stable, session-owned copy
// of each initial image and advertises those exact paths to the provider. The
// inline image turn still uses the validated parts supplied by the caller;
// staging is only needed for a later model-issued read_image call.
func prepareSessionImageToolAccess(opts SessionRunOptions, sourcePaths []string, parts []messages.ImagePart) (SessionRunOptions, func(), error) {
	if !sessionHasTool(opts.ToolDefinitions, tools.ReadImageToolID) {
		return opts, func() {}, nil
	}
	if len(sourcePaths) != len(parts) {
		return opts, func() {}, fmt.Errorf("stage session images: source path count %d does not match image part count %d", len(sourcePaths), len(parts))
	}

	configDir, err := sessionImageStagingConfigDir(opts.ConfigDir)
	if err != nil {
		return opts, func() {}, fmt.Errorf("stage session images: %w", err)
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return opts, func() {}, fmt.Errorf("stage session images in %q: create config directory: %w", configDir, err)
	}
	stageDir, err := os.MkdirTemp(configDir, ".session-images-*")
	if err != nil {
		return opts, func() {}, fmt.Errorf("stage session images in %q: create staging directory: %w", configDir, err)
	}
	cleanup := func() { _ = os.RemoveAll(stageDir) }

	stagedPaths := make([]string, len(parts))
	for index, part := range parts {
		path := filepath.Join(stageDir, fmt.Sprintf("image-%03d%s", index, sessionImageStageExtension(sourcePaths[index], part.MediaType)))
		if err := os.WriteFile(path, part.Bytes, 0o600); err != nil {
			cleanup()
			return opts, func() {}, fmt.Errorf("stage session image %q: %w", sourcePaths[index], err)
		}
		stagedPaths[index] = filepath.Clean(path)
	}

	opts.ToolDefinitions = advertiseSessionImagePaths(opts.ToolDefinitions, stagedPaths)
	return opts, cleanup, nil
}

func sessionImageStagingConfigDir(configDir string) (string, error) {
	configDir = strings.TrimSpace(configDir)
	if configDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		configDir = filepath.Join(homeDir, config.ConfigDirName)
	}
	abs, err := filepath.Abs(configDir)
	if err != nil {
		return "", fmt.Errorf("resolve config directory %q: %w", configDir, err)
	}
	return filepath.Clean(abs), nil
}

func sessionImageStageExtension(sourcePath, mediaType string) string {
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

func advertiseSessionImagePaths(definitions []messages.ToolDefinition, paths []string) []messages.ToolDefinition {
	advertisement := sessionImageToolPathDescription + strings.Join(paths, "\n- ")
	updated := messages.CanonicalToolDefinitions(definitions)
	for index := range updated {
		if updated[index].Name != tools.ReadImageToolID {
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
