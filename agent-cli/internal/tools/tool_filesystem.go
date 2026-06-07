package tools

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	_ "golang.org/x/image/webp"
)

// validatePath ensures the given path is within the workspace if restrict is true.
func validatePath(path, workspace string, restrict bool) (string, error) {
	if workspace == "" {
		return path, fmt.Errorf("workspace is not defined")
	}

	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("failed to resolve workspace path: %w", err)
	}

	var absPath string
	if filepath.IsAbs(path) {
		absPath = filepath.Clean(path)
	} else {
		absPath, err = filepath.Abs(filepath.Join(absWorkspace, path))
		if err != nil {
			return "", fmt.Errorf("failed to resolve file path: %w", err)
		}
	}

	if restrict {
		if !isWithinWorkspace(absPath, absWorkspace) {
			return "", fmt.Errorf("access denied: path is outside the workspace")
		}

		var resolved string
		workspaceReal := absWorkspace
		if resolved, err = filepath.EvalSymlinks(absWorkspace); err == nil {
			workspaceReal = resolved
		}

		if resolved, err = filepath.EvalSymlinks(absPath); err == nil {
			if !isWithinWorkspace(resolved, workspaceReal) {
				return "", fmt.Errorf("access denied: symlink resolves outside workspace")
			}
		} else if os.IsNotExist(err) {
			var parentResolved string
			if parentResolved, err = resolveExistingAncestor(filepath.Dir(absPath)); err == nil {
				if !isWithinWorkspace(parentResolved, workspaceReal) {
					return "", fmt.Errorf("access denied: symlink resolves outside workspace")
				}
			} else if !os.IsNotExist(err) {
				return "", fmt.Errorf("failed to resolve path: %w", err)
			}
		} else {
			return "", fmt.Errorf("failed to resolve path: %w", err)
		}
	}

	return absPath, nil
}

func resolveExistingAncestor(path string) (string, error) {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return resolved, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		if filepath.Dir(current) == current {
			return "", os.ErrNotExist
		}
	}
}

func isWithinWorkspace(candidate, workspace string) bool {
	rel, err := filepath.Rel(filepath.Clean(workspace), filepath.Clean(candidate))
	return err == nil && filepath.IsLocal(rel)
}

// mediaKind is the high-level type of file content for read_file tool handling.
type mediaKind int

const (
	mediaText mediaKind = iota
	mediaImage
	mediaVideo
	mediaAudio
)

// mediaKindFromPath returns the media kind based on file extension.
// Order matters: image first, then video (so .webm → video), then audio, then text.
func mediaKindFromPath(path string) mediaKind {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp":
		return mediaImage
	case ".flv", ".mov", ".mpeg", ".mpg", ".mp4", ".webm", ".wmv", ".3gp", ".3gpp":
		return mediaVideo
	case ".aac", ".flac", ".mp3", ".m4a", ".mpga", ".ogg", ".opus", ".wav":
		return mediaAudio
	default:
		return mediaText
	}
}

// videoMediaType returns the MIME type for the video format indicated by the path extension (Gemini-supported types).
func videoMediaType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".flv":
		return "video/x-flv"
	case ".mov":
		return "video/quicktime"
	case ".mpeg", ".mpg":
		return "video/mpeg"
	case ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".wmv":
		return "video/wmv"
	case ".3gp", ".3gpp":
		return "video/3gpp"
	default:
		return "video/mp4"
	}
}

// imageMediaType returns the MIME type for the image format indicated by the path extension.
func imageMediaType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "image/jpeg"
	}
}

// imageToNative decodes image data and returns it in the format implied by the path extension:
// JPEG and PNG are re-encoded (to validate/sanitize); GIF and WebP are validated by decode and returned as-is to preserve animation and avoid extra dependencies.
func imageToNative(path string, content []byte) ([]byte, string, error) {
	img, _, err := image.Decode(bytes.NewReader(content))
	if err != nil {
		return nil, "", fmt.Errorf("decode image: %w", err)
	}
	mediaType := imageMediaType(path)
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".jpg", ".jpeg":
		var out bytes.Buffer
		if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 92}); err != nil {
			return nil, "", fmt.Errorf("encode as JPEG: %w", err)
		}
		return out.Bytes(), mediaType, nil
	case ".png":
		var out bytes.Buffer
		if err := png.Encode(&out, img); err != nil {
			return nil, "", fmt.Errorf("encode as PNG: %w", err)
		}
		return out.Bytes(), mediaType, nil
	case ".gif", ".webp":
		// Preserve original bytes (GIF animation, WebP) after validating decode.
		return content, mediaType, nil
	default:
		var out bytes.Buffer
		if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 92}); err != nil {
			return nil, "", fmt.Errorf("encode as JPEG: %w", err)
		}
		return out.Bytes(), "image/jpeg", nil
	}
}

