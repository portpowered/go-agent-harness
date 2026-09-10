package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	providerwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/wire"
)

const (
	customModelID    = "custom-only"
	secondModelID    = "custom-second"
	unknownModelID   = "gpt-realtime"
	customProviderID = "openai"
)

type customCatalog struct {
	models []providers.RealtimeModel
}

func (c customCatalog) RealtimeModels(provider string) []providers.RealtimeModel {
	if !strings.EqualFold(strings.TrimSpace(provider), customProviderID) {
		return nil
	}
	return append([]providers.RealtimeModel(nil), c.models...)
}

func (c customCatalog) LookupRealtimeModel(provider, model string) (providers.RealtimeModel, bool) {
	if !strings.EqualFold(strings.TrimSpace(provider), customProviderID) {
		return providers.RealtimeModel{}, false
	}
	model = strings.TrimSpace(model)
	for _, candidate := range c.models {
		if candidate.ID == model {
			return candidate, true
		}
	}
	return providers.RealtimeModel{}, false
}

func (c customCatalog) SupportedRealtimeModelIDs(provider string) []string {
	models := c.RealtimeModels(provider)
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}

type observation struct {
	Name                    string   `json:"name"`
	Error                   string   `json:"error,omitempty"`
	ErrorKind               string   `json:"error_kind,omitempty"`
	Provider                string   `json:"provider,omitempty"`
	Model                   string   `json:"model,omitempty"`
	SupportedIDs            []string `json:"supported_ids,omitempty"`
	ErrorsIsUnsupported     bool     `json:"errors_is_unsupported"`
	ErrorsIsCatalogRequired bool     `json:"errors_is_catalog_required"`
}

type report struct {
	Schema          string         `json:"schema"`
	ConstructedVia  string         `json:"constructed_via"`
	CustomSupported []string       `json:"custom_supported_ids"`
	Resolver        resolverReport `json:"resolver"`
	Observations    []observation  `json:"observations"`
}

type resolverReport struct {
	Matched bool   `json:"matched"`
	ModelID string `json:"model_id"`
}

func observe(name string, err error) observation {
	result := observation{Name: name}
	if err == nil {
		return result
	}
	result.Error = err.Error()
	result.ErrorsIsUnsupported = errors.Is(err, providers.ErrUnsupportedRealtimeModel)
	result.ErrorsIsCatalogRequired = errors.Is(err, providers.ErrModelCatalogRequired)
	var unsupported *providers.UnsupportedRealtimeModelError
	if errors.As(err, &unsupported) {
		result.ErrorKind = "UnsupportedRealtimeModelError"
		result.Provider = unsupported.Provider
		result.Model = unsupported.Model
		result.SupportedIDs = append([]string(nil), unsupported.SupportedModels...)
	}
	return result
}

func require(condition bool, format string, args ...any) error {
	if condition {
		return nil
	}
	return fmt.Errorf(format, args...)
}

func run() (report, error) {
	catalog := customCatalog{models: []providers.RealtimeModel{
		{ID: customModelID, SupportsAudio: true, SupportsFunctionCalling: true},
		{ID: secondModelID, SupportsAudio: true},
	}}
	modelAdmission := providerwire.NewModelAdmission(catalog)
	if modelAdmission == nil {
		return report{}, errors.New("providers/wire returned a nil ModelAdmission")
	}

	knownErr := modelAdmission.ValidateSessionModel(" OpenAI ", " custom-only ")
	if err := require(knownErr == nil, "custom model was not admitted: %v", knownErr); err != nil {
		return report{}, err
	}

	unknownErr := modelAdmission.ValidateSessionModel(customProviderID, unknownModelID)
	unknown := observe("custom-catalog-rejects-built-in-model", unknownErr)
	if err := require(unknownErr != nil, "unknown model was admitted"); err != nil {
		return report{}, err
	}
	if err := require(unknown.ErrorsIsUnsupported && unknown.ErrorKind == "UnsupportedRealtimeModelError", "unknown model lost its typed error: %+v", unknown); err != nil {
		return report{}, err
	}
	if err := require(unknown.Provider == "OpenAI" && unknown.Model == unknownModelID, "typed error identity changed: %+v", unknown); err != nil {
		return report{}, err
	}
	if err := require(equalStrings(unknown.SupportedIDs, []string{customModelID, secondModelID}), "custom supported ordering changed: %v", unknown.SupportedIDs); err != nil {
		return report{}, err
	}

	snapshotProbeErr := modelAdmission.ValidateSessionModel(customProviderID, "snapshot-probe")
	var snapshotProbe *providers.UnsupportedRealtimeModelError
	if !errors.As(snapshotProbeErr, &snapshotProbe) {
		return report{}, fmt.Errorf("snapshot probe did not produce typed error: %v", snapshotProbeErr)
	}
	snapshotProbe.SupportedModels[0] = "mutated-by-consumer"
	snapshot := observe("supported-model-snapshot-isolated", modelAdmission.ValidateSessionModel(customProviderID, "snapshot-probe-2"))
	if err := require(equalStrings(snapshot.SupportedIDs, []string{customModelID, secondModelID}), "supported IDs leaked consumer mutation: %v", snapshot.SupportedIDs); err != nil {
		return report{}, err
	}

	nilCatalogAdmission := providerwire.NewModelAdmission(nil)
	nilCatalogErr := nilCatalogAdmission.ValidateSessionModel(customProviderID, customModelID)
	nilCatalog := observe("nil-catalog-fails-closed", nilCatalogErr)
	if err := require(errors.Is(nilCatalogErr, providers.ErrModelCatalogRequired), "nil catalog lost dependency sentinel: %v", nilCatalogErr); err != nil {
		return report{}, err
	}

	nonOpenAIAdmissionErr := modelAdmission.ValidateSessionModel(" anthropic ", "provider-owned-model")
	nonOpenAI := observe("non-openai-provider-remains-unrestricted", nonOpenAIAdmissionErr)
	if err := require(nonOpenAIAdmissionErr == nil, "non-OpenAI provider became catalog-bound: %v", nonOpenAIAdmissionErr); err != nil {
		return report{}, err
	}

	compatibilityPort, ok := modelAdmission.(providers.ModelAdmissionResolver)
	if !ok {
		return report{}, errors.New("providers/wire admission does not expose its provider-owned compatibility port")
	}
	resolved, matched := compatibilityPort.ResolveRealtimeModel(" OPENAI ", " custom-only ", providers.ModelAdmissionOptions{TrimProvider: true, TrimModel: true})
	if err := require(matched && resolved.ID == customModelID, "shared resolver did not return custom metadata: %+v, matched=%v", resolved, matched); err != nil {
		return report{}, err
	}

	if os.Getenv("C44_WRONG_ORACLE") == "1" {
		wrongOracleErr := modelAdmission.ValidateSessionModel(customProviderID, unknownModelID)
		return report{}, fmt.Errorf("wrong oracle expected built-in %q to be admitted, but it was rejected: %v", unknownModelID, wrongOracleErr)
	}

	return report{
		Schema:          "audio-runtime-c44-model-admission/v1",
		ConstructedVia:  "providers/wire.NewModelAdmission",
		CustomSupported: []string{customModelID, secondModelID},
		Resolver:        resolverReport{Matched: matched, ModelID: resolved.ID},
		Observations:    []observation{observe("custom-model-admitted", knownErr), unknown, snapshot, nilCatalog, nonOpenAI},
	}, nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func main() {
	result, err := run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "C44 consumer failure: %v\n", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "C44 consumer report: %v\n", err)
		os.Exit(1)
	}
}
