package agent

// This file owns executor validation and the request errors produced when configured models cannot accept requested output or input content.

import (
	"fmt"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/input"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
)

// validateOutputModality checks that the requested output modality is supported by the configured model.
// Returns nil if modality is empty, "text", or supported by the model. Returns an error otherwise.
func (e *Executor) validateOutputModality(cfg *Config, runData *RunData) error {
	if e.relaxModelValidation {
		return nil
	}
	modality := cfg.OutputModality
	if modality == "" || modality == "text" {
		return nil
	}
	if e.inferencerOverride != nil && cfg.ConfigDir == "" {
		return nil
	}

	loadedCfg, err := e.loadConfig(cfg)
	if err != nil {
		return nil // skip validation if config can't be loaded
	}
	active, err := loadedCfg.ActiveOpenAIConfig()
	if err != nil {
		return nil // skip validation if provider config unavailable
	}
	modelName := active.Model
	if runData.Models == nil {
		return nil
	}
	modelInfo := runData.Models.Lookup(modelName)
	if modelInfo == nil {
		return nil // unknown model — allow the request and let the provider decide
	}
	if !modelInfo.SupportsOutputModality(modality) {
		return fmt.Errorf("model %q does not support %s output", modelName, modality)
	}
	return nil
}

// validateInputMimeTypes checks that every file content part in the input is accepted by the
// configured model's supportedInputMimeTypes list. Follows the same resolution flow as
// validateOutputModality: silently allows if config or model info is unavailable.
func (e *Executor) validateInputMimeTypes(cfg *Config, runData *RunData, execInput agentloop.ExecuteInput) error {
	if e.relaxModelValidation || len(execInput.ContentParts) == 0 {
		return nil
	}
	if e.inferencerOverride != nil && cfg.ConfigDir == "" {
		return nil
	}
	loadedCfg, err := e.loadConfig(cfg)
	if err != nil {
		return nil // skip validation if config can't be loaded
	}
	active, err := loadedCfg.ActiveOpenAIConfig()
	if err != nil {
		return nil // skip validation if provider config unavailable
	}
	modelName := active.Model
	if runData.Models == nil {
		return nil
	}
	modelInfo := runData.Models.Lookup(modelName)
	if modelInfo == nil {
		return nil // unknown model — allow and let the provider decide
	}
	return input.ValidateContentPartsMimeTypes(execInput.ContentParts, modelName, modelInfo.SupportedInputMimeTypes)
}
