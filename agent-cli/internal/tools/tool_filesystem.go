package tools

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
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

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	_ "golang.org/x/image/webp"
)

const (
	// writeFileTempPrefix is deliberately independent of the destination
	// basename. Appending an internal suffix to the destination can make a
	// legal maximum-length filename fail before the rename.
	writeFileTempPrefix      = ".w-"
	writeFileTempRandomBytes = 4
	writeFileTempCreateTries = 100
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
	defer func() { _ = os.Remove(tmpPath) }()

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
	return &ReadFileTool{fs: newLegacyFileSystem(workspace, restrict)}
}

// NewReadFileToolWithPolicy constructs a read tool confined to the supplied
// filesystem policy.
func NewReadFileToolWithPolicy(policy *FilesystemPolicy) *ReadFileTool {
	return &ReadFileTool{fs: newSandboxFs(policy)}
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
	return &WriteFileTool{fs: newLegacyFileSystem(workspace, restrict)}
}

// NewWriteFileToolWithPolicy constructs a write tool confined to the supplied
// filesystem policy.
func NewWriteFileToolWithPolicy(policy *FilesystemPolicy) *WriteFileTool {
	return &WriteFileTool{fs: newSandboxFs(policy)}
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
	return &ListDirTool{fs: newLegacyFileSystem(workspace, restrict)}
}

// NewListDirToolWithPolicy constructs a directory-list tool confined to the
// supplied filesystem policy.
func NewListDirToolWithPolicy(policy *FilesystemPolicy) *ListDirTool {
	return &ListDirTool{fs: newSandboxFs(policy)}
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

func newLegacyFileSystem(workspace string, restrict bool) fileSystem {
	if restrict {
		return &sandboxFs{
			workspace:          workspace,
			protectedReadRoots: normalizeProtectedReadRoots(platformProtectedReadRoots()),
		}
	}
	return &hostFs{}
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

	// Preserve the existing invalid-target error classification before creating
	// a temporary file. A NUL byte, for example, is rejected by the OS only
	// when the path is used, and should not leave a staging artifact behind.
	if _, err := os.Lstat(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	// Write to a short, unique file in the destination directory, then rename
	// it over the target. Keeping the temporary name independent of path means
	// the staging write does not consume any of the target's filename budget.
	tmpFile, tmpPath, err := createHostWriteTempFile(dir)
	if err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	defer func() { _ = os.Remove(tmpPath) }() // clean up on write/close/rename failure

	if err := writeAndCloseTempFile(tmpFile, data); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to replace original file: %w", err)
	}
	return nil
}

func newWriteFileTempName() (string, error) {
	var token [writeFileTempRandomBytes]byte
	if _, err := cryptorand.Read(token[:]); err != nil {
		return "", fmt.Errorf("generate temporary filename: %w", err)
	}
	return writeFileTempPrefix + hex.EncodeToString(token[:]), nil
}

func createHostWriteTempFile(dir string) (*os.File, string, error) {
	for attempt := 0; attempt < writeFileTempCreateTries; attempt++ {
		name, err := newWriteFileTempName()
		if err != nil {
			return nil, "", err
		}
		path := filepath.Join(dir, name)
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			return file, path, nil
		}
		if os.IsExist(err) {
			continue
		}
		return nil, "", err
	}
	return nil, "", fmt.Errorf("could not allocate a unique temporary filename after %d attempts", writeFileTempCreateTries)
}

func writeAndCloseTempFile(file *os.File, data []byte) error {
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	return nil
}

func validateSandboxWriteTarget(root *os.Root, path string) error {
	if _, err := root.Lstat(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to write to temp file: %w", err)
	}
	return nil
}

func createSandboxWriteTempFile(root *os.Root, dir string) (*os.File, string, error) {
	for attempt := 0; attempt < writeFileTempCreateTries; attempt++ {
		name, err := newWriteFileTempName()
		if err != nil {
			return nil, "", err
		}
		relPath := filepath.Join(dir, name)
		file, err := root.OpenFile(relPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			return file, relPath, nil
		}
		if os.IsExist(err) {
			continue
		}
		return nil, "", err
	}
	return nil, "", fmt.Errorf("could not allocate a unique temporary filename after %d attempts", writeFileTempCreateTries)
}

// sandboxFs is a sandboxed fileSystem that operates within a strictly defined workspace using os.Root.
type sandboxFs struct {
	workspace            string
	additionalWorkspaces []string
	protectedReadRoots   []string
	enforceCanonical     bool
}

func (r *sandboxFs) execute(path string, fn func(root *os.Root, relPath string) error) error {
	rootPath, relPath, err := r.resolve(path)
	if err != nil {
		return err
	}

	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return fmt.Errorf("failed to open workspace: %w", err)
	}
	defer func() { _ = root.Close() }()

	return fn(root, relPath)
}

func newSandboxFs(policy *FilesystemPolicy) *sandboxFs {
	if policy == nil {
		return &sandboxFs{}
	}
	roots := policy.WritableRoots()
	if len(roots) == 0 {
		return &sandboxFs{}
	}
	return &sandboxFs{
		workspace:            roots[0],
		additionalWorkspaces: append([]string(nil), roots[1:]...),
		protectedReadRoots:   policy.ProtectedReadRoots(),
		enforceCanonical:     true,
	}
}

