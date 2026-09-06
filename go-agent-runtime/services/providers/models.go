package providers

import (
	"errors"
	"fmt"
	"strings"
)

// RealtimeModel describes the capabilities exposed by a built-in realtime
// model. It is a value contract; catalog storage remains private to the
// provider implementation.
type RealtimeModel struct {
	ID                      string
	SupportsAudio           bool
	SupportsImageInput      bool
	SupportsFunctionCalling bool
	SupportsReasoning       bool
}

// ErrUnsupportedRealtimeModel identifies a model that is not registered for
// the selected provider's realtime session surface.
var ErrUnsupportedRealtimeModel = errors.New("unsupported realtime model")

// ErrModelCatalogRequired identifies a provider service that was composed
// without its immutable model catalog dependency.
var ErrModelCatalogRequired = errors.New("provider model catalog is required")

// UnsupportedRealtimeModelError reports an unregistered model and the
// deterministic set of supported model IDs for the provider.
type UnsupportedRealtimeModelError struct {
	Provider        string
	Model           string
	SupportedModels []string
}

func (e *UnsupportedRealtimeModelError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s model %q is not realtime-capable for agent session; supported realtime models: %s", e.Provider, e.Model, strings.Join(e.SupportedModels, ", "))
}

func (e *UnsupportedRealtimeModelError) Unwrap() error {
	if e == nil {
		return nil
	}
	return ErrUnsupportedRealtimeModel
}

// ModelCatalog exposes the immutable built-in model metadata needed by
// callers that select capabilities after admission. Implementations own their
// backing storage and return fresh values so a request cannot mutate another
// request's catalog.
type ModelCatalog interface {
	RealtimeModels(provider string) []RealtimeModel
	LookupRealtimeModel(provider, model string) (RealtimeModel, bool)
	SupportedRealtimeModelIDs(provider string) []string
}

// ModelAdmission is the provider model capability boundary. It is separate
// from SessionService so custom builders remain usable without the built-in
// catalog.
type ModelAdmission interface {
	ValidateSessionModel(provider, model string) error
}

const (
	OpenAIRealtimeLegacyModel  = "gpt-realtime"
	OpenAIRealtimeDefaultModel = "gpt-realtime-2.1-mini"
	OpenAIRealtime21Model      = "gpt-realtime-2.1"
)
