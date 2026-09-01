package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"mime"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const ReadImageToolID = "read_image"

const (
	// Version 2 is the compact envelope contract. Version 1 included a data
	// URL, so changing its meaning would let an older consumer silently retain
	// the unbounded representation this contract is meant to remove.
	ReadImageResultVersion                   = 2
	ReadImageResultStatusSuccess             = "success"
	ReadImageResultStatusError               = "error"
	ReadImageResultTypedProjectionInputImage = "input_image"
)

// ReadImageResult is the provider-neutral, versioned text representation of a
// read_image result. A successful result carries metadata and a fixed marker
// for the correlated typed ImagePart; the exact bytes are exposed only through
// that rich part. Error results intentionally omit all success-only fields.
type ReadImageResult struct {
	Version         int                `json:"version"`
	Status          string             `json:"status"`
	MIMEType        string             `json:"mime_type,omitempty"`
	ByteLength      int                `json:"byte_length,omitempty"`
	SHA256          string             `json:"sha256,omitempty"`
	TypedProjection string             `json:"typed_projection,omitempty"`
	Error           string             `json:"error,omitempty"`
	Refusal         *FilesystemRefusal `json:"refusal,omitempty"`
}

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
	preparer       ImagePartPreparer
	policy         *FilesystemPolicy
	policyRequired bool
}

func NewReadImageTool(preparer ImagePartPreparer) *ReadImageTool {
	return &ReadImageTool{preparer: preparer}
}

// NewReadImageToolWithPolicy constructs a read_image tool that authorizes the
// requested path against the supplied filesystem policy before invoking the
// session-owned image preparer. The optional preparer keeps construction
// compatible with callers that bind the provider-aware preparer later.
func NewReadImageToolWithPolicy(policy *FilesystemPolicy, preparer ...ImagePartPreparer) *ReadImageTool {
	var imagePreparer ImagePartPreparer
	if len(preparer) > 0 {
		imagePreparer = preparer[0]
	}
	return &ReadImageTool{preparer: imagePreparer, policy: policy, policyRequired: true}
}

func (t *ReadImageTool) withSessionImagePreparer(preparer ImagePartPreparer) *ReadImageTool {
	if t == nil {
		return &ReadImageTool{preparer: preparer}
	}
	return &ReadImageTool{preparer: preparer, policy: t.policy, policyRequired: t.policyRequired}
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
		return readImageErrorMessage(fmt.Errorf("path is required"))
	}
	if strings.TrimSpace(path) == "" {
		return readImageErrorMessage(fmt.Errorf("path must not be empty"))
	}
	if t == nil {
		return readImageErrorMessage(ErrReadImagePreparerUnavailable)
	}
	if t.policyRequired {
		var err error
		if t.policy == nil {
			err = newFilesystemAccessDeniedWithContext("", FilesystemRefusalInvalidScope, ErrInvalidFilesystemRoot.Error())
		} else {
			err = t.policy.AuthorizeRead(path)
		}
		if err != nil {
			return readImageErrorMessageForPath(path, err)
		}
	}
	if t.preparer == nil {
		return readImageErrorMessage(ErrReadImagePreparerUnavailable)
	}

	parts, err := t.preparer([]string{path})
	if err != nil {
		return readImageErrorMessage(err)
	}
	if len(parts) != 1 || len(parts[0].Bytes) == 0 || !strings.HasPrefix(strings.ToLower(parts[0].MediaType), "image/") {
		return readImageErrorMessage(fmt.Errorf("%w: want exactly one non-empty image part", ErrReadImageInvalidResult))
	}
	part := parts[0]
	imageBytes := append([]byte(nil), part.Bytes...)
	mediaType, err := validateReadImagePart(imageBytes, part.MediaType)
	if err != nil {
		return readImageErrorMessage(fmt.Errorf("%w: %v", ErrReadImageInvalidResult, err))
	}

	digest := sha256.Sum256(imageBytes)
	result := ReadImageResult{
		Version:         ReadImageResultVersion,
		Status:          ReadImageResultStatusSuccess,
		MIMEType:        mediaType,
		ByteLength:      len(imageBytes),
		SHA256:          hex.EncodeToString(digest[:]),
		TypedProjection: ReadImageResultTypedProjectionInputImage,
	}
	encoded, _ := json.Marshal(result)

	return []messages.Message{{
		Role: messages.RoleTool,
		ContentParts: []messages.ContentPart{
			messages.TextPart{Text: string(encoded)},
			// The provider-facing image representation and the envelope are
			// derived from the same owned byte snapshot. Do not hand the
			// preparer's mutable slice to the next stage.
			messages.ImagePart{Bytes: append([]byte(nil), imageBytes...), MediaType: mediaType},
		},
	}}, nil
}

func readImageErrorMessage(err error) ([]messages.Message, error) {
	return readImageErrorMessageForPath("", err)
}

func readImageErrorMessageForPath(path string, err error) ([]messages.Message, error) {
	if err == nil {
		err = ErrReadImageInvalidResult
	}
	result := ReadImageResult{
		Version: ReadImageResultVersion,
		Status:  ReadImageResultStatusError,
		Error:   err.Error(),
	}
	if refusal, ok := filesystemRefusalFor(ReadImageToolID, path, nil, err); ok {
		result.Refusal = &refusal
	}
	encoded, _ := json.Marshal(result)
	return []messages.Message{messages.NewTextMessage(messages.RoleTool, string(encoded))}, nil
}

func validateReadImagePart(data []byte, rawMediaType string) (string, error) {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(rawMediaType))
	if err != nil {
		return "", fmt.Errorf("invalid image MIME type %q", rawMediaType)
	}
	mediaType = strings.ToLower(mediaType)
	if !strings.HasPrefix(mediaType, "image/") {
		return "", fmt.Errorf("unsupported image MIME type %q", mediaType)
	}

	_, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("image content is not decodable: %w", err)
	}
	expectedMIME, ok := map[string]string{
		"gif":  "image/gif",
		"jpeg": "image/jpeg",
		"png":  "image/png",
		"webp": "image/webp",
	}[strings.ToLower(format)]
	if !ok || mediaType != expectedMIME {
		return "", fmt.Errorf("image MIME type %q does not match decoded %s content", mediaType, format)
	}
	return mediaType, nil
}
