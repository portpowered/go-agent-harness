package durationrun_test

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live/durationrun"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

func TestDurationRunnerContractPreservesLoopCreationFailure(t *testing.T) {
	want := errors.New("loop unavailable")
	runner := durationrun.NewDurationRunner(failingDurationService{err: want}, failedLoopFactory{err: want})
	var contract session.DurationRunner = runner

	_, err := contract.RunDuration(session.DurationRunRequest{
		Context:    context.Background(),
		Inferencer: unusedInferencer{},
	})
	if !errors.Is(err, want) {
		t.Fatalf("RunDuration() error = %v, want to preserve %v", err, want)
	}
}

type failingDurationService struct {
	sessionduration.Service
	err error
}

func (s failingDurationService) RunWithResult(request sessionduration.RunRequest) (sessionduration.Result, error) {
	_, err := request.LoopFactory(request.Context, nil, nil)
	return sessionduration.Result{}, err
}

func (s failingDurationService) Complete(request sessionduration.CompletionRequest) error {
	return request.RunError
}

type failedLoopFactory struct{ err error }

func (f failedLoopFactory) Build(context.Context, messages.SessionInferencer, sessionduration.DuplexLoopOptions) (sessionduration.Loop, error) {
	return nil, f.err
}

type unusedInferencer struct{}

func (unusedInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return nil, errors.New("provider connection was not expected")
}
