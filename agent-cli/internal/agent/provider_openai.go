package agent

import (
	"github.com/portpowered/go-agent-harness/agent-cli/internal/logger"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
)

// RegisterOpenAIProvider registers the OpenAI-compatible provider builder
// under the given names. It also sets the builder as the factory default,
// matching the current behaviour where any unrecognised provider string
// falls through to the OpenAI path.
func RegisterOpenAIProvider(factory *ProviderFactory, names ...string) {
	builder := buildOpenAIProviderFactory
	for _, name := range names {
		factory.Register(name, builder)
	}
	factory.RegisterDefault(builder)
}

// buildOpenAIProviderFactory constructs an OpenAI-compatible provider from
// the build context. This is the factory-compatible equivalent of
// Executor.buildOpenAIProvider.
func buildOpenAIProviderFactory(ctx ProviderBuildContext) (ProviderBuildResult, error) {
	active, err := ctx.LoadedConfig.ActiveOpenAIConfig()
	if err != nil {
		return ProviderBuildResult{}, err
	}

	providerOpts := []oaiprovider.Option{
		oaiprovider.WithModel(active.Model),
		oaiprovider.WithLogger(logger.NewZapGatewayAdapter(ctx.Logger)),
	}
	if active.APIKey != "" {
		providerOpts = append(providerOpts, oaiprovider.WithAPIKey(active.APIKey))
	}
	if active.BaseURL != "" {
		providerOpts = append(providerOpts, oaiprovider.WithBaseURL(active.BaseURL))
	}
	if ctx.HTTPClient != nil {
		providerOpts = append(providerOpts, oaiprovider.WithHTTPClient(ctx.HTTPClient))
	}

	return ProviderBuildResult{
		Provider: oaiprovider.New(providerOpts...),
	}, nil
}
