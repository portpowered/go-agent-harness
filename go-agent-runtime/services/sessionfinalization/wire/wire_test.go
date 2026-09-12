package wire

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfinalization"
)

func TestNewServiceProvidesPublicContract(t *testing.T) {
	s := NewService()
	if s == nil {
		t.Fatal("NewService returned nil")
	}
	f := s.NewFinalizer(sessionfinalization.FinalizerRequest{})
	if err := f.Finish(context.Background(), nil, nil); err != nil {
		t.Fatalf("empty finalizer = %v", err)
	}
	b := s.NewTerminationBoundary(context.Background(), sessionfinalization.TerminationRequest{WaitForStragglers: func(sessionfinalization.DrainPolicy) error { return nil }})
	if err := b.Terminate(nil); err != nil {
		t.Fatalf("empty termination = %v", err)
	}
}
