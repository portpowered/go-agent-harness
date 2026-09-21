package agentruntime

import (
	"context"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	audioiowire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

func newTestAudioIOService() audioio.Service { return audioiowire.NewService() }

func runAgentLoopSessionWithDurationClock(ctx context.Context, out io.Writer, inferencer messages.SessionInferencer, opts sessionLoopOptions, maxDuration time.Duration, clock sessionduration.TimerScheduler) error {
	return runSessionDurationInvocation(ctx, out, inferencer, opts, maxDuration, clock, nil)
}

func runAgentLoopSessionWithDurationAdmissionClock(ctx context.Context, out io.Writer, inferencer messages.SessionInferencer, opts sessionLoopOptions, maxDuration time.Duration, clock sessionduration.TimerScheduler, admitted sessionduration.AdmissionInferencer) error {
	return runSessionDurationInvocation(ctx, out, inferencer, opts, maxDuration, clock, admitted)
}

func runAgentLoopSessionWithDurationAdmissionClockStream(ctx context.Context, out io.Writer, inferencer messages.SessionInferencer, opts sessionLoopOptions, maxDuration time.Duration, clock sessionduration.TimerScheduler, admitted sessionduration.AdmissionInferencer) (sessionduration.Result, error) {
	return executeDurationRequest(ctx, out, inferencer, opts, maxDuration, clock, admitted)
}
