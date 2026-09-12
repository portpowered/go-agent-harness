package agentruntime

import (
	"errors"
	"fmt"
	"strings"

	runtimeproviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	providerswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const (
	openAIRealtimeLegacyModel  = runtimeproviders.OpenAIRealtimeLegacyModel
	openAIRealtimeDefaultModel = runtimeproviders.OpenAIRealtimeDefaultModel
	openAIRealtime21Model      = runtimeproviders.OpenAIRealtime21Model

	// DefaultOpenAIRealtimeModel is the model selected for an OpenAI realtime
	// session when no model is configured.
	DefaultOpenAIRealtimeModel = openAIRealtimeDefaultModel
)

var (
	ErrUnsupportedRealtimeModel       = runtimeproviders.ErrUnsupportedRealtimeModel
	ErrUnsupportedOpenAIRealtimeModel = ErrUnsupportedRealtimeModel
)

type OpenAIRealtimeModel = runtimeproviders.RealtimeModel
type UnsupportedRealtimeModelError = runtimeproviders.UnsupportedRealtimeModelError
type UnsupportedOpenAIRealtimeModelError = UnsupportedRealtimeModelError

func lookupOpenAIRealtimeModel(opts SessionRunOptions, model string) (runtimeproviders.RealtimeModel, bool) {
	return providerswire.NewModelAdmission(opts.ModelCatalog).ResolveRealtimeModel(sessionProviderOpenAI, model, runtimeproviders.ModelAdmissionOptions{TrimModel: true})
}

func unsupportedOpenAIRealtimeModelErrorFor(opts SessionRunOptions, model string) error {
	err := providerswire.NewModelAdmission(opts.ModelCatalog).ValidateRealtimeModel(sessionProviderOpenAI, model, runtimeproviders.ModelAdmissionOptions{
		TrimModel:            true,
		PreserveModelInError: true,
	})
	if errors.Is(err, runtimeproviders.ErrModelCatalogRequired) {
		return fmt.Errorf("%w: OpenAI realtime model admission", err)
	}
	return err
}

func validateBareSessionModel(opts SessionRunOptions, provider, model string) error {
	if provider != sessionProviderOpenAI {
		return nil
	}
	return unsupportedOpenAIRealtimeModelErrorFor(opts, model)
}

func validateSelfPlayModel(opts SelfPlayRunOptions) error {
	err := providerswire.NewModelAdmission(opts.modelCatalog).ValidateRealtimeModel(SelfPlayDefaultProvider, opts.Model, runtimeproviders.ModelAdmissionOptions{
		PreserveModelInError: true,
	})
	if err == nil {
		return nil
	}
	if errors.Is(err, runtimeproviders.ErrModelCatalogRequired) {
		return fmt.Errorf("%w: self-play model admission", err)
	}
	var unsupported *runtimeproviders.UnsupportedRealtimeModelError
	if errors.As(err, &unsupported) {
		return fmt.Errorf("self-play model %q is not an OpenAI Realtime model; supported models: %s", unsupported.Model, strings.Join(unsupported.SupportedModels, ", "))
	}
	return err
}

// VoiceLoudnessGainDB returns the fixed output gain, in dB, that normalizes
// voice toward the shared cross-voice loudness target. An empty, unknown, or
// unmeasured voice returns exactly 0 (no adjustment). All currently documented
// built-in voices have an independently measured entry.
func VoiceLoudnessGainDB(voice string) float64 {
	return audio.VoiceLoudnessGainDB(voice)
}
