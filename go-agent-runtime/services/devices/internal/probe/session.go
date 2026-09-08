package deviceprobe

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/participants"
)

const deviceProbeRunnerBufferSize = 128

func startLiveDeviceProbeSession(ctx context.Context, inferencer messages.SessionInferencer, resources liveDeviceProbeResources) (*participants.ModelRunner, *liveDeviceProbeSessionBridge, context.CancelFunc, context.CancelFunc, <-chan struct{}) {
	runner := participants.NewSessionModelRunner(inferencer, deviceProbeRunnerBufferSize, nil)
	runnerContext, runnerCancel := context.WithCancel(ctx)
	runnerDone := make(chan error, 1)
	go func() { runnerDone <- runner.Run(runnerContext) }()

	bridge := newLiveDeviceProbeSessionBridge(runner, resources.outputLink, resources.sink)
	bridgeContext, bridgeCancel := context.WithCancel(ctx)
	go bridge.Run(bridgeContext)

	runnerFinished := make(chan struct{})
	go func() {
		runnerErr := <-runnerDone
		if runnerErr != nil && !errors.Is(runnerErr, context.Canceled) && !errors.Is(runnerErr, context.DeadlineExceeded) {
			bridge.setError(fmt.Errorf("session runner: %w", runnerErr))
		}
		bridge.finishResponse()
		close(runnerFinished)
	}()
	stop := func() {
		bridgeCancel()
		runnerCancel()
	}
	return runner, bridge, runnerCancel, stop, runnerFinished
}
