package images

import (
	"slices"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

const imageMIMEPrefix = "image/"

// ResolveCapabilities decides whether the session model accepts image input.
// Only the image-capable realtime provider qualifies; an explicitly empty
// model is rejected; configured metadata may narrow the MIME set or reject
// the model.
func ResolveCapabilities(request sessionturn.ImageCapabilityRequest) (sessionturn.ImageCapabilities, error) {
	if !strings.EqualFold(strings.TrimSpace(request.Provider), sessionturn.ImageInputProvider) {
		return sessionturn.ImageCapabilities{}, CapabilityError(request.Model)
	}
	model, err := resolveModel(request)
	if err != nil {
		return sessionturn.ImageCapabilities{}, err
	}
	if request.RealtimeImageInput == nil {
		return sessionturn.ImageCapabilities{}, CapabilityError(model)
	}
	if supported, known := request.RealtimeImageInput(model); !known || !supported {
		return sessionturn.ImageCapabilities{}, CapabilityError(model)
	}
	var configured *sessionturn.ImageModelMetadata
	if request.ConfiguredModel != nil {
		configured, err = request.ConfiguredModel(model)
		if err != nil {
			return sessionturn.ImageCapabilities{}, err
		}
	}
	var mimeTypes []string
	if configured != nil {
		if !configuredImageInput(configured) {
			return sessionturn.ImageCapabilities{}, CapabilityError(model)
		}
		mimeTypes = append(mimeTypes, configured.SupportedInputMIMETypes...)
	}
	return sessionturn.ImageCapabilities{Model: model, SupportsImageInput: true, SupportedInputMIMETypes: mimeTypes}, nil
}

func resolveModel(request sessionturn.ImageCapabilityRequest) (string, error) {
	model := strings.TrimSpace(request.Model)
	switch {
	case model != "":
		return model, nil
	case request.ModelProvided:
		return "", CapabilityError(model)
	case request.ReplayModel != "":
		return request.ReplayModel, nil
	case request.DefaultModel == nil:
		return "", CapabilityError(model)
	}
	return request.DefaultModel()
}

// configuredImageInput honors declared modalities, or infers image input
// from declared MIME types when no modality is configured.
func configuredImageInput(model *sessionturn.ImageModelMetadata) bool {
	if slices.Contains(model.InputModalities, sessionturn.ImageInputModality) {
		return true
	}
	return len(model.InputModalities) == 0 && slices.ContainsFunc(model.SupportedInputMIMETypes, func(mime string) bool {
		return strings.HasPrefix(mime, imageMIMEPrefix)
	})
}

// CloneCapabilities returns an independent copy.
func CloneCapabilities(capabilities sessionturn.ImageCapabilities) sessionturn.ImageCapabilities {
	capabilities.SupportedInputMIMETypes = append([]string(nil), capabilities.SupportedInputMIMETypes...)
	return capabilities
}
