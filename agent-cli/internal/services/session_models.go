package services

import (
	"errors"
	"fmt"
	"strings"
)

const (
	openAIRealtimeLegacyModel  = "gpt-realtime"
	openAIRealtimeDefaultModel = "gpt-realtime-2.1-mini"
	openAIRealtime21Model      = "gpt-realtime-2.1"

	// DefaultOpenAIRealtimeModel is the model selected for an OpenAI realtime
	// session when no model is configured.
	DefaultOpenAIRealtimeModel = openAIRealtimeDefaultModel
)

// ErrUnsupportedRealtimeModel identifies a model that is not registered for
// the OpenAI realtime session surface.
var ErrUnsupportedRealtimeModel = errors.New("unsupported realtime model")

// ValidateOpenAIRealtimeReasoningEffort validates the documented Realtime
// reasoning budgets. Empty preserves the provider default.
func ValidateOpenAIRealtimeReasoningEffort(effort string) error {
	switch strings.TrimSpace(effort) {
	case "", "minimal", "low", "medium", "high", "xhigh":
		return nil
	default:
		return fmt.Errorf("--reasoning-effort must be one of minimal, low, medium, high, or xhigh; got %q", effort)
	}
}

// ErrUnsupportedOpenAIRealtimeModel is an explicit provider-named alias for
// ErrUnsupportedRealtimeModel. Both names preserve the same error identity.
var ErrUnsupportedOpenAIRealtimeModel = ErrUnsupportedRealtimeModel

// OpenAIRealtimeModel describes the capabilities exposed by one supported
// OpenAI realtime model.
type OpenAIRealtimeModel struct {
	ID                      string
	SupportsAudio           bool
	SupportsImageInput      bool
	SupportsFunctionCalling bool
	SupportsReasoning       bool
}

// UnsupportedRealtimeModelError reports an unregistered OpenAI realtime model
// and the deterministic set of supported model IDs.
type UnsupportedRealtimeModelError struct {
	Model           string
	SupportedModels []string
}

// UnsupportedOpenAIRealtimeModelError is the provider-named form of the
// realtime model validation error.
type UnsupportedOpenAIRealtimeModelError = UnsupportedRealtimeModelError

func (e *UnsupportedRealtimeModelError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf(
		"OpenAI model %q is not realtime-capable for agent session; supported realtime models: %s",
		e.Model,
		strings.Join(e.SupportedModels, ", "),
	)
}

func (e *UnsupportedRealtimeModelError) Unwrap() error {
	if e == nil {
		return nil
	}
	return ErrUnsupportedRealtimeModel
}

// openAIRealtimeModelRegistry is an ordered value registry. Callers receive a
// copy through OpenAIRealtimeModels, so its backing storage cannot be changed
// outside this package.
var openAIRealtimeModelRegistry = [...]OpenAIRealtimeModel{
	{
		ID:                      openAIRealtimeLegacyModel,
		SupportsAudio:           true,
		SupportsImageInput:      true,
		SupportsFunctionCalling: true,
	},
	{
		ID:                      openAIRealtimeDefaultModel,
		SupportsAudio:           true,
		SupportsImageInput:      true,
		SupportsFunctionCalling: true,
	},
	{
		ID:                      openAIRealtime21Model,
		SupportsAudio:           true,
		SupportsImageInput:      true,
		SupportsFunctionCalling: true,
		SupportsReasoning:       true,
	},
}

// OpenAIRealtimeModels returns the supported models in deterministic registry
// order. The returned slice is independent from the registry backing storage.
func OpenAIRealtimeModels() []OpenAIRealtimeModel {
	models := make([]OpenAIRealtimeModel, len(openAIRealtimeModelRegistry))
	copy(models, openAIRealtimeModelRegistry[:])
	return models
}

// SupportedOpenAIRealtimeModels is a descriptive alias for OpenAIRealtimeModels.
func SupportedOpenAIRealtimeModels() []OpenAIRealtimeModel {
	return OpenAIRealtimeModels()
}

// LookupOpenAIRealtimeModel returns exact metadata for a registered model ID.
// Model IDs are case-sensitive and are not silently normalized.
func LookupOpenAIRealtimeModel(model string) (OpenAIRealtimeModel, bool) {
	for _, supported := range openAIRealtimeModelRegistry {
		if supported.ID == model {
			return supported, true
		}
	}
	return OpenAIRealtimeModel{}, false
}

// SupportedOpenAIRealtimeModelIDs returns the registered IDs in registry order.
func SupportedOpenAIRealtimeModelIDs() []string {
	ids := make([]string, 0, len(openAIRealtimeModelRegistry))
	for _, model := range openAIRealtimeModelRegistry {
		ids = append(ids, model.ID)
	}
	return ids
}

func unsupportedOpenAIRealtimeModelError(model string) error {
	return &UnsupportedRealtimeModelError{
		Model:           model,
		SupportedModels: SupportedOpenAIRealtimeModelIDs(),
	}
}
