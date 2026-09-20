package agentruntime

import (
	"errors"
	"fmt"

	runtimeproviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	providerswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/wire"
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
