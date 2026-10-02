package openaichatgpt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"

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
	list, _, err := p.listModels(ctx, cred)
	if err != nil {
		return nil, err
	}
	return sortedByPriority(list), nil
}

// listModels reads the model list. It returns the credential it used, which
// a 401 may have replaced through a forced refresh.
func (p *Provider) listModels(ctx context.Context, cred chatgptauth.Credential) ([]Model, chatgptauth.Credential, error) {
	endpoint := p.baseURL + modelsPath + "?" + url.Values{"client_version": {p.clientVersion}}.Encode()
	used := cred
	body, err := p.send(ctx, "models", cred, func(cred chatgptauth.Credential) (*http.Request, error) {
		used = cred
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
		if err != nil {
			return nil, fmt.Errorf("%s: create models request: %w", ProviderName, err)
		}
		p.setHeaders(req.Header, cred)
		req.Header.Set("Accept", contentTypeJSON)
		return req, nil
	})
	if err != nil {
		return nil, used, err
	}
	defer func() {
		if err := body.Close(); err != nil {
			return
		}
	}()
	var list struct {
		Models []Model `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(body, maxModelsBodyBytes)).Decode(&list); err != nil {
		return nil, used, fmt.Errorf("%s: decode model list: invalid JSON", ProviderName)
	}
	return list.Models, used, nil
}
