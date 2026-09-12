package agentruntime

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providerliveness"
	providerlivenesswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providerliveness/wire"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

const SessionSilentProviderEmptyResponseClassification = providerliveness.SilentProviderEmptyResponseClassification
const SessionSilentProviderTimeoutClassification = providerliveness.SilentProviderTimeoutClassification

// Deprecated: use providerliveness.Timer at a service boundary.
type SessionLivenessTimer = SessionDurationTimer

// Deprecated: use providerliveness.Clock at a service boundary.
type SessionLivenessClock = SessionDurationClock

func sessionLivenessClockFromSource(source platformclock.Source) SessionLivenessClock {
	if timerSource, ok := platformclock.Ensure(source).(platformclock.TimerSource); ok {
		return timerSource
	}
	return nil
}

var (
	// Deprecated: use providerliveness.ErrSilentProviderEmptyResponse.
	ErrSilentProviderEmptyResponse = providerliveness.ErrSilentProviderEmptyResponse
	// Deprecated: use providerliveness.ErrSilentProviderTimeout.
	ErrSilentProviderTimeout = providerliveness.ErrSilentProviderTimeout
)

// Deprecated: use providerliveness.Error.
type SessionLivenessError = providerliveness.Error

func sessionLivenessMetadata(err error) (string, messages.TerminalReason, messages.TerminalProvenance, messages.TerminalOutputState) {
	var typed *providerliveness.Error
	if !errors.As(err, &typed) || typed == nil {
		return "", "", "", ""
	}
	return typed.Classification, messages.TerminalReason(typed.TerminalReason), messages.TerminalProvenance(typed.TerminalProvenance), messages.TerminalOutputState(typed.OutputState)
}

type providerLivenessAdapter struct {
	SessionLivenessClock
	service providerliveness.Service
}

func (o *sessionProgressObserver) providerLiveness() providerliveness.Service {
	if o == nil {
		return nil
	}
	o.livenessMu.Lock()
	defer o.livenessMu.Unlock()
	if adapter, ok := o.livenessClock.(*providerLivenessAdapter); ok && adapter.service != nil {
		return adapter.service
	}
	source := o.livenessClock
	if source == nil {
		source = realSessionDurationClock{}
	}
	adapter := &providerLivenessAdapter{SessionLivenessClock: source}
	adapter.service = providerlivenesswire.NewService(providerliveness.Dependencies{Clock: providerliveness.ClockAdapter[SessionLivenessTimer]{Source: source}, OnFailure: func(err error) { o.recordProviderLivenessFailure(err) }})
	o.livenessClock = adapter
	return adapter.service
}
func (o *sessionProgressObserver) recordProviderLivenessFailure(err error) {
	if o == nil {
		return
	}
	var typed *providerliveness.Error
	if !errors.As(err, &typed) || typed == nil {
		return
	}
	failingEvent := string(messages.StreamTypeMessageEnd)
	if typed.Classification == providerliveness.SilentProviderTimeoutClassification {
		failingEvent = failingEventRun
	}
	o.livenessMu.Lock()
	if o.failure == nil {
		o.failure = &failureFacts{classification: typed.Classification, terminalReason: typed.TerminalReason, provenance: typed.TerminalProvenance, outputState: typed.OutputState, failingEvent: failingEvent}
	}
	notify := o.livenessObserver
	o.livenessMu.Unlock()
	if notify != nil {
		notify(err)
	}
}
func (o *sessionProgressObserver) setLivenessClock(clock SessionLivenessClock) {
	if o == nil || clock == nil {
		return
	}
	o.livenessMu.Lock()
	if adapter, ok := o.livenessClock.(*providerLivenessAdapter); ok {
		service := adapter.service
		adapter.SessionLivenessClock = clock
		o.livenessMu.Unlock()
		service.SetClock(providerliveness.ClockAdapter[SessionLivenessTimer]{Source: clock})
		return
	}
	o.livenessClock = clock
	o.livenessMu.Unlock()
}
func (o *sessionProgressObserver) livenessFailure() error {
	return o.providerLiveness().Failure()
}
func (o *sessionProgressObserver) livenessEvents() <-chan struct{} {
	return o.providerLiveness().Events()
}
func sessionLivenessErrorChannel(ctx context.Context, observer *sessionProgressObserver) <-chan error {
	if observer == nil {
		return nil
	}
	return observer.providerLiveness().FailureChannel(ctx)
}

