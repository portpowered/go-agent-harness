package agentruntime

import (
	"fmt"
	"strings"

	runtimeproviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
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
	if opts.ModelCatalog == nil {
		return runtimeproviders.RealtimeModel{}, false
	}
	return opts.ModelCatalog.LookupRealtimeModel(sessionProviderOpenAI, strings.TrimSpace(model))
}

func unsupportedOpenAIRealtimeModelErrorFor(opts SessionRunOptions, model string) error {
	if opts.ModelCatalog == nil {
		return fmt.Errorf("%w: OpenAI realtime model admission", runtimeproviders.ErrModelCatalogRequired)
	}
	return &runtimeproviders.UnsupportedRealtimeModelError{
		Provider: "OpenAI", Model: model,
		SupportedModels: opts.ModelCatalog.SupportedRealtimeModelIDs(sessionProviderOpenAI),
	}
}

func validateBareSessionModel(opts SessionRunOptions, provider, model string) error {
	if provider != sessionProviderOpenAI {
		return nil
	}
	if opts.ModelCatalog == nil {
		return fmt.Errorf("%w: OpenAI realtime model admission", runtimeproviders.ErrModelCatalogRequired)
	}
	if _, ok := lookupOpenAIRealtimeModel(opts, model); ok {
		return nil
	}
	return unsupportedOpenAIRealtimeModelErrorFor(opts, model)
}

func validateSelfPlayModel(opts SelfPlayRunOptions) error {
	if opts.modelCatalog == nil {
		return fmt.Errorf("%w: self-play model admission", runtimeproviders.ErrModelCatalogRequired)
	}
	if _, ok := opts.modelCatalog.LookupRealtimeModel(SelfPlayDefaultProvider, opts.Model); ok {
		return nil
	}
	return fmt.Errorf("self-play model %q is not an OpenAI Realtime model; supported models: %s", opts.Model, strings.Join(opts.modelCatalog.SupportedRealtimeModelIDs(SelfPlayDefaultProvider), ", "))
}
