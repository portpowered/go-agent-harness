package sessionduration

import (
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

func TestContractTypesRemainHostNeutral(t *testing.T) {
	var _ MessageWriter = func(messages.StreamMessage) error { return nil }
	var _ ArtifactWriter = artifactProbe{}
	var _ State = stateProbe{}
	var _ Service = serviceProbe{}
}

type artifactProbe struct{}

func (artifactProbe) Accept(messages.StreamMessage) error { return nil }

type stateProbe struct{}

func (stateProbe) Observe(messages.StreamMessage)            {}
func (stateProbe) OutputState() messages.TerminalOutputState { return messages.TerminalOutputNone }
func (stateProbe) Admit(bool, messages.StreamMessage) (messages.StreamMessage, bool) {
	return messages.StreamMessage{}, false
}
func (stateProbe) PublishProviderTerminal(Publication) error { return nil }
func (stateProbe) PublishMaxDuration(Publication, messages.TerminalOutputState) error {
	return nil
}
func (stateProbe) Written() bool { return false }

type serviceProbe struct{}

func (serviceProbe) Run(RunRequest) error                     { return nil }
func (serviceProbe) RunWithResult(RunRequest) (Result, error) { return Result{}, nil }
func (serviceProbe) Complete(CompletionRequest) error         { return nil }
func (serviceProbe) Begin(Options) (Controller, error)        { return nil, nil }
func (serviceProbe) NewFinalizer(FinalizationPorts) Finalizer { return nil }
func (serviceProbe) NewState(TerminalSource) State            { return stateProbe{} }
func (serviceProbe) PublishMaxDuration(Publication, messages.TerminalOutputState) error {
	return nil
}
func (serviceProbe) LifecycleError(LifecycleFailures) error { return nil }
func (serviceProbe) TransportError(error) error             { return nil }
func (serviceProbe) ValidateDuration(time.Duration) error   { return nil }
func (serviceProbe) NewEventAdmission() EventAdmission      { return nil }
func (serviceProbe) NewAdmissionInferencer(messages.SessionInferencer, EventAdmission, chan struct{}) AdmissionInferencer {
	return nil
}
func (serviceProbe) NewAdmissionSession(context.Context, messages.Session, EventAdmission, func(error)) AdmissionSession {
	return nil
}
func (serviceProbe) WithArtifacts(context.Context, ArtifactLifecycle) context.Context { return nil }
func (serviceProbe) ArtifactsFromContext(context.Context) ArtifactLifecycle           { return nil }
func (serviceProbe) WithTerminalRecorder(context.Context, TerminalRecorder) context.Context {
	return nil
}
func (serviceProbe) WithArtifactPaths(context.Context, SessionDurationArtifactPaths) context.Context {
	return nil
}
func (serviceProbe) PrepareArtifacts(context.Context) (context.Context, error) { return nil, nil }
func (serviceProbe) FinalizeArtifacts(ArtifactLifecycle) error                 { return nil }
func (serviceProbe) EvaluateRetry(RetryPolicy, *messages.MessageEndValue) RetryDecision {
	return RetryDecision{}
}
func (serviceProbe) IsDurationShutdownMessage(messages.StreamMessage) bool { return false }
func (serviceProbe) IsDurationForwardMessage(messages.StreamMessage) bool  { return false }
func (serviceProbe) RecordingTerminalSummaryFromMessage(messages.StreamMessage) (*transcript.RecordingTerminalSummary, bool, error) {
	return nil, false, nil
}
