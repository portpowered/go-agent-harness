package wire

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfinalization"
)

type helperCloser struct{ calls int }

func (c *helperCloser) Close() error { c.calls++; return nil }

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

func TestCompositionHelpersPreserveContractSemantics(t *testing.T) {
	closer := &helperCloser{}
	cleanup := OptionalCloser(closer)
	if cleanup == nil {
		t.Fatal("closable value produced no cleanup")
	}
	if err := cleanup(); err != nil || closer.calls != 1 {
		t.Fatalf("closer cleanup = %v, calls=%d", err, closer.calls)
	}
	if OptionalCloser(nil) != nil || OptionalCloser(struct{}{}) != nil {
		t.Fatal("non-closable values produced cleanup")
	}
	if err := Invoke(nil); err != nil {
		t.Fatalf("nil invoke = %v", err)
	}
	if err := Invoke(func() error { panic("wire helper panic") }); !errors.Is(err, sessionfinalization.ErrFinalizationPanic) {
		t.Fatalf("invoke panic = %v", err)
	}
	request := NewFinalizerRequest(nil, cleanup, nil, nil, nil, func(context.Context, io.Writer) error { return nil }, nil, nil, nil, nil)
	if request.CloseSession == nil || request.Finalize == nil {
		t.Fatalf("request adapter lost callbacks: %#v", request)
	}
	adapted := AdaptDrain(func(policy struct{ quietPeriod int }) error {
		if policy.quietPeriod != 25 {
			t.Fatalf("adapted policy = %#v", policy)
		}
		return nil
	}, func(policy sessionfinalization.DrainPolicy) struct{ quietPeriod int } {
		return struct{ quietPeriod int }{quietPeriod: int(policy.QuietPeriod / time.Second)}
	})
	if err := adapted(sessionfinalization.DrainPolicy{QuietPeriod: 25 * time.Second}); err != nil {
		t.Fatalf("adapted drain = %v", err)
	}
	if AdaptDrain[struct{}](nil, func(sessionfinalization.DrainPolicy) struct{} { return struct{}{} }) != nil {
		t.Fatal("nil drain callback was adapted")
	}
}
