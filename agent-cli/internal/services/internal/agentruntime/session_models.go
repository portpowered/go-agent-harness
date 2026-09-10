package agentruntime

import (
	"errors"
	"fmt"
	"strings"

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

func providerModelAdmission(catalog runtimeproviders.ModelCatalog) runtimeproviders.ModelAdmissionResolver {
	modelAdmission := providerswire.NewModelAdmission(catalog)
	adapter, ok := modelAdmission.(runtimeproviders.ModelAdmissionResolver)
	if !ok {
		return nil
	}
	return adapter
}

func lookupOpenAIRealtimeModel(opts SessionRunOptions, model string) (runtimeproviders.RealtimeModel, bool) {
	modelAdmission := providerModelAdmission(opts.ModelCatalog)
	if modelAdmission == nil {
		return runtimeproviders.RealtimeModel{}, false
	}
	return modelAdmission.ResolveRealtimeModel(sessionProviderOpenAI, model, runtimeproviders.ModelAdmissionOptions{TrimModel: true})
}

func unsupportedOpenAIRealtimeModelErrorFor(opts SessionRunOptions, model string) error {
	modelAdmission := providerModelAdmission(opts.ModelCatalog)
	if modelAdmission == nil {
		return fmt.Errorf("%w: OpenAI realtime model admission", runtimeproviders.ErrModelCatalogRequired)
	}
	err := modelAdmission.ValidateRealtimeModel(sessionProviderOpenAI, model, runtimeproviders.ModelAdmissionOptions{
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
	modelAdmission := providerModelAdmission(opts.modelCatalog)
	if modelAdmission == nil {
		return fmt.Errorf("%w: self-play model admission", runtimeproviders.ErrModelCatalogRequired)
	}
	err := modelAdmission.ValidateRealtimeModel(SelfPlayDefaultProvider, opts.Model, runtimeproviders.ModelAdmissionOptions{
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
