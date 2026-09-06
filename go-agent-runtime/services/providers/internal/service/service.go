// Package service contains the built-in provider implementation behind the
// public providers.Service contract.
package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	llmproviders "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	falprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/fal"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
)

var _ providers.Service = (*Service)(nil)
var _ providers.ModelAdmission = (*Service)(nil)
var _ providers.ModelCatalog = (*Service)(nil)

// Service implements the built-in provider builders.
type Service struct {
	httpClient *http.Client
	logger     logging.Logger
	clock      clock.TimerSource
	recording  recording.Service
	catalog    providers.ModelCatalog
}

// New constructs an inert provider service. It does not dial or validate
// credentials; Build performs those operations when a request is admitted.
func New(httpClient *http.Client, logger logging.Logger, source clock.TimerSource, captures recording.Service, modelCatalog providers.ModelCatalog) *Service {
	if logger == nil {
		logger = logging.DummyLogger()
	}
	if source == nil {
		source = clock.Real{}
	}
	return &Service{httpClient: httpClient, logger: logger, clock: source, recording: captures, catalog: modelCatalog}
}

func (s *Service) Build(ctx context.Context, cfg providers.Config) (llmproviders.Provider, error) {
	if ctx == nil {
		return nil, errors.New("provider requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.APIKey) == "" && cfg.ReplayPath == "" && cfg.Provider != "fal" && requiresCredential(cfg.BaseURL) {
		return nil, errors.New("provider API key is missing")
	}
	invocation, recorder, err := s.httpRuntime(cfg)
	if err != nil {
		return nil, err
	}
	provider, err := invocation.buildConfiguredProvider(cfg)
	if err != nil {
		return nil, err
	}
	if recorder != nil {
		return &capturedProvider{Provider: provider, recorder: recorder}, nil
	}
	return provider, nil
}

func (s *Service) buildOpenAI(cfg providers.Config) llmproviders.Provider {
	opts := []oaiprovider.Option{
		oaiprovider.WithModel(cfg.Model),
		oaiprovider.WithLogger(s.logger),
	}
	if cfg.APIKey != "" {
		opts = append(opts, oaiprovider.WithAPIKey(cfg.APIKey))
	}
	if cfg.BaseURL != "" {
		opts = append(opts, oaiprovider.WithBaseURL(cfg.BaseURL))
	}
	if s.httpClient != nil {
		opts = append(opts, oaiprovider.WithHTTPClient(s.httpClient))
	}
	return oaiprovider.New(opts...)
}

func (s *Service) buildFal(cfg providers.Config) (llmproviders.Provider, error) {
	if cfg.Fal == nil {
		return nil, fmt.Errorf("model.provider is fal but model.fal is not configured")
	}
	var opts []falprovider.Option
	if cfg.Fal.APIKey != "" {
		opts = append(opts, falprovider.WithAPIKey(cfg.Fal.APIKey))
	}
	if cfg.Fal.BaseURL != "" {
		opts = append(opts, falprovider.WithBaseURL(cfg.Fal.BaseURL))
	}
	if s.httpClient != nil {
		opts = append(opts, falprovider.WithHTTPClient(s.httpClient))
	}
	return falprovider.New(opts...), nil
}

func (s *Service) buildConfiguredProvider(cfg providers.Config) (llmproviders.Provider, error) {
	if cfg.Provider == "fal" {
		return s.buildFal(cfg)
	}
	return s.buildOpenAI(cfg), nil
}
