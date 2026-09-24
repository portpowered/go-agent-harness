package agentruntime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	audioiowire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	durationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/wire"
)

func newTestAudioIOService() audioio.Service { return audioiowire.NewService() }

func newTestSessionRuntimeFactory() SessionRuntimeFactory {
	durationService := durationwire.NewService()
	durationRunner := sessionwire.NewDurationRunner(sessionwire.DurationDependencies{
		DurationService: durationService,
		LoopFactory:     sessionwire.NewDuplexLoopFactory(),
	})
	return NewSessionRuntimeFactory(durationService, durationRunner)
}

func newTestSessionRunOptions(opts SessionRunOptions) SessionRunOptions {
	opts.RuntimeFactory = newTestSessionRuntimeFactory()
	return opts
}

func newTestSessionRunOptionsPointer(opts SessionRunOptions) *SessionRunOptions {
	configured := newTestSessionRunOptions(opts)
	return &configured
}

func newTestSessionLoopOptions(opts sessionLoopOptions) sessionLoopOptions {
	factory := newTestSessionRuntimeFactory()
	opts.durationService = factory.durationService
	opts.durationRunner = factory.durationRunner
	return opts
}

func newTestRoomRunOptions(opts RoomRunOptions) RoomRunOptions {
	opts.RuntimeFactory = newTestSessionRuntimeFactory()
	return opts
}

func newTestSessionRuntimePlan(plan sessionRuntimePlan) sessionRuntimePlan {
	factory := newTestSessionRuntimeFactory()
	plan.durationService = factory.durationService
	plan.durationRunner = factory.durationRunner
	plan.loop.durationService = factory.durationService
	plan.loop.durationRunner = factory.durationRunner
	return plan
}

func runAgentLoopSessionWithDurationClock(ctx context.Context, out io.Writer, inferencer messages.SessionInferencer, opts sessionLoopOptions, maxDuration time.Duration, clock sessionduration.TimerScheduler) error {
	return runSessionDurationInvocation(ctx, out, inferencer, opts, maxDuration, clock, nil)
}

func runAgentLoopSessionWithDurationAdmissionClock(ctx context.Context, out io.Writer, inferencer messages.SessionInferencer, opts sessionLoopOptions, maxDuration time.Duration, clock sessionduration.TimerScheduler, admitted sessionduration.AdmissionInferencer) error {
	return runSessionDurationInvocation(ctx, out, inferencer, opts, maxDuration, clock, admitted)
}

func runAgentLoopSessionWithDurationAdmissionClockStream(ctx context.Context, out io.Writer, inferencer messages.SessionInferencer, opts sessionLoopOptions, maxDuration time.Duration, clock sessionduration.TimerScheduler, admitted sessionduration.AdmissionInferencer) (sessionduration.Result, error) {
	return executeDurationRequest(ctx, out, inferencer, opts, maxDuration, clock, admitted)
}

func runSessionDurationS2Case(t *testing.T, name string, maxDuration time.Duration, wantTimerCalls int, wantReason string) {
	t.Helper()
	clock := &durationTestClock{}
	switch {
	case maxDuration < 0:
		inferencer := &durationTestInferencer{}
		err := RunSessionWithMaxDurationClock(context.Background(), io.Discard, newTestSessionRunOptions(SessionRunOptions{
			ModelCatalog: testModelCatalog(), AudioService: newTestAudioIOService(), SessionInferencer: inferencer,
		}), maxDuration, clock)
		var durationErr *sessionduration.InvalidDurationError
		if !errors.As(err, &durationErr) || inferencer.connected || clock.calls != wantTimerCalls {
			t.Fatalf("negative case error=%v connected=%v timer_calls=%d", err, inferencer.connected, clock.calls)
		}
	case maxDuration == 0:
		var out bytes.Buffer
		err := RunSessionWithMaxDurationClock(context.Background(), &out, newTestSessionRunOptions(SessionRunOptions{
			ModelCatalog: testModelCatalog(), AudioService: newTestAudioIOService(),
			ReplayPath: "synthetic.session.json", SessionInferencer: &durationTestInferencer{events: durationNaturalEvents()},
		}), maxDuration, clock)
		if err != nil || !strings.Contains(out.String(), "terminal_reason=provider_close") {
			t.Fatalf("unbounded case err=%v output=%q", err, out.String())
		}
	default:
		runSessionDurationBoundedS2Case(t, name, maxDuration, wantReason, clock)
	}
	if clock.calls != wantTimerCalls || maxDuration > 0 && (clock.timer == nil || !clock.timer.stopped) {
		t.Fatalf("timer lifecycle calls=%d timer=%v", clock.calls, clock.timer)
	}
}

func runSessionDurationBoundedS2Case(t *testing.T, name string, maxDuration time.Duration, wantReason string, clock *durationTestClock) {
	t.Helper()
	writer := newDurationTestWriter()
	events := durationOutputEvents()
	closeAfterEvents := name == "longer_than_session"
	if closeAfterEvents {
		events = durationNaturalEvents()
	}
	inferencer := &durationTestInferencer{events: events, connectedCh: make(chan struct{}), closeAfterEvents: closeAfterEvents}
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- runAgentLoopSessionWithDurationClock(context.Background(), writer, inferencer, newTestSessionLoopOptions(sessionLoopOptions{audioService: newTestAudioIOService()}), maxDuration, clock)
	}()
	select {
	case <-inferencer.connectedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("session did not connect")
	}
	if name == "deadline_during_output" {
		writer.waitFor(t, "accepted output")
	}
	if !closeAfterEvents {
		clock.fire()
	}
	select {
	case err := <-runErrCh:
		if err != nil {
			t.Fatalf("bounded case: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bounded case did not finish")
	}
	if !strings.Contains(writer.String(), "terminal_reason="+wantReason) {
		t.Fatalf("bounded case terminal output = %q", writer.String())
	}
}