// audioToPCM16k converts audio file content to PCM 16kHz mono (s16le) using ffmpeg.
func audioToPCM16k(ctx context.Context, content []byte) ([]byte, error) {
	tmp, err := os.CreateTemp("", "agent-cli-audio-*")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close temp file: %w", err)
	}

	// ffmpeg -i input -f s16le -ac 1 -ar 16000 - (stdout = raw PCM 16kHz mono)
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y", "-i", tmpPath, "-f", "s16le", "-ac", "1", "-ar", "16000", "-")
	cmd.Stdin = nil
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg convert to PCM 16kHz: %w (stderr: %s)", err, stderr.String())
	}
	return stdout.Bytes(), nil
}

type ReadFileTool struct {
	fs fileSystem
}

func NewReadFileTool(workspace string, restrict bool) *ReadFileTool {
	var fs fileSystem
	if restrict {
		fs = &sandboxFs{workspace: workspace}
	} else {
		fs = &hostFs{}
	}
	return &ReadFileTool{fs: fs}
}

func (t *ReadFileTool) Name() string {
	return "read_file"
}

func (t *ReadFileTool) Description() string {
	return "Read the contents of a file, can be an image, audio, text, video, etc. It transforms the context into the relevant latent representation for model parsing when possible."
}

func (t *ReadFileTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file to read",
			},
		},
		"required": []string{"path"},
	}
}

func (t *ReadFileTool) Execute(ctx context.Context, args map[string]any) ([]messages.Message, error) {
	path, ok := args["path"].(string)
	if !ok {
		return ErrorAsToolMessage(fmt.Errorf("path is required"))
	}

	content, err := t.fs.ReadFile(path)
	if err != nil {
		return ErrorAsToolMessage(err)
	}

	kind := mediaKindFromPath(path)
	switch kind {
	case mediaImage:
		imgBytes, mediaType, err := imageToNative(path, content)
		if err != nil {
			return ErrorAsToolMessage(fmt.Errorf("read image: %w", err))
		}
		msg := messages.Message{
			Role:         messages.RoleTool,
			ContentParts: []messages.ContentPart{messages.ImagePart{Bytes: imgBytes, MediaType: mediaType}},
		}
		return []messages.Message{msg}, nil
	case mediaVideo:
		mediaType := videoMediaType(path)
		msg := messages.Message{
			Role:         messages.RoleTool,
			ContentParts: []messages.ContentPart{messages.VideoPart{Bytes: content, MediaType: mediaType}},
		}
		return []messages.Message{msg}, nil
	case mediaAudio:
		pcmBytes, err := audioToPCM16k(ctx, content)
		if err != nil {
			return ErrorAsToolMessage(fmt.Errorf("read audio: %w", err))
		}
		msg := messages.Message{
			Role:         messages.RoleTool,
			ContentParts: []messages.ContentPart{messages.AudioPart{Bytes: pcmBytes, MediaType: "audio/pcm"}},
		}
		return []messages.Message{msg}, nil
	default:
		return []messages.Message{messages.NewTextMessage(messages.RoleTool, string(content))}, nil
	}
}

type WriteFileTool struct {
	fs fileSystem
}

func NewWriteFileTool(workspace string, restrict bool) *WriteFileTool {
	var fs fileSystem
	if restrict {
		fs = &sandboxFs{workspace: workspace}
	} else {
		fs = &hostFs{}
	}
	return &WriteFileTool{fs: fs}
}

func (t *WriteFileTool) Name() string {
	return "write_file"
}

func (t *WriteFileTool) Description() string {
	return "Write content to a file"
}

func (t *WriteFileTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file to write",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Content to write to the file",
			},
		},
		"required": []string{"path", "content"},
	}
}

func (t *WriteFileTool) Execute(ctx context.Context, args map[string]any) ([]messages.Message, error) {
	path, ok := args["path"].(string)
	if !ok {
		return ErrorAsToolMessage(fmt.Errorf("path is required"))
	}

	content, ok := args["content"].(string)
	if !ok {
		return ErrorAsToolMessage(fmt.Errorf("content is required"))
	}

	if err := t.fs.WriteFile(path, []byte(content)); err != nil {
		return ErrorAsToolMessage(err)
	}
	return []messages.Message{messages.NewTextMessage(messages.RoleTool, fmt.Sprintf("File written: %s", path))}, nil
}

type ListDirTool struct {
	fs fileSystem
}

func NewListDirTool(workspace string, restrict bool) *ListDirTool {
	var fs fileSystem
	if restrict {
		fs = &sandboxFs{workspace: workspace}
	} else {
		fs = &hostFs{}
	}
	return &ListDirTool{fs: fs}
}

func (t *ListDirTool) Name() string {
	return "list_dir"
}

func (t *ListDirTool) Description() string {
	return "List files and directories in a path"
}

func (t *ListDirTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to list",
			},
		},
		"required": []string{"path"},
	}
}

func (t *ListDirTool) Execute(ctx context.Context, args map[string]any) ([]messages.Message, error) {
	path, ok := args["path"].(string)
	if !ok {
		path = "."
	}

	entries, err := t.fs.ReadDir(path)
	if err != nil {
		return ErrorAsToolMessage(fmt.Errorf("failed to read directory: %w", err))
	}
	formatted := formatDirEntries(entries)
	return []messages.Message{messages.NewTextMessage(messages.RoleTool, formatted)}, nil
}