func (r *sandboxFs) executeRead(path string, fn func(root *os.Root, relPath string) error) error {
	rootPath, relPath, err := r.resolveRead(path)
	if err != nil {
		return err
	}

	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return fmt.Errorf("failed to open workspace: %w", err)
	}
	defer func() { _ = root.Close() }()

	return fn(root, relPath)
}

func (r *sandboxFs) resolve(path string) (string, string, error) {
	roots, err := r.rootPaths()
	if err != nil {
		return "", "", err
	}

	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(roots[0], candidate)
	}
	candidate = filepath.Clean(candidate)
	comparisonCandidate, err := canonicalizeExistingPath(candidate)
	if err != nil {
		// A path that cannot be canonicalized is not safe to authorize. In
		// particular, do not fall back to a lexical check when an existing
		// symlink or ancestor cannot be resolved: the root operation would be
		// making the authorization decision after the check.
		return "", "", fmt.Errorf("%w: unable to resolve requested path", ErrFilesystemAccessDenied)
	}
	for _, rootPath := range roots {
		relPath, err := filepath.Rel(rootPath, comparisonCandidate)
		if err != nil {
			return "", "", fmt.Errorf("failed to calculate relative path: %w", err)
		}
		if filepath.IsLocal(relPath) {
			return rootPath, relPath, nil
		}
	}
	// A legacy restricted tool may be given a root that has already been
	// removed. Preserve its historical open-root diagnostic rather than
	// misclassifying the missing root as an escaping symlink. Validated
	// FilesystemPolicy roots always exist, so this fallback cannot widen a
	// policy-backed customer surface.
	for _, rootPath := range roots {
		if _, statErr := os.Stat(rootPath); statErr != nil && os.IsNotExist(statErr) {
			relPath, relErr := filepath.Rel(rootPath, candidate)
			if relErr != nil {
				return "", "", fmt.Errorf("failed to calculate relative path: %w", relErr)
			}
			if filepath.IsLocal(relPath) {
				return rootPath, relPath, nil
			}
		}
	}
	for _, rootPath := range roots {
		relPath, relErr := filepath.Rel(rootPath, candidate)
		if relErr != nil {
			return "", "", fmt.Errorf("failed to calculate relative path: %w", relErr)
		}
		if filepath.IsLocal(relPath) {
			if !r.enforceCanonical {
				// Legacy restricted constructors predate FilesystemPolicy and
				// retain their os.Root-based symlink enforcement and diagnostics.
				return rootPath, relPath, nil
			}
			// The lexical path is beneath a permitted root, but its canonical
			// target is not. This is the symlink-specific refusal and is
			// wrapped so callers can distinguish it without relying on text.
			return "", "", newFilesystemAccessDenied(fmt.Sprintf("path escapes workspace: %s: %s", path, ErrFilesystemAccessDenied))
		}
	}
	// Preserve the long-standing diagnostic for an ordinary absolute or
	// traversal path that was never lexically beneath a configured root.
	return "", "", newFilesystemAccessDenied(fmt.Sprintf("path escapes workspace: %s", path))
}

func (r *sandboxFs) resolveRead(path string) (string, string, error) {
	if r.isProtectedRead(path) {
		return "", "", fmt.Errorf("%w: %w", ErrFilesystemAccessDenied, ErrProtectedFilesystemRead)
	}
	return r.resolve(path)
}

func (r *sandboxFs) authorizeRead(path string) error {
	_, _, err := r.resolveRead(path)
	return err
}

func (r *sandboxFs) isProtectedRead(path string) bool {
	roots, err := r.rootPaths()
	if err != nil || len(roots) == 0 {
		return false
	}
	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(roots[0], candidate)
	}
	candidate = filepath.Clean(candidate)
	comparisonCandidate := candidate
	if resolved, err := canonicalizeExistingPath(candidate); err == nil {
		comparisonCandidate = resolved
	}
	protectedRoots := r.protectedReadRoots
	if len(protectedRoots) == 0 {
		protectedRoots = normalizeProtectedReadRoots(platformProtectedReadRoots())
	}
	for _, protectedRoot := range protectedRoots {
		if isWithinWorkspace(candidate, protectedRoot) || isWithinWorkspace(comparisonCandidate, protectedRoot) {
			return true
		}
	}
	return false
}

// canonicalizeExistingPath resolves the existing portion of a path and then
// appends its missing descendants. This keeps lexical containment comparisons
// correct when the platform exposes the same directory through a symlink
// alias (for example /var versus /private/var on macOS).
func canonicalizeExistingPath(path string) (string, error) {
	// NUL is not a valid filesystem path component. Leave it for the actual
	// os.Root operation to report so legacy tools retain their precise invalid
	// path diagnostic; no valid symlink can be hidden behind a NUL component.
	if strings.ContainsRune(path, '\x00') {
		return filepath.Clean(path), nil
	}
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	return canonicalizePathWithMissing(absolute, make(map[string]struct{}))
}

