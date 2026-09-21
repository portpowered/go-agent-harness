package service

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

func TestPrepareSeedRejectsMissingInferencer(t *testing.T) {
	_, err := New(Dependencies{}).Prepare(context.Background(), sessionturn.Request{
		Seed: sessionturn.Seed{Present: true, Value: "customer prompt"},
	})
	if !errors.Is(err, sessionturn.ErrMissingTurnInferencer) {
		t.Fatalf("Prepare error = %v, want %v", err, sessionturn.ErrMissingTurnInferencer)
	}
}
