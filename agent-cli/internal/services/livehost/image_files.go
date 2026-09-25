package livehost

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // registers JPEG decoding for opening image validation
	_ "image/png"  // registers PNG decoding for opening image validation
	"net/http"
	"os"
	"strings"

	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

// stageOpeningImages delegates read_image staging to the runtime session
// service. The staging parent is the command's config directory.
func stageOpeningImages(request serviceSession.Request, liveRequest *runtimeSession.LiveRequest, stager runtimeSession.LiveImageStager) (func() error, error) {
	if len(request.ImagePaths) == 0 {
		return func() error { return nil }, nil
	}
	if stager == nil {
		return nil, errors.New("live image stager is unavailable")
	}
	return stager.StageOpeningImages(runtimeSession.LiveImageStageRequest{Directory: request.ConfigDir, SourcePaths: request.ImagePaths}, liveRequest)
}

// OpenImages resolves the command's image paths at the host edge. The
// reusable live runtime receives immutable content parts and never reads host
// paths or performs MIME discovery itself.
func OpenImages(paths []string) ([]messages.ContentPart, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	parts := make([]messages.ContentPart, 0, len(paths))
	for _, path := range paths {
		part, err := openImage(path)
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
	return parts, nil
}

func openImage(path string) (messages.ImagePart, error) {
	if strings.TrimSpace(path) == "" {
		return messages.ImagePart{}, fmt.Errorf("--image path is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return messages.ImagePart{}, fmt.Errorf("session image %q is missing: %w", path, err)
		}
		return messages.ImagePart{}, fmt.Errorf("session image %q cannot be read: %w", path, err)
	}
	if len(data) == 0 {
		return messages.ImagePart{}, fmt.Errorf("image %q is empty", path)
	}
	mediaType := http.DetectContentType(data)
	if mediaType != "image/png" && mediaType != "image/jpeg" {
		return messages.ImagePart{}, fmt.Errorf("image %q has unsupported MIME type %q (supported: image/png, image/jpeg)", path, mediaType)
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return messages.ImagePart{}, fmt.Errorf("image %q is not valid %s content: %w", path, mediaType, err)
	}
	return messages.ImagePart{Bytes: data, MediaType: mediaType}, nil
}

// BindImagePreparer gives a live participant's read_image route the same
// host-side image validation used for opening an initial image turn. The
// runtime service performs the filesystem authorization before invoking this
// callback; the callback only resolves the already-authorized bytes into the
// provider-neutral typed part. open is the capability-guarded opener, so a
// model without image input yields a correlated tool failure.
func BindImagePreparer(executor messages.ToolExecutor, open ImageOpener) messages.ToolExecutor {
	binder, ok := executor.(runtimeTools.SessionImagePreparerBinder)
	if !ok {
		return executor
	}
	return binder.WithSessionImagePreparer(func(paths []string) ([]messages.ImagePart, error) {
		content, err := open(paths)
		if err != nil {
			return nil, err
		}
		parts := make([]messages.ImagePart, 0, len(content))
		for _, item := range content {
			part, ok := item.(messages.ImagePart)
			if !ok {
				return nil, fmt.Errorf("live image preparer received unexpected content part %T", item)
			}
			part.Bytes = append([]byte(nil), part.Bytes...)
			parts = append(parts, part)
		}
		return parts, nil
	})
}
