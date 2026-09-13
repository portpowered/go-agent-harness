package wire

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type acceptingModelAdmission struct{}

func (acceptingModelAdmission) ValidateSessionModel(string, string) error { return nil }

func TestNewServiceWiresDependenciesAndEvidenceFactory(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("session factory failure")
	factoryCalled := false
	service := NewService(Dependencies{
		Clock:          clock.Real{},
		ModelAdmission: acceptingModelAdmission{},
		Sessions: selfplay.SessionFactoryFunc(func(context.Context, selfplay.SessionRequest) (messages.SessionInferencer, error) {
			factoryCalled = true
			return nil, wantErr
		}),
		Runner: selfplay.SessionRunnerFunc(func(context.Context, messages.SessionInferencer, selfplay.SessionRunOptions) error {
			return errors.New("runner should not be reached")
		}),
	})
	if service == nil {
		t.Fatal("NewService returned nil")
	}
	if NewEvidenceFactory() == nil {
		t.Fatal("NewEvidenceFactory returned nil")
	}

	_, err := service.RunWithResult(context.Background(), io.Discard, selfplay.RunOptions{
		Provider:    selfplay.SelfPlayDefaultProvider,
		Model:       selfplay.SelfPlayDefaultModel,
		OutputDir:   t.TempDir(),
		MaxDuration: time.Second,
		MaxTurns:    1,
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("service error = %v, want %v", err, wantErr)
	}
	if !factoryCalled {
		t.Fatal("wired session factory was not invoked")
	}
}

var _ providers.ModelAdmission = acceptingModelAdmission{}
