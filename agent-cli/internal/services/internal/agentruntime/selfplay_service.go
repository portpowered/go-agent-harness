package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"

	public "github.com/portpowered/go-agent-harness/agent-cli/internal/services/selfplay"
	runtimeproviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

var _ public.Service = (*SelfPlayService)(nil)

// SelfPlayService is the private runtime façade for the public self-play
// service contract. Keeping this adapter in agentruntime avoids a sideways
// dependency between sibling private services.
type SelfPlayService struct {
	factory      sessionRuntimeFactory
	clock        platformclock.Source
	modelCatalog runtimeproviders.ModelCatalog
}

func NewSelfPlayService(factory SessionRuntimeFactory, clockSource platformclock.Source, modelCatalog runtimeproviders.ModelCatalog) public.Service {
	return &SelfPlayService{factory: factory, clock: clockSource, modelCatalog: modelCatalog}
}

func (s *SelfPlayService) Run(ctx context.Context, out io.Writer, options public.RunOptions) error {
	if s == nil {
		return errors.New("self-play service is required")
	}
	if !s.factory.configured() {
		return errors.New("self-play session runtime factory is required")
	}
	if _, err := platformclock.RequireTimerSource(s.clock); err != nil {
		return fmt.Errorf("self-play clock: %w", err)
	}
	_, err := RunSelfPlayWithResult(ctx, out, SelfPlayRunOptions{
		APIKey:         options.APIKey,
		OutputDir:      options.OutputDir,
		Provider:       options.Provider,
		Model:          options.Model,
		BaseURL:        options.BaseURL,
		ConfigDir:      options.ConfigDir,
		MaxDuration:    options.MaxDuration,
		MaxTurns:       options.MaxTurns,
		clock:          s.clock,
		runtimeFactory: s.factory,
		modelCatalog:   s.modelCatalog,
	})
	return err
}
