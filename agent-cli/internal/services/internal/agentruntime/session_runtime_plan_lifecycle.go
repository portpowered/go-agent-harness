package agentruntime

import (
	"context"
	"fmt"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	sessionterminalwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal/wire"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func (p sessionRuntimePlan) run(ctx context.Context, out io.Writer) error {
	if p.loop.durationRunner == nil {
		return fmt.Errorf("session duration runner is required")
	}
	reporter := p.loop.terminalReporter
	if reporter == nil {
		reporter = sessionterminalwire.NewReporter()
		p.loop.terminalReporter = reporter
	}
	loopOut := out
	if p.loopOut != nil {
		loopOut = p.loopOut
	}
	return p.loop.durationRunner.Execute(sessionduration.ExecutionRequest{
		Context: ctx, Output: out, MaxDuration: p.loop.MaxDuration,
		Clock: p.loop.durationClock, SourceClock: durationLivenessClock(p.loop), FallbackClock: platformclock.Real{},
		Prepare: func(ctx context.Context, output io.Writer) error {
			if p.replayIntegrityWarning != "" {
				if _, err := fmt.Fprintln(output, p.replayIntegrityWarning); err != nil {
					return err
				}
			}
			if err := p.bindRTC(ctx); err != nil {
				return err
			}
			return p.writeAnnouncements(output, true)
		},
		Run: func(ctx context.Context, _ io.Writer, clock sessionduration.TimerScheduler) error {
			loop := p.loop
			loop.durationClock = clock
			p.configureLoopObserver(&loop)
			if p.inferencer == nil {
				return nil
			}
			reporter.MarkRunStarted()
			if err := runAgentLoopSession(ctx, loopOut, p.inferencer, loop); err != nil {
				return wrapSessionRuntimeError(p, wrapSessionPhaseError("run session loop", err))
			}
			return nil
		},
		Finalization: p.finalizationPorts(nil, true),
	})
}

func (p *sessionRuntimePlan) finalizationPorts(artifacts sessionduration.ArtifactLifecycle, recordArtifacts bool) sessionduration.FinalizationPorts {
	ports := sessionduration.FinalizationPorts{
		CloseCapabilities: func() error {
			if p.capabilityCoordinator == nil {
				return nil
			}
			return p.capabilityCoordinator.Close()
		},
		CloseSession: p.closeSession,
		CloseBinding: func() error {
			if p.rtcBinding == nil {
				return nil
			}
			return p.rtcBinding.Close()
		},
		CloseRuntime: func() error {
			if p.rtcRuntime == nil {
				return nil
			}
			return p.rtcRuntime.Close()
		},
		FlushCapture: p.flushCapture,
		ReleaseCapture: func() error {
			if p.captureClaim == nil {
				return nil
			}
			return wrapSessionRuntimeError(*p, p.captureClaim.release())
		},
		Artifacts: artifacts,
	}
	reporter := p.loop.terminalReporter
	if reporter == nil {
		reporter = sessionterminalwire.NewReporter()
	}
	if recordArtifacts {
		ports.RecordArtifactFinalization = reporter.RecordArtifactFinalization
	}
	if p.replayCompletion != nil {
		ports.HasIndependentFailure = sessionterminalwire.HasIndependentFailure
		ports.CompleteReplay = func() { p.replayCompletion(reporter) }
	}
	ports.PublishTerminal = reporter.Publish
	if p.finalize != nil {
		ports.Finalize = func(ctx context.Context, out io.Writer) error {
			if reporter := p.loop.terminalReporter; reporter != nil {
				ctx = sessionterminalwire.WithReporter(ctx, reporter)
			}
			return wrapSessionRuntimeError(*p, p.finalize(ctx, out))
		}
	}
	return ports
}
