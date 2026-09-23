package agentruntime

import (
	"context"
	"fmt"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	sessiontracewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"
)

func runSelfPlaySide(ctx context.Context, name string, side int, inferencer messages.SessionInferencer, prompt string, output *selfPlayPCMBridge, ready chan<- *agentloop.AgentLoop, opts SelfPlayRunOptions, livenessClock SessionLivenessClock, evidence *selfPlayEvidence, stop *selfPlayStopState, results chan<- selfPlaySideResult) {
	sideEvidence := evidence.side(side)
	sideEvidence.diagnosticErr = func(err error) {
		wrapped := fmt.Errorf("%s diagnostic evidence: %w", name, err)
		evidence.fail(wrapped)
		stop.fail(wrapped)
	}
	observer := sessiontracewire.NewObserver(sessiontrace.NewObserverOptions{
		Sink: sideEvidence, Provider: opts.Provider, Model: opts.Model,
		RuntimeRecorder: sideEvidence.runtimeRecord,
		TerminalService: wire.NewService(),
	})
	observer.SetTurnAdmission(func(messages.StreamMessage) bool {
		return stop.recordTurn(side, opts.MaxTurns)
	})
	observer.SetStreamObserver(selfPlayStreamObserver(ctx, name, sideEvidence, evidence, stop, output))
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
