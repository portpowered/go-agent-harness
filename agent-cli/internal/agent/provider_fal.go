package agent

import (
	"fmt"

	falprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/fal"
)

// RegisterFalProvider registers the FAL provider builder under the given names.
func RegisterFalProvider(factory *ProviderFactory, names ...string) {
	for _, name := range names {
		factory.Register(name, buildFalProviderFactory)
	}
}

// buildFalProviderFactory constructs a fal.ai provider from the build context.
// This is the factory-compatible equivalent of Executor.buildFalProvider.
func buildFalProviderFactory(ctx ProviderBuildContext) (ProviderBuildResult, error) {
	falCfg := ctx.LoadedConfig.Model.Fal
	if falCfg == nil {
		return ProviderBuildResult{}, fmt.Errorf("model.provider is fal but model.fal is not configured")
	}

	opts := []falprovider.Option{}
	if falCfg.APIKey != "" {
		opts = append(opts, falprovider.WithAPIKey(falCfg.APIKey))
	}
	if falCfg.BaseURL != "" {
		opts = append(opts, falprovider.WithBaseURL(falCfg.BaseURL))
	}
	if ctx.HTTPClient != nil {
		opts = append(opts, falprovider.WithHTTPClient(ctx.HTTPClient))
	}

	return ProviderBuildResult{
		Provider: falprovider.New(opts...),
	}, nil
}
