package runner_test

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	durationservice "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/internal/service"
)

func TestPublicDurationServicePreservesLoopCreationFailure(t *testing.T) {
	want := errors.New("loop unavailable")
	service := durationservice.New()
	_, err := service.RunWithResult(sessionduration.RunRequest{
		Context:    context.Background(),
		Inferencer: unusedInferencer{},
		LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
			return nil, want
		},
	})
	if !errors.Is(err, want) {
		t.Fatalf("RunWithResult() error = %v, want to preserve %v", err, want)
	}
}

type unusedInferencer struct{}

func (unusedInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return nil, errors.New("provider connection was not expected")
}
