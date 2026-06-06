package agent

import (
	"fmt"
	"net/http"

	falprovider "github.com/portpowered/go-llm-gateway/pkg/providers/fal"
	"github.com/portpowered/go-llm-gateway/pkg/testing"
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

	var recordRT *testing.RecordRoundTripper
	if ctx.ExecConfig.ReplayCapturePath != "" {
		replayRT, err := testing.NewReplayRoundTripper(ctx.ExecConfig.ReplayCapturePath)
		if err != nil {
			return ProviderBuildResult{}, fmt.Errorf("failed to load replay captures: %w", err)
		}
		opts = append(opts, falprovider.WithHTTPClient(&http.Client{Transport: replayRT}))
	} else if ctx.ExecConfig.RecordCapturePath != "" {
		recordRT = testing.NewRecordRoundTripper(http.DefaultTransport)
		opts = append(opts, falprovider.WithHTTPClient(&http.Client{Transport: recordRT}))
	}

	return ProviderBuildResult{
		Provider: falprovider.New(opts...),
		Recorder: recordRT,
	}, nil
}
