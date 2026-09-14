package wire

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestNewServiceBuildsIndependentFactories(t *testing.T) {
	first := NewService()
	second := NewService()
	if first == nil || second == nil {
		t.Fatalf("services = %v, %v; want constructed services", first, second)
	}
}

func TestPublicWireFactoriesExposeIndependentContracts(t *testing.T) {
	if NewLiveRecorder(sessiontrace.LiveRecorderOptions{}) == nil {
		t.Fatal("NewLiveRecorder returned nil")
	}
	if NewRuntimeRecorder(nil, clock.Real{}) != nil {
		t.Fatal("NewRuntimeRecorder accepted a nil observer")
	}
	if NewProviderWireDialer(nil, nil, clock.Real{}) != nil {
		t.Fatal("NewProviderWireDialer accepted nil dependencies")
	}
	if _, err := NewReplayMetricsCollector(sessiontrace.MetricsCollectorOptions{}).Collect(context.Background(), "fixture", "prompt"); err == nil {
		t.Fatal("NewReplayMetricsCollector returned a nil error for missing dependencies")
	}
	if NewPlaybackDiagnostics(sessiontrace.PlaybackDiagnosticsOptions{}) == nil {
		t.Fatal("NewPlaybackDiagnostics returned nil")
	}
	if NewObserver(sessiontrace.NewObserverOptions{}) == nil {
		t.Fatal("NewObserver returned nil")
	}

	called := 0
	sink := sessiontrace.DiagnosticFunc(func(sessiontrace.DiagnosticRecord) { called++ })
	if CombineDiagnosticSinks(nil) != nil {
		t.Fatal("CombineDiagnosticSinks(nil) returned a sink")
	}
	if combined := CombineDiagnosticSinks(sink); combined == nil {
		t.Fatal("CombineDiagnosticSinks lost its only sink")
	} else {
		combined.RecordSessionDiagnostic(sessiontrace.DiagnosticRecord{Event: "test"})
	}
	if combined := CombineDiagnosticSinks(sink, sink); combined == nil {
		t.Fatal("CombineDiagnosticSinks lost its fanout")
	} else {
		combined.RecordSessionDiagnostic(sessiontrace.DiagnosticRecord{Event: "test"})
	}
	if called != 3 {
		t.Fatalf("diagnostic fanout calls = %d, want 3", called)
	}

	firstErr := errors.New("first")
	first := make(chan error, 1)
	first <- firstErr
	if got := <-MergeErrorChannels(context.Background(), first, nil); !errors.Is(got, firstErr) {
		t.Fatalf("merged error = %v, want first error", got)
	}
	if NewCancellationIntent() == nil || LivenessClockFromSource(clock.Real{}) == nil {
		t.Fatal("wire did not construct cancellation/liveness dependencies")
	}
	livenessErr := &sessiontrace.LivenessError{
		Classification:     "timeout",
		TerminalReason:     messages.TerminalReasonTerminalFailure,
		TerminalProvenance: messages.TerminalProvenanceSession,
		OutputState:        messages.TerminalOutputNone,
	}
	classification, reason, provenance, output := LivenessMetadata(livenessErr)
	if classification != "timeout" || reason != messages.TerminalReasonTerminalFailure || provenance != messages.TerminalProvenanceSession || output != messages.TerminalOutputNone {
		t.Fatalf("liveness metadata = %q/%q/%q/%q", classification, reason, provenance, output)
	}
	if classification, reason, provenance, output := LivenessMetadata(errors.New("ordinary")); classification != "" || reason != "" || provenance != "" || output != "" {
		t.Fatalf("ordinary error metadata = %q/%q/%q/%q, want empty", classification, reason, provenance, output)
	}
	if got := OutputStateForProgress(false, 2); got != string(messages.TerminalOutputNone) {
		t.Fatalf("closed progress output state = %q", got)
	}
	if got := OutputStateForProgress(true, 1); got != string(messages.TerminalOutputPartial) {
		t.Fatalf("active progress output state = %q", got)
	}
	if unresolved := NewUnresolvedToolResultsError([]string{"b", "a"}, map[string]messages.SessionSendStatus{"a": messages.SessionSendTimedOut}); unresolved == nil || unresolved.Error() == "" {
		t.Fatal("NewUnresolvedToolResultsError did not return a typed error")
	}
}
