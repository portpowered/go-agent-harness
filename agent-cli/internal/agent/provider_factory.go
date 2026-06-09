package agent

import (
	"fmt"
	"net/http"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"go.uber.org/zap"
)

// ProviderBuildContext holds the dependencies a provider builder needs.
type ProviderBuildContext struct {
	LoadedConfig *config.Config
	Logger       *zap.Logger
	HTTPClient   *http.Client
}

// ProviderBuildResult holds the result of building a provider.
type ProviderBuildResult struct {
	Provider providers.Provider
}

// ProviderBuilderFunc is a function that constructs a provider from build context.
type ProviderBuilderFunc func(ctx ProviderBuildContext) (ProviderBuildResult, error)

// ProviderFactory maps provider names to builder functions and produces
// Provider instances on demand. Use Register to add builders and Build
// to instantiate a provider by name.
type ProviderFactory struct {
	builders       map[string]ProviderBuilderFunc
	defaultBuilder ProviderBuilderFunc
}

// NewProviderFactory creates an empty ProviderFactory.
func NewProviderFactory() *ProviderFactory {
	return &ProviderFactory{
		builders: make(map[string]ProviderBuilderFunc),
	}
}

// Register adds a builder function under the given provider name.
// Calling Register with a name that is already registered overwrites the previous builder.
func (f *ProviderFactory) Register(name string, builder ProviderBuilderFunc) {
	f.builders[name] = builder
}

// RegisterDefault sets the builder used when Build is called with a name
// that has no explicit registration.
func (f *ProviderFactory) RegisterDefault(builder ProviderBuilderFunc) {
	f.defaultBuilder = builder
}

// Build looks up the builder for providerName and calls it with the given context.
// If no builder is registered for providerName and no default builder is set,
// Build returns an error listing the available provider names.
func (f *ProviderFactory) Build(providerName string, ctx ProviderBuildContext) (ProviderBuildResult, error) {
	if builder, ok := f.builders[providerName]; ok {
		return builder(ctx)
	}
	if f.defaultBuilder != nil {
		return f.defaultBuilder(ctx)
	}

	available := make([]string, 0, len(f.builders))
	for name := range f.builders {
		available = append(available, name)
	}
	return ProviderBuildResult{}, fmt.Errorf("unknown provider %q; available providers: %v", providerName, available)
}
