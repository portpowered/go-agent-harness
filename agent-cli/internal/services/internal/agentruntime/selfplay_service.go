package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"

	public "github.com/portpowered/go-agent-harness/agent-cli/internal/services/selfplay"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	runtimeproviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

var _ public.Service = (*SelfPlayService)(nil)

// SelfPlayService is the private runtime façade for the public self-play
// service contract. Keeping this adapter in agentruntime avoids a sideways
// dependency between sibling private services.
type SelfPlayService struct {
	audioService audioio.Service
	factory      sessionRuntimeFactory
	clock        platformclock.Source
	modelCatalog runtimeproviders.ModelCatalog
}

func NewSelfPlayService(audioService audioio.Service, factory SessionRuntimeFactory, clockSource platformclock.Source, modelCatalog runtimeproviders.ModelCatalog) public.Service {
	return &SelfPlayService{audioService: audioService, factory: factory, clock: clockSource, modelCatalog: modelCatalog}
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
		audioService:   s.audioService,
		clock:          s.clock,
		runtimeFactory: s.factory,
		modelCatalog:   s.modelCatalog,
	})
	return err
}

func runSelfPlaySide(ctx context.Context, name string, side int, inferencer messages.SessionInferencer, prompt string, output *selfPlayPCMBridge, ready chan<- *agentloop.AgentLoop, opts SelfPlayRunOptions, livenessClock SessionLivenessClock, evidence *selfPlayEvidence, stop *selfPlayStopState, results chan<- selfPlaySideResult) {
	sideEvidence := evidence.side(side)
	sideEvidence.diagnosticErr = func(err error) {
		wrapped := fmt.Errorf("%s diagnostic evidence: %w", name, err)
		evidence.fail(wrapped)
		stop.fail(wrapped)
	}
	observer := newSessionProgressObserver(sideEvidence, nil, opts.Provider, opts.Model)
	observer.runtime = sideEvidence.runtimeRecord
	observer.turnAdmission = func(messages.StreamMessage) bool {
		return stop.recordTurn(side, opts.MaxTurns)
	}
	observer.streamObserver = selfPlayStreamObserver(ctx, name, sideEvidence, evidence, stop, output)
	err := runAgentLoopSession(ctx, io.Discard, inferencer, sessionLoopOptions{
		audioService:  opts.audioService,
		Prompt:        prompt,
		WaitForClose:  true,
		Done:          stop.done,
		DoneErr:       stop.doneErr,
		observer:      observer,
		runtime:       sideEvidence.runtimeRecord,
		loopReady:     ready,
		clockSource:   opts.clock,
		livenessClock: livenessClock,
	})
	results <- selfPlaySideResult{name: name, err: err}
}

func recordSelfPlaySideResult(result selfPlaySideResult, stop *selfPlayStopState) {
	if result.err != nil {
		if !stop.stopped() {
			stop.fail(fmt.Errorf("%s session: %w", result.name, result.err))
		}
		return
	}
	if !stop.stopped() {
		stop.fail(fmt.Errorf("%s session ended before a self-play bound", result.name))
	}
}
