package filesystem

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	_ "golang.org/x/image/webp"
)

const (
	workspaceUndefinedMessage = "workspace is not defined"
	imageJPEGMediaType        = "image/jpeg"
	imagePNGMediaType         = "image/png"
	imageJPEGExtension        = ".jpeg"
	imageJPGExtension         = ".jpg"
	imagePNGExtension         = ".png"
	imageWebPExtension        = ".webp"
	imageGIFExtension         = ".gif"
	windowsPlatform           = "windows"
	filesystemJPEGQuality     = 92
	sandboxDirectoryMode      = 0o755
	sandboxFileMode           = 0o644

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
		return path, fmt.Errorf("%s", workspaceUndefinedMessage)
	}
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("failed to resolve workspace path: %w", err)
	}
	absPath, err := absolutePath(path, absWorkspace)
	if err != nil {
		return "", err
	}
	if restrict {
		if err := validatePathScope(absPath, absWorkspace); err != nil {
			return "", err
		}
	}
	return absPath, nil
}

func absolutePath(path, workspace string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	absPath, err := filepath.Abs(filepath.Join(workspace, path))
	if err != nil {
		return "", fmt.Errorf("failed to resolve file path: %w", err)
	}
	return absPath, nil
}

func validatePathScope(absPath, absWorkspace string) error {
	if !isWithinWorkspace(absPath, absWorkspace) {
		return fmt.Errorf("access denied: path is outside the workspace")
	}
	workspaceReal := resolvedWorkspacePath(absWorkspace)
	resolved, err := filepath.EvalSymlinks(absPath)
	if err == nil {
		if !isWithinWorkspace(resolved, workspaceReal) {
			return fmt.Errorf("access denied: symlink resolves outside workspace")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("failed to resolve path: %w", err)
	}
	parentResolved, parentErr := resolveExistingAncestor(filepath.Dir(absPath))
	if parentErr == nil {
		if !isWithinWorkspace(parentResolved, workspaceReal) {
			return fmt.Errorf("access denied: symlink resolves outside workspace")
		}
		return nil
	}
	if os.IsNotExist(parentErr) {
		return nil
	}
	return fmt.Errorf("failed to resolve path: %w", parentErr)
}

func resolvedWorkspacePath(workspace string) string {
	if resolved, err := filepath.EvalSymlinks(workspace); err == nil {
		return resolved
	}
	return workspace
}

// ValidatePath preserves the legacy shell and host adapter path check while
// keeping the implementation in the filesystem owner package.
func ValidatePath(path, workspace string, restrict bool) (string, error) {
	return validatePath(path, workspace, restrict)
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
	case imageJPGExtension, imageJPEGExtension, imagePNGExtension, imageGIFExtension, imageWebPExtension:
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
	case imageJPGExtension, imageJPEGExtension:
		return imageJPEGMediaType
	case imagePNGExtension:
		return imagePNGMediaType
	case imageGIFExtension:
		return "image/gif"
	case imageWebPExtension:
		return "image/webp"
	default:
		return imageJPEGMediaType
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
	case imageJPGExtension, imageJPEGExtension:
		var out bytes.Buffer
		if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: filesystemJPEGQuality}); err != nil {
			return nil, "", fmt.Errorf("encode as JPEG: %w", err)
		}
		return out.Bytes(), mediaType, nil
	case imagePNGExtension:
		var out bytes.Buffer
		if err := png.Encode(&out, img); err != nil {
			return nil, "", fmt.Errorf("encode as PNG: %w", err)
		}
		return out.Bytes(), mediaType, nil
	case imageGIFExtension, imageWebPExtension:
		// Preserve original bytes (GIF animation, WebP) after validating decode.
		return content, mediaType, nil
	default:
		var out bytes.Buffer
		if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: filesystemJPEGQuality}); err != nil {
			return nil, "", fmt.Errorf("encode as JPEG: %w", err)
		}
		return out.Bytes(), imageJPEGMediaType, nil
	}
}

// audioToPCM16k converts audio file content to PCM 16kHz mono (s16le) using ffmpeg.
func audioToPCM16k(ctx context.Context, content []byte) ([]byte, error) {
	tmp, err := os.CreateTemp("", "agent-cli-audio-*")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer removeFileIfPresent(tmpPath)

	if _, err := tmp.Write(content); err != nil {
		if closeErr := tmp.Close(); closeErr != nil {
			return nil, fmt.Errorf("write temp file: %w (close temp file: %w)", err, closeErr)
		}
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