// canonicalizePathWithMissing resolves every existing path component,
// including dangling symlinks, and then appends any missing descendants. A
// plain EvalSymlinks call cannot resolve a dangling link, which would
// otherwise make a link to a future outside target look like an ordinary new
// in-root file.
func canonicalizePathWithMissing(path string, seen map[string]struct{}) (string, error) {
	current := filepath.Clean(path)
	missing := make([]string, 0)
	for {
		info, err := os.Lstat(current)
		if err == nil {
			resolved, resolveErr := canonicalizeExistingNode(current, info, seen)
			if resolveErr != nil {
				return "", resolveErr
			}
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func canonicalizeExistingNode(path string, info os.FileInfo, seen map[string]struct{}) (string, error) {
	if info.Mode()&os.ModeSymlink != 0 {
		path = filepath.Clean(path)
		if _, exists := seen[path]; exists {
			return "", fmt.Errorf("resolve symlink %q: too many levels of symbolic links", path)
		}
		seen[path] = struct{}{}
		target, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		return canonicalizePathWithMissing(target, seen)
	}

	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Abs(resolved)
}

func (r *sandboxFs) rootPaths() ([]string, error) {
	if r == nil || strings.TrimSpace(r.workspace) == "" {
		return nil, fmt.Errorf("workspace is not defined")
	}
	rawRoots := make([]string, 0, 1+len(r.additionalWorkspaces))
	rawRoots = append(rawRoots, r.workspace)
	rawRoots = append(rawRoots, r.additionalWorkspaces...)
	roots := make([]string, 0, len(rawRoots))
	for _, rawRoot := range rawRoots {
		rootPath, err := filepath.Abs(rawRoot)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve workspace path: %w", err)
		}
		roots = append(roots, filepath.Clean(rootPath))
	}
	return roots, nil
}

func isSandboxAccessDenied(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, fs.ErrPermission) || os.IsPermission(err) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "escapes from parent") ||
		strings.Contains(message, "outside root") ||
		strings.Contains(message, "outside of root") ||
		strings.Contains(message, "cross-device link")
}

func (r *sandboxFs) ReadFile(path string) ([]byte, error) {
	var content []byte
	err := r.executeRead(path, func(root *os.Root, relPath string) error {
		fileContent, err := root.ReadFile(relPath)
		if err != nil {
			if os.IsNotExist(err) || errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("failed to read file: file not found: %w", err)
			}
			if isSandboxAccessDenied(err) {
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
		// Stat the target before creating the temporary file. Besides keeping
		// authorization ahead of side effects, this makes an existing symlink
		// to an external target fail closed instead of being replaced by an
		// otherwise-safe rename.
		if _, err := root.Stat(relPath); err != nil && !os.IsNotExist(err) && !errors.Is(err, fs.ErrNotExist) {
			if isSandboxAccessDenied(err) {
				return fmt.Errorf("failed to authorize file: access denied: %w", err)
			}
		} else if err == nil {
			if info, lstatErr := root.Lstat(relPath); lstatErr == nil && info.Mode()&os.ModeSymlink != 0 {
				if err := root.WriteFile(relPath, data, 0o644); err != nil {
					if isSandboxAccessDenied(err) {
						return fmt.Errorf("failed to write file: access denied: %w", err)
					}
					return fmt.Errorf("failed to write file: %w", err)
				}
				return nil
			}
		}

		dir := filepath.Dir(relPath)
		if dir != "." && dir != "/" {
			if err := root.MkdirAll(dir, 0o755); err != nil {
				if isSandboxAccessDenied(err) {
					return fmt.Errorf("failed to create parent directories: access denied: %w", err)
				}
				return fmt.Errorf("failed to create parent directories: %w", err)
			}
		}

		if err := validateSandboxWriteTarget(root, relPath); err != nil {
			return err
		}

		// Keep the staging file short and in the destination directory. The
		// root-owned operations preserve workspace confinement while retaining
		// write-then-rename atomicity.
		tmpFile, tmpRelPath, err := createSandboxWriteTempFile(root, dir)
		if err != nil {
			return fmt.Errorf("failed to write to temp file: %w", err)
		}
		defer func() { _ = root.Remove(tmpRelPath) }() // clean up on failure

		if err := writeAndCloseTempFile(tmpFile, data); err != nil {
			return fmt.Errorf("failed to write to temp file: %w", err)
		}

		if err := root.Rename(tmpRelPath, relPath); err != nil {
			_ = root.Remove(tmpRelPath)
			if isSandboxAccessDenied(err) {
				return fmt.Errorf("failed to rename temp file over target: access denied: %w", err)
			}
			return fmt.Errorf("failed to rename temp file over target: %w", err)
		}
		return nil
	})
}

func (r *sandboxFs) ReadDir(path string) ([]os.DirEntry, error) {
	var entries []os.DirEntry
	err := r.executeRead(path, func(root *os.Root, relPath string) error {
		dirEntries, err := fs.ReadDir(root.FS(), filepath.ToSlash(relPath))
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
