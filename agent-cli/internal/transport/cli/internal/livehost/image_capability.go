package livehost

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeProviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
)

const (
	// imageInputCapability names the capability in image capability errors.
	imageInputCapability = "image input"
	// imageInputModality is the configured model modality for image input.
	imageInputModality = "image"
	imageMIMEPrefix    = "image/"
)

// ImageCapabilityError reports a session model that cannot accept image
// input. Its message matches the session image admission contract used by
// the retired turn runtime.
type ImageCapabilityError struct{ Model, Capability string }

func (e *ImageCapabilityError) Error() string {
	return fmt.Sprintf("model %q does not support %s capability", e.Model, e.Capability)
}

func imageCapabilityError(model string) error {
	return &ImageCapabilityError{Model: strings.TrimSpace(model), Capability: imageInputCapability}
}

// imageCapability is the resolved image-input decision for one live session.
// A non-nil err rejects every image; a non-empty mimeTypes narrows the host's
// default PNG/JPEG admission to the configured set.
type imageCapability struct {
	err       error
	mimeTypes []string
}

// resolveImageCapability decides whether the selected provider/model accepts
// image input: only an OpenAI realtime model whose catalog entry supports
// image input qualifies, and configured model metadata (models.yaml) may
// reject the model or narrow its accepted MIME types.
func resolveImageCapability(provider, model, configDir string, catalog runtimeProviders.ModelCatalog) imageCapability {
	model = strings.TrimSpace(model)
	if !strings.EqualFold(strings.TrimSpace(provider), config.ProviderOpenAI) || model == "" || catalog == nil {
		return imageCapability{err: imageCapabilityError(model)}
	}
	realtime, known := catalog.LookupRealtimeModel(config.ProviderOpenAI, model)
	if !known || !realtime.SupportsImageInput {
		return imageCapability{err: imageCapabilityError(model)}
	}
	configured, err := configuredImageModel(configDir, model)
	if err != nil {
		return imageCapability{err: err}
	}
	if configured == nil {
		return imageCapability{}
	}
	if !configuredImageInput(configured) {
		return imageCapability{err: imageCapabilityError(model)}
	}
	return imageCapability{mimeTypes: append([]string(nil), configured.SupportedInputMimeTypes...)}
}

func configuredImageModel(configDir, model string) (*config.ModelInfo, error) {
	storage, err := config.NewModelsConfigStorage(configDir)
	if err != nil {
		return nil, fmt.Errorf("initialize model capability metadata: %w", err)
	}
	models, err := storage.Load()
	if err != nil {
		return nil, fmt.Errorf("load model capability metadata: %w", err)
	}
	return models.Lookup(model), nil
}

// configuredImageInput honors declared modalities, or infers image input from
// declared MIME types when no modality is configured.
func configuredImageInput(model *config.ModelInfo) bool {
	if slices.Contains(model.InputModalities, imageInputModality) {
		return true
	}
	return len(model.InputModalities) == 0 && slices.ContainsFunc(model.SupportedInputMimeTypes, func(mime string) bool {
		return strings.HasPrefix(mime, imageMIMEPrefix)
	})
}

// guardImageOpener admits images only when the session model accepts image
// input. The capability is resolved once, on first use, so sessions that never
// open an image do not read model metadata.
func guardImageOpener(resolve func() imageCapability, open func([]string) ([]messages.ContentPart, error)) func([]string) ([]messages.ContentPart, error) {
	resolveOnce := sync.OnceValue(resolve)
	return func(paths []string) ([]messages.ContentPart, error) {
		capability := resolveOnce()
		if capability.err != nil {
			return nil, capability.err
		}
		if open == nil {
			return nil, errImageOpenerUnavailable
		}
		parts, err := open(paths)
		if err != nil {
			return nil, err
		}
		if err := capability.admitMIME(paths, parts); err != nil {
			return nil, err
		}
		return parts, nil
	}
}

func (c imageCapability) admitMIME(paths []string, parts []messages.ContentPart) error {
	if len(c.mimeTypes) == 0 {
		return nil
	}
	for index, part := range parts {
		image, ok := part.(messages.ImagePart)
		if !ok || slices.Contains(c.mimeTypes, image.MediaType) {
			continue
		}
		path := ""
		if index < len(paths) {
			path = paths[index]
		}
		return fmt.Errorf("session image %q has unsupported MIME type %q (supported: %s)", path, image.MediaType, strings.Join(c.mimeTypes, ", "))
	}
	return nil
}