func formatDirEntries(entries []os.DirEntry) string {
	var result strings.Builder
	for _, entry := range entries {
		if entry.IsDir() {
			result.WriteString("DIR:  " + entry.Name() + "\n")
		} else {
			result.WriteString("FILE: " + entry.Name() + "\n")
		}
	}
	return result.String()
}

// fileSystem abstracts reading, writing, and listing files, allowing both
// unrestricted (host filesystem) and sandbox (os.Root) implementations to share the same polymorphic interface.
type fileSystem interface {
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte) error
	ReadDir(path string) ([]os.DirEntry, error)
}

// hostFs is an unrestricted fileReadWriter that operates directly on the host filesystem.
type hostFs struct{}

func (h *hostFs) ReadFile(path string) ([]byte, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to read file: file not found: %w", err)
		}
		if os.IsPermission(err) {
			return nil, fmt.Errorf("failed to read file: access denied: %w", err)
		}
		return nil, fmt.Errorf("failed to read file: %w", err)
	}
	return content, nil
}

func (h *hostFs) ReadDir(path string) ([]os.DirEntry, error) {
	return os.ReadDir(path)
}

func (h *hostFs) WriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create parent directories: %w", err)
	}

	// We use a "write-then-rename" pattern here to ensure an atomic write.
	// This prevents the target file from being left in a truncated or partial state
	// if the operation is interrupted, as the rename operation is atomic on Linux.
	tmpPath := fmt.Sprintf("%s.%d.tmp", path, time.Now().UnixNano())
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		_ = os.Remove(tmpPath) // Ensure cleanup of partial/empty temp file
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to replace original file: %w", err)
	}
	return nil
}

// sandboxFs is a sandboxed fileSystem that operates within a strictly defined workspace using os.Root.
type sandboxFs struct {
	workspace string
}

func (r *sandboxFs) execute(path string, fn func(root *os.Root, relPath string) error) error {
	if r.workspace == "" {
		return fmt.Errorf("workspace is not defined")
	}

	root, err := os.OpenRoot(r.workspace)
	if err != nil {
		return fmt.Errorf("failed to open workspace: %w", err)
	}
	defer func() {
		_ = root.Close()
	}()

	relPath, err := getSafeRelPath(r.workspace, path)
	if err != nil {
		return err
	}

	return fn(root, relPath)
}

func (r *sandboxFs) ReadFile(path string) ([]byte, error) {
	var content []byte
	err := r.execute(path, func(root *os.Root, relPath string) error {
		fileContent, err := os.ReadFile(relPath)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("failed to read file: file not found: %w", err)
			}
			// os.Root returns "escapes from parent" for paths outside the root
			if os.IsPermission(err) || strings.Contains(err.Error(), "escapes from parent") ||
				strings.Contains(err.Error(), "permission denied") {
				return fmt.Errorf("failed to read file: access denied: %w", err)
			}
			return fmt.Errorf("failed to read file: %w", err)
		}
		content = fileContent
		return nil
	})
	return content, err
}

func (r *sandboxFs) WriteFile(path string, data []byte) error {
	return r.execute(path, func(root *os.Root, relPath string) error {
		dir := filepath.Dir(relPath)
		if dir != "." && dir != "/" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("failed to create parent directories: %w", err)
			}
		}

		// We use a "write-then-rename" pattern here to ensure an atomic write.
		// This prevents the target file from being left in a truncated or partial state
		// if the operation is interrupted, as the rename operation is atomic on Linux.
		tmpRelPath := fmt.Sprintf("%s.%d.tmp", relPath, time.Now().UnixNano())

		if err := os.WriteFile(tmpRelPath, data, 0o644); err != nil {
			_ = root.Remove(tmpRelPath) // Ensure cleanup of partial/empty temp file
			return fmt.Errorf("failed to write to temp file: %w", err)
		}

		if err := os.Rename(tmpRelPath, relPath); err != nil {
			_ = root.Remove(tmpRelPath)
			return fmt.Errorf("failed to rename temp file over target: %w", err)
		}
		return nil
	})
}

func (r *sandboxFs) ReadDir(path string) ([]os.DirEntry, error) {
	var entries []os.DirEntry
	err := r.execute(path, func(root *os.Root, relPath string) error {
		dirEntries, err := fs.ReadDir(root.FS(), relPath)
		if err != nil {
			return err
		}
		entries = dirEntries
		return nil
	})
	return entries, err
}

// Helper to get a safe relative path for os.Root usage
func getSafeRelPath(workspace, path string) (string, error) {
	if workspace == "" {
		return "", fmt.Errorf("workspace is not defined")
	}

	rel := filepath.Clean(path)
	if filepath.IsAbs(rel) {
		var err error
		rel, err = filepath.Rel(workspace, rel)
		if err != nil {
			return "", fmt.Errorf("failed to calculate relative path: %w", err)
		}
	}

	if !filepath.IsLocal(rel) {
		return "", fmt.Errorf("path escapes workspace: %s", path)
	}

	return rel, nil
}
