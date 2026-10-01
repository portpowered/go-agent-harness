package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

// Context ownership: the service rejects a nil caller context instead of
// substituting a root context, and cleanup keeps running under a caller
// context that is already cancelled.

func TestServiceRejectsNilCallerContext(t *testing.T) {
	service := New()
	var nilContext context.Context
	if err := service.Run(nilContext, sessionduration.RunRequest{Inferencer: contractInferencer{session: newContractSession()}}); !errors.Is(err, sessionduration.ErrContextRequired) {
		t.Fatalf("Run(nil ctx) = %v, want ErrContextRequired", err)
	}
	if controller, err := service.Begin(nilContext, sessionduration.Options{}); controller != nil || !errors.Is(err, sessionduration.ErrContextRequired) {
		t.Fatalf("Begin(nil ctx) = %v, %v, want ErrContextRequired", controller, err)
	}
	if got := WithSessionDurationArtifactPaths(nilContext, sessionduration.SessionDurationArtifactPaths{AudioPath: "audio.wav"}); got != nil {
		t.Fatalf("WithSessionDurationArtifactPaths(nil) = %v, want nil left for Run to reject", got)
	}
}

func TestControllerFinalizePreservesPrimaryAndCleanupErrors(t *testing.T) {
	primary := errors.New("provider cause")
	drainErr := errors.New("drain cause")
	closeErr := errors.New("close cause")
	artifactErr := errors.New("artifact cause")
	controller, err := New().Begin(context.Background(), sessionduration.Options{})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	order := make([]string, 0, 4)
	var nilContext context.Context
	if _, err := controller.Finalize(nilContext, sessionduration.FinalizeRequest{Primary: primary}); !errors.Is(err, primary) || !errors.Is(err, sessionduration.ErrContextRequired) {
		t.Fatalf("Finalize(nil ctx) = %v, want primary and context-required identities", err)
	}
	// Finalization detaches from caller cancellation, so an already-canceled
	// caller context still runs every cleanup step.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	result, finalErr := controller.Finalize(canceled, sessionduration.FinalizeRequest{
		Primary: primary,
		Drain:   func(context.Context) error { order = append(order, "drain"); return drainErr },
		Close:   func() error { order = append(order, "close"); return closeErr },
		Artifacts: artifactLifecycleFunc{
			accept: func(messages.StreamMessage) error { return nil },
			flush:  func() error { order = append(order, "flush"); return artifactErr },
			close:  func() error { order = append(order, "artifact-close"); return nil },
		},
	})
	if !errors.Is(finalErr, primary) || !errors.Is(finalErr, drainErr) || !errors.Is(finalErr, closeErr) || !errors.Is(finalErr, artifactErr) {
		t.Fatalf("final error = %v, lost cleanup identity", finalErr)
	}
	if got, want := result.OutputState, messages.TerminalOutputNone; got != want {
		t.Fatalf("result output state = %q, want %q", got, want)
	}
	if got, want := order, []string{"drain", "close", "flush", "artifact-close"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
		t.Fatalf("cleanup order = %v, want %v", got, want)
	}
}

func TestFinalizerOrdersOwnedCleanupAndIsIdempotent(t *testing.T) {
	var order []string
	step := func(name string) func() error {
		return func() error {
			order = append(order, name)
			return nil
		}
	}
	finalizer := New().NewFinalizer(sessionduration.FinalizationPorts{
		CloseCapabilities: step("capabilities"),
		CloseSession:      step("session"),
		CloseRuntime:      step("runtime"),
		FlushCapture:      step("flush"),
		Finalize: func(_ context.Context, out io.Writer) error {
			if out == nil {
				t.Fatal("finalizer received a nil output writer")
			}
			order = append(order, "finalize")
			return nil
		},
		ReleaseCapture: step("release"),
	})
	finalizer.SetDeviceBinding(step("binding"))
	primary := errors.New("primary")
	var nilContext context.Context
	if err := finalizer.Finish(nilContext, &bytes.Buffer{}, primary); !errors.Is(err, primary) || !errors.Is(err, sessionduration.ErrContextRequired) {
		t.Fatalf("Finish(nil ctx) = %v, want primary and context-required identities", err)
	}
	if len(order) != 0 {
		t.Fatalf("Finish(nil ctx) ran cleanup %v", order)
	}
	if err := finalizer.Finish(context.Background(), &bytes.Buffer{}, primary); !errors.Is(err, primary) {
		t.Fatalf("Finish() = %v, want primary identity", err)
	}
	if err := finalizer.Finish(context.Background(), nil, nil); err != nil {
		t.Fatalf("duplicate Finish() = %v, want nil", err)
	}
	want := []string{"capabilities", "session", "binding", "runtime", "flush", "finalize", "release"}
	if len(order) != len(want) {
		t.Fatalf("cleanup calls = %v, want %v", order, want)
	}
	for index := range want {
		if order[index] != want[index] {
			t.Fatalf("cleanup order = %v, want %v", order, want)
		}
	}
}
