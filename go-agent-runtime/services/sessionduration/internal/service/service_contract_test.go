package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

func TestServiceFacadeExposesDurationContract(t *testing.T) {
	service := New()
	if err := service.ValidateDuration(-time.Second); !errors.Is(err, sessionduration.ErrInvalidDuration) {
		t.Fatalf("ValidateDuration() = %v, want invalid-duration identity", err)
	}
	if err := service.ValidateDuration(0); err != nil {
		t.Fatalf("ValidateDuration(0) = %v", err)
	}

	providerTerminal := messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValueWithTerminal(
		"session", "provider", "provider_close", messages.TerminalReasonProviderClose,
		messages.TerminalProvenanceProvider, messages.TerminalOutputComplete,
	)}
	state := service.NewState(sessionduration.TerminalSource{
		Message: func() (messages.StreamMessage, bool) { return providerTerminal, true },
		Matches: func(msg messages.StreamMessage) bool { return msg.Type == messages.StreamTypeSessionClose },
	})
	state.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant})
	if got := state.OutputState(); got != messages.TerminalOutputPartial {
		t.Fatalf("state output = %q, want partial", got)
	}
	if err := state.PublishProviderTerminal(sessionduration.Publication{}); err != nil {
		t.Fatalf("PublishProviderTerminal: %v", err)
	}
	if !state.Written() {
		t.Fatal("state did not retain terminal-written state")
	}
	if _, ok := state.Admit(true, providerTerminal); ok {
		t.Fatal("state admitted a duplicate provider terminal")
	}

	var published messages.StreamMessage
	if err := service.PublishMaxDuration(sessionduration.Publication{Write: func(msg messages.StreamMessage) error {
		published = msg
		return nil
	}}, messages.TerminalOutputPartial); err != nil {
		t.Fatalf("PublishMaxDuration: %v", err)
	}
	if published.Type != messages.StreamTypeSessionClose {
		t.Fatalf("max-duration publication type = %q", published.Type)
	}

	runtimeErr := errors.New("runtime")
	closeErr := errors.New("close")
	if got := service.LifecycleError(sessionduration.LifecycleFailures{Runtime: runtimeErr, Close: closeErr}); !errors.Is(got, runtimeErr) || !errors.Is(got, closeErr) {
		t.Fatalf("LifecycleError() = %v, missing causes", got)
	}
	if service.TransportError(nil) != nil || !errors.Is(service.TransportError(runtimeErr), runtimeErr) {
		t.Fatal("TransportError did not preserve nil and wrapped identities")
	}

	admission := service.NewEventAdmission()
	if admission == nil {
		t.Fatal("NewEventAdmission returned nil")
	}
	wrappedInferencer := service.NewAdmissionInferencer(contractInferencer{session: newContractSession()}, admission, nil)
	if wrappedInferencer == nil {
		t.Fatal("NewAdmissionInferencer returned nil")
	}
	wrapper := service.NewAdmissionSession(context.Background(), newContractSession(), admission, nil)
	if wrapper == nil || !wrapper.SupportsCompleteMessages() || !wrapper.SupportsCompleteMessagesWithoutResponse() {
		t.Fatal("NewAdmissionSession did not preserve complete-message capabilities")
	}
	if err := wrapper.Close(); err != nil {
		t.Fatalf("wrapped session Close: %v", err)
	}
	if service.NewAdmissionInferencer(contractInferencer{session: newContractSession()}, struct{}{}, nil) == nil {
		t.Fatal("NewAdmissionInferencer rejected an opaque admission boundary")
	}

	artifact := NewSessionDurationArtifactSetWithSinks(nil, &artifactTranscriptSink{})
	ctx := service.WithArtifacts(context.Background(), artifact)
	if service.ArtifactsFromContext(ctx) != artifact {
		t.Fatal("ArtifactsFromContext did not return the attached lifecycle")
	}
	ctx = service.WithTerminalRecorder(ctx, &terminalRecorderProbe{})
	if service.ArtifactsFromContext(ctx) == nil {
		t.Fatal("WithTerminalRecorder removed the artifact lifecycle")
	}
	if service.WithTerminalRecorder(ctx, nil) != ctx {
		t.Fatal("WithTerminalRecorder(nil) did not preserve the context")
	}
	ctx = service.WithArtifactPaths(ctx, sessionduration.SessionDurationArtifactPaths{AudioPath: "audio", TranscriptPath: "transcript"})
	if _, ok := ArtifactPathsFromContext(ctx); !ok {
		t.Fatal("WithArtifactPaths did not retain paths")
	}
	if _, err := service.PrepareArtifacts(context.Background()); err != nil {
		t.Fatalf("PrepareArtifacts with no paths: %v", err)
	}
	if err := service.FinalizeArtifacts(nil); err != nil {
		t.Fatalf("FinalizeArtifacts(nil): %v", err)
	}

	retryTerminal := &messages.MessageEndValue{Status: "failed", ProviderErrorCode: "rate_limit_exceeded", ProviderErrorMessage: "please try again in 3s"}
	if decision := service.EvaluateRetry(sessionduration.RetryPolicy{Enabled: true}, retryTerminal); !decision.Eligible || decision.Delay != 3*time.Second {
		t.Fatalf("EvaluateRetry() = %+v", decision)
	}
	statusRetry := &messages.MessageEndValue{Status: "failed", StatusDetails: "code=rate_limit_exceeded,message=please try again in 1.5s"}
	if decision := service.EvaluateRetry(sessionduration.RetryPolicy{Enabled: true}, statusRetry); !decision.Eligible || decision.Delay != 1500*time.Millisecond {
		t.Fatalf("EvaluateRetry(status details) = %+v", decision)
	}
	if decision := service.EvaluateRetry(sessionduration.RetryPolicy{Enabled: true}, nil); decision != (sessionduration.RetryDecision{}) {
		t.Fatalf("EvaluateRetry(nil) = %+v, want empty decision", decision)
	}
	if !service.IsDurationShutdownMessage(providerTerminal) || !service.IsDurationForwardMessage(messages.StreamMessage{Type: messages.StreamTypeError}) {
		t.Fatal("duration message classification did not preserve terminal/error forwarding")
	}
	if summary, present, err := service.RecordingTerminalSummaryFromMessage(providerTerminal); err != nil || !present || summary == nil {
		t.Fatalf("RecordingTerminalSummaryFromMessage() = %+v, %v, %v", summary, present, err)
	}
	if _, present, err := service.RecordingTerminalSummaryFromMessage(messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: &messages.SessionCloseValue{Classification: "incomplete"}}); present || err == nil {
		t.Fatalf("invalid terminal summary = present:%v err:%v, want validation error", present, err)
	}
}
