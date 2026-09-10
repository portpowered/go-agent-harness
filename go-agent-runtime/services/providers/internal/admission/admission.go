// Package admission owns the provider model decision without importing the
// public providers contract. The public package adapts its catalog and maps
// the decision into the stable error types exposed to hosts.
package admission

import "strings"

const openAIProvider = "openai"

// Model is the private representation used while deciding whether a model is
// available. Keeping this type local avoids coupling the decision core to the
// public contract package and keeps the dependency direction one-way.
type Model struct {
	ID                      string
	SupportsAudio           bool
	SupportsImageInput      bool
	SupportsFunctionCalling bool
	SupportsReasoning       bool
}

// Catalog is the smallest catalog surface needed by model admission. The
// caller owns the catalog; SupportedRealtimeModelIDs is copied into each
// rejected decision so error snapshots cannot share caller-owned storage.
type Catalog interface {
	LookupRealtimeModel(provider, model string) (Model, bool)
	SupportedRealtimeModelIDs(provider string) []string
}

// Options preserve the normalization policy of each host boundary. The
// provider service trims both fields; the CLI adapters can deliberately retain
// self-play's historical raw-model lookup while sharing the same decision.
type Options struct {
	TrimProvider bool
	TrimModel    bool
}

// Decision is the complete result of one admission attempt. Allowed is true
// for providers whose realtime catalog is intentionally unrestricted; Matched
// is true only when an OpenAI model was found and metadata is available.
type Decision struct {
	Allowed         bool
	Matched         bool
	MissingCatalog  bool
	Provider        string
	RequestedModel  string
	NormalizedModel string
	Model           Model
	SupportedModels []string
}

// Admission is the single model decision implementation shared by the public
// provider facade, the built-in provider service, and CLI compatibility
// adapters.
type Admission struct {
	catalog Catalog
}

func New(catalog Catalog) *Admission { return &Admission{catalog: catalog} }

// Decide applies the requested boundary normalization, then performs one
// provider-owned lookup. Nil admission/catalog dependencies fail closed before
// provider policy is evaluated, matching the provider service contract.
func (a *Admission) Decide(provider, model string, options Options) Decision {
	requestedModel := model
	if options.TrimProvider {
		provider = strings.TrimSpace(provider)
	}
	if options.TrimModel {
		model = strings.TrimSpace(model)
	}
	decision := Decision{
		Provider:        provider,
		RequestedModel:  requestedModel,
		NormalizedModel: model,
	}
	if a == nil || a.catalog == nil {
		decision.MissingCatalog = true
		return decision
	}
	if !strings.EqualFold(provider, openAIProvider) {
		decision.Allowed = true
		return decision
	}

	metadata, ok := a.catalog.LookupRealtimeModel(provider, model)
	if ok {
		decision.Allowed = true
		decision.Matched = true
		decision.Model = metadata
		return decision
	}
	decision.SupportedModels = append([]string(nil), a.catalog.SupportedRealtimeModelIDs(provider)...)
	return decision
}
