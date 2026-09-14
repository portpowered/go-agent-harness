package agentruntime

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	sessionturnwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/wire"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

// This file is the stateless CLI boundary for host model/config admission.
// Image validation, staging, binding, and cleanup remain sessionturn-owned.

func sessionHasTool(definitions []messages.ToolDefinition, name string) bool {
	for _, definition := range definitions {
		if definition.Name == name {
			return true
		}
	}
	return false
}

func bindSessionImageToolExecutor(opts SessionRunOptions, plan sessionRuntimePlan) messages.ToolExecutor {
	if opts.ToolExecutor == nil || !sessionHasTool(opts.ToolDefinitions, runtimeTools.ReadImageToolID) {
		return opts.ToolExecutor
	}
	// Initial-image entrypoints already ask the session-turn service to prepare
	// and bind this executor together with staging. Do not compose a second
	// image-preparer wrapper at the common plan boundary.
	if opts.sessionImageCapabilities != nil {
		return opts.ToolExecutor
	}

	capabilities := cloneSessionImageCapabilities(opts.sessionImageCapabilities)
	var resolveErr error
	if capabilities == nil {
		capabilityOpts := opts
		if plan.provider != "" {
			capabilityOpts.Provider = plan.provider
		}
		if plan.model != "" {
			capabilityOpts.Model = plan.model
			capabilityOpts.ModelProvided = true
		}
		var resolved sessionturn.ImageCapabilities
		resolved, resolveErr = resolveSessionImageCapabilities(capabilityOpts)
		capabilities = cloneSessionImageCapabilities(&resolved)
	}
	metadata := sessionturn.ImageCapabilities{}
	if capabilities != nil {
		metadata = *capabilities
		metadata.SupportedInputMIMETypes = append([]string(nil), capabilities.SupportedInputMIMETypes...)
	}
	if resolveErr != nil {
		var capabilityErr *sessionturn.ImageCapabilityError
		if !errors.As(resolveErr, &capabilityErr) {
			return opts.ToolExecutor
		}
		metadata.Model = capabilityErr.Model
		metadata.SupportsImageInput = false
	}
	return sessionturnwire.NewDefaultService().BindImageToolExecutor(opts.ToolExecutor, metadata)
}

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

func sessionImageStagingConfigDir(configDir string) (string, error) {
	configDir = strings.TrimSpace(configDir)
	storage, err := config.NewDefaultConfigStorage(configDir)
	if err != nil {
		return "", fmt.Errorf("resolve config directory %q: %w", configDir, err)
	}
	return filepath.Clean(filepath.Dir(storage.Path())), nil
}
