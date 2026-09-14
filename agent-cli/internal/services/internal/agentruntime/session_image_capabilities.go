package agentruntime

import (
	"fmt"
	"slices"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

func resolveSessionImageCapabilities(opts SessionRunOptions) (sessionturn.ImageCapabilities, error) {
	if !strings.EqualFold(strings.TrimSpace(effectiveSessionProvider(opts)), sessionProviderOpenAI) {
		return sessionturn.ImageCapabilities{}, sessionImageCapabilityError(opts.Model)
	}
	model := strings.TrimSpace(opts.Model)
	if model == "" && opts.ModelProvided {
		return sessionturn.ImageCapabilities{}, sessionImageCapabilityError(model)
	}
	if model == "" && opts.ReplayPath != "" {
		model = openAIRealtimeModel
	}
	if model == "" {
		resolved, err := resolveOpenAIRealtimeSessionConfig(opts)
		if err != nil {
			return sessionturn.ImageCapabilities{}, err
		}
		model = resolved.Model
	}
	realtimeModel, ok := lookupOpenAIRealtimeModel(opts, model)
	if !ok || !realtimeModel.SupportsImageInput {
		return sessionturn.ImageCapabilities{}, sessionImageCapabilityError(model)
	}
	info, err := loadSessionImageModelInfo(opts.ConfigDir, model)
	if err != nil {
		return sessionturn.ImageCapabilities{}, err
	}
	supported := []string(nil)
	if info != nil {
		if !configuredModelSupportsImageInput(info) {
			return sessionturn.ImageCapabilities{}, sessionImageCapabilityError(model)
		}
		supported = append(supported, info.SupportedInputMimeTypes...)
	}
	return sessionturn.ImageCapabilities{Model: model, SupportsImageInput: true, SupportedInputMIMETypes: supported}, nil
}

func sessionImageCapabilityError(model string) error {
	return &sessionturn.ImageCapabilityError{Model: strings.TrimSpace(model), Capability: "image input"}
}

func loadSessionImageModelInfo(configDir, model string) (*config.ModelInfo, error) {
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

func configuredModelSupportsImageInput(model *config.ModelInfo) bool {
	return model != nil && (slices.Contains(model.InputModalities, "image") ||
		(len(model.InputModalities) == 0 && slices.ContainsFunc(model.SupportedInputMimeTypes, func(mime string) bool {
			return strings.HasPrefix(mime, "image/")
		})))
}

func cloneSessionImageCapabilities(capabilities *sessionturn.ImageCapabilities) *sessionturn.ImageCapabilities {
	if capabilities == nil {
		return nil
	}
	clone := *capabilities
	clone.SupportedInputMIMETypes = append([]string(nil), capabilities.SupportedInputMIMETypes...)
	return &clone
}
