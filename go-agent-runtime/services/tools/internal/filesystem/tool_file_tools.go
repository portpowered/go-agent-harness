package filesystem

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	core "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal"
)

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
		return core.ErrorAsToolMessage(fmt.Errorf("path is required"))
	}

	content, err := t.fs.ReadFile(path)
	if err != nil {
		return filesystemErrorAsToolMessage(t.fs, t.Name(), path, err)
	}

	kind := mediaKindFromPath(path)
	switch kind {
	case mediaText:
		return []messages.Message{messages.NewTextMessage(messages.RoleTool, string(content))}, nil
	case mediaImage:
		imgBytes, mediaType, err := imageToNative(path, content)
		if err != nil {
			return core.ErrorAsToolMessage(fmt.Errorf("read image: %w", err))
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
			return core.ErrorAsToolMessage(fmt.Errorf("read audio: %w", err))
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
		return core.ErrorAsToolMessage(fmt.Errorf("path is required"))
	}

	content, ok := args["content"].(string)
	if !ok {
		return core.ErrorAsToolMessage(fmt.Errorf("content is required"))
	}

	if err := t.fs.WriteFile(path, []byte(content)); err != nil {
		return filesystemErrorAsToolMessage(t.fs, t.Name(), path, err)
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
		return filesystemErrorAsToolMessage(t.fs, t.Name(), path, fmt.Errorf("failed to read directory: %w", err))
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
