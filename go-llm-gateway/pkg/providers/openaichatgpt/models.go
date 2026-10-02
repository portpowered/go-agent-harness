package openaichatgpt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai/chatgptauth"
)

// visibilityList marks a model the Codex picker shows.
const visibilityList = "list"

// maxModelsBodyBytes bounds the model list read.
const maxModelsBodyBytes = 4 << 20

// Model is one entry of the account's Codex model list
// (GET {base}/models?client_version=...). The fields are the subset of
// Codex's ModelInfo (codex-rs/protocol/src/openai_models.rs) this provider
// uses.
type Model struct {
	Slug           string `json:"slug"`
	DisplayName    string `json:"display_name"`
	Visibility     string `json:"visibility"`
	Priority       int    `json:"priority"`
	SupportedInAPI bool   `json:"supported_in_api"`
}

// DefaultModel returns the account's default model by Codex's rule: sort by
// priority (lowest first), then take the first model whose visibility is
// "list", or the first model when none is listed
// (ModelPreset::mark_default_by_picker_visibility). A ChatGPT login sees
// every model, so nothing is filtered by supported_in_api.
func DefaultModel(list []Model) (string, bool) {
	if len(list) == 0 {
		return "", false
	}
	sorted := sortedByPriority(list)
	for _, model := range sorted {
		if model.Visibility == visibilityList && model.Slug != "" {
			return model.Slug, true
		}
	}
	if sorted[0].Slug == "" {
		return "", false
	}
	return sorted[0].Slug, true
}

func sortedByPriority(list []Model) []Model {
	sorted := append([]Model(nil), list...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Priority < sorted[j].Priority })
	return sorted
}

// Models lists the account's Codex models, sorted by priority. The list
// depends on the account and plan, so it is read at runtime and never
// hard-coded.
func (p *Provider) Models(ctx context.Context) ([]Model, error) {
	cred, err := p.credential(ctx)
	if err != nil {
		return nil, err
	}
	list, err := p.listModels(ctx, cred)
	if err != nil {
		return nil, err
	}
	return sortedByPriority(list), nil
}

func (p *Provider) listModels(ctx context.Context, cred chatgptauth.Credential) ([]Model, error) {
	endpoint := p.baseURL + modelsPath + "?" + url.Values{"client_version": {p.clientVersion}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("%s: create models request: %w", ProviderName, err)
	}
	p.setHeaders(req.Header, cred)
	req.Header.Set("Accept", contentTypeJSON)
	resp, err := p.client().Do(req)
	if err != nil {
		return nil, requestError("models", err)
	}
	defer func() { closeBody(resp) }()
	if resp.StatusCode != http.StatusOK {
		code := readErrorCode(resp.Body)
		p.logger.Error(ProviderName+": models request failed",
			logging.Field{Key: "status_code", Value: resp.StatusCode}, logging.Field{Key: "code", Value: code})
		return nil, statusError(resp.StatusCode, code)
	}
	var body struct {
		Models []Model `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxModelsBodyBytes)).Decode(&body); err != nil {
		return nil, fmt.Errorf("%s: decode model list: invalid JSON", ProviderName)
	}
	return body.Models, nil
}
