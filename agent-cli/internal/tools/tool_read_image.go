package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const ReadImageToolID = "read_image"

var (
	// ErrReadImagePreparerUnavailable means the tool was used outside a session
	// that supplied the provider/model-aware image preparation seam.
	ErrReadImagePreparerUnavailable = errors.New("read_image session image preparer is not configured")
	// ErrReadImageInvalidResult protects the agent loop from a custom preparer
	// accidentally turning a successful read into an empty or non-image result.
	ErrReadImageInvalidResult = errors.New("read_image preparer returned invalid image content")
)

// ImagePartPreparer is the session-owned image preparation seam. The session
// supplies the implementation so this tool does not duplicate capability or
// MIME policy from services.PrepareSessionImageParts.
type ImagePartPreparer func(paths []string) ([]messages.ImagePart, error)

// SessionImagePreparerBinder is implemented by registry-backed executors that
// can produce a session-isolated executor with a provider-aware image
// preparer. Binding returns a new executor and never mutates the shared
// registry or another session's executor.
type SessionImagePreparerBinder interface {
	WithSessionImagePreparer(ImagePartPreparer) messages.ToolExecutor
}

// ReadImageTool attaches one validated local image as rich tool-result content.
// A nil preparer is intentional for the process-wide/default registry: only a
// session knows which provider/model capabilities should govern the read.
type ReadImageTool struct {
	preparer ImagePartPreparer
}

func NewReadImageTool(preparer ImagePartPreparer) *ReadImageTool {
	return &ReadImageTool{preparer: preparer}
}

func (t *ReadImageTool) withSessionImagePreparer(preparer ImagePartPreparer) *ReadImageTool {
	return &ReadImageTool{preparer: preparer}
}

func (t *ReadImageTool) Name() string { return ReadImageToolID }

func (t *ReadImageTool) Description() string {
	return "Read and attach a validated local image so the model can inspect its pixels"
}

func (t *ReadImageTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the local image to attach",
			},
		},
		"required": []string{"path"},
	}
}

func (t *ReadImageTool) Execute(_ context.Context, args map[string]any) ([]messages.Message, error) {
	path, ok := args["path"].(string)
	if !ok {
		return ErrorAsToolMessage(fmt.Errorf("path is required"))
	}
	if strings.TrimSpace(path) == "" {
		return ErrorAsToolMessage(fmt.Errorf("path must not be empty"))
	}
	if t == nil || t.preparer == nil {
		return ErrorAsToolMessage(ErrReadImagePreparerUnavailable)
	}

	parts, err := t.preparer([]string{path})
	if err != nil {
		return ErrorAsToolMessage(err)
	}
	if len(parts) != 1 || len(parts[0].Bytes) == 0 || !strings.HasPrefix(strings.ToLower(parts[0].MediaType), "image/") {
		return ErrorAsToolMessage(fmt.Errorf("%w: want exactly one non-empty image part", ErrReadImageInvalidResult))
	}
	part := parts[0]
	part.Bytes = append([]byte(nil), part.Bytes...)

	return []messages.Message{{
		Role:         messages.RoleTool,
		ContentParts: []messages.ContentPart{part},
	}}, nil
}