func mergeSessionErrorChannels(ctx context.Context, first, second <-chan error) <-chan error {
	return (providerliveness.ErrorChannels{First: first, Second: second}).Merge(ctx)
}

func (o *sessionProgressObserver) responseHasToolLifecycleObligation() bool {
	if o == nil {
		return false
	}
	o.toolStateMu.Lock()
	defer o.toolStateMu.Unlock()
	if o.toolCallInTurn || len(o.unresolvedToolCalls) > 0 {
		return true
	}
	for _, state := range o.toolContinuations {
		if state != nil && state.resultAccepted && !state.continuationComplete {
			return true
		}
	}
	return false
}
func providerEvent(msg messages.StreamMessage) providerliveness.Event {
	return providerliveness.Event{Kind: providerliveness.EventKind(msg.Type), Role: providerliveness.EventRole(msg.Role), ResponseID: msg.ResponseID, ToolAcknowledgement: msg.ResponsePurpose == messages.ResponsePurposeToolAcknowledgement}
}
func (o *sessionProgressObserver) observeSilentProviderEmptyResponse(msg messages.StreamMessage, value *messages.MessageEndValue, outputPresent, toolObligation bool) {
	if o == nil || value == nil {
		return
	}
	o.providerLiveness().ObserveResponseEnd(providerliveness.ResponseEnd{Event: providerEvent(msg), Status: value.Status, TerminalReason: string(value.TerminalReason), TerminalProvenance: string(value.TerminalProvenance), OutputState: string(value.OutputState), Usage: providerliveness.TokenUsage{PromptTokens: value.Usage.PromptTokens, CompletionTokens: value.Usage.CompletionTokens, TotalTokens: value.Usage.TotalTokens, ReasoningTokens: value.Usage.ReasoningTokens}, OutputPresent: outputPresent, ToolObligation: toolObligation})
}
func (o *sessionProgressObserver) withLiveness(fn func(providerliveness.Service)) {
	if o != nil {
		fn(o.providerLiveness())
	}
}
func (o *sessionProgressObserver) armProviderProgress() { o.withLiveness(providerliveness.Service.Arm) }
func (o *sessionProgressObserver) disarmProviderProgress() {
	o.withLiveness(providerliveness.Service.Disarm)
}
func (o *sessionProgressObserver) beginLocalToolExecution() {
	o.withLiveness(providerliveness.Service.BeginLocalToolExecution)
}
func (o *sessionProgressObserver) endLocalToolExecution() {
	o.withLiveness(providerliveness.Service.EndLocalToolExecution)
}
func (o *sessionProgressObserver) stopLiveness() { o.withLiveness(providerliveness.Service.Stop) }
func (o *sessionProgressObserver) observeProviderEvent(msg messages.StreamMessage) {
	if s := o.providerLiveness(); s != nil {
		s.ObserveProviderEvent(providerEvent(msg))
	}
}
func (o *sessionProgressObserver) observeProviderDispatch(msg messages.StreamMessage) {
	if s := o.providerLiveness(); s != nil {
		s.ObserveProviderDispatch(providerEvent(msg))
	}
}
func applyRoomParticipantTerminalMetadata(result *RoomParticipantResult, lifecycle *roomParticipantLifecycle, err error) {
	if result == nil {
		return
	}
	classification, reason, provenance, output := "", messages.TerminalReason(""), messages.TerminalProvenance(""), messages.TerminalOutputState("")
	if lifecycle != nil {
		classification, reason, provenance, output = lifecycle.terminalMetadata()
	}
	if classification == "" {
		classification, reason, provenance, output = sessionLivenessMetadata(err)
	}
	if classification == "" {
		return
	}
	result.Classification, result.TerminalReason, result.TerminalProvenance, result.OutputState = classification, string(reason), string(provenance), string(output)
}
