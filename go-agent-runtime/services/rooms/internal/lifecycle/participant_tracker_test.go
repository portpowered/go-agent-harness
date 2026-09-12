package lifecycle

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type participantTestSession struct {
	mu                   sync.Mutex
	receive              *messages.TypedBuffer[messages.StreamMessage]
	done                 chan struct{}
	doneOnce             sync.Once
	closeErr             error
	sendErr              error
	closeCalls           int
	responseRequests     int
	sends                []messages.StreamMessage
	completeSends        []messages.Message
	completeNoResponse   []messages.Message
	responseRequestsOK   bool
	completeOK           bool
	completeNoResponseOK bool
}

func newParticipantTestSession() *participantTestSession {
	return &participantTestSession{
		receive:              messages.NewTypedBuffer[messages.StreamMessage](8),
		done:                 make(chan struct{}),
		responseRequestsOK:   true,
		completeOK:           true,
		completeNoResponseOK: true,
	}
}

func (s *participantTestSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

func (s *participantTestSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	if err := ctx.Err(); err != nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: err}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sends = append(s.sends, msg)
	if s.sendErr != nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure, Err: s.sendErr}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

func (s *participantTestSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}
func (s *participantTestSession) Done() <-chan struct{} { return s.done }

func (s *participantTestSession) Close() error {
	s.mu.Lock()
	s.closeCalls++
	err := s.closeErr
	s.mu.Unlock()
	s.doneOnce.Do(func() { close(s.done) })
	return err
}

func (s *participantTestSession) RequestResponse(context.Context) messages.SessionSendOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responseRequests++
	if !s.responseRequestsOK {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

func (s *participantTestSession) SupportsResponseRequests() bool { return s.responseRequestsOK }

func (s *participantTestSession) SendMessage(_ context.Context, msg messages.Message) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completeSends = append(s.completeSends, msg)
	return s.completeOK
}

func (s *participantTestSession) SendMessageWithoutResponse(_ context.Context, msg messages.Message) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completeNoResponse = append(s.completeNoResponse, msg)
	return s.completeNoResponseOK
}

func (s *participantTestSession) SupportsCompleteMessages() bool { return s.completeOK }
func (s *participantTestSession) SupportsCompleteMessagesWithoutResponse() bool {
	return s.completeNoResponseOK
}
func (s *participantTestSession) TerminalError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sendErr
}

type participantTestInferencer struct {
	session messages.Session
	err     error
}

func firstParticipantToolCallID(primary, fallback string) string {
	if validID(primary) {
		return primary
	}
	return fallback
}
func participantTerminalProvenance(disposition, reason string) string {
	return termProv(disposition, reason)
}
func participantOutputState(opened bool, turns int) string { return outputState(opened, turns) }
func participantBoundTerminationTrigger(reason rooms.RoomTerminationReason, mid bool) string {
	return boundTrigger(reason, mid)
}
func classifyParticipantSessionClose(closeReason string, terminalReason messages.TerminalReason) rooms.ParticipantTerminationReason {
	return classClose(closeReason, terminalReason)
}
func participantCancellationOnly(err error) bool { return cancelOnly(err) }
func participantCompleteMessageCapabilitiesForSession(session messages.Session) (bool, bool) {
	return capabilities(session)
}

func (i participantTestInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, i.err
}

func participantMessageStart(responseID string) messages.StreamMessage {
	return messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageStartValue()}
}

func participantToolStart(id string) messages.StreamMessage {
	return messages.StreamMessage{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, ToolCallId: id, Value: messages.NewToolCallStartValue(id, "lookup")}
}

func participantToolEnd(id string) messages.StreamMessage {
	return messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ToolCallId: id, Value: messages.NewToolCallEndValue(id, "lookup", "{}")}
}

func TestParticipantTrackerConnectionOwnsEverySessionOutcome(t *testing.T) {
	testParticipantConnectionSuccess(t)
	testParticipantConnectionFailure(t)
	testParticipantConnectionInvalid(t)
}

func testParticipantConnectionSuccess(t *testing.T) {
	stateChanged := make(chan struct{}, 8)
	lifecycle := NewParticipantLifecycle(rooms.ParticipantLifecycleOptions{StateChanged: stateChanged})
	connected := newParticipantTestSession()
	tracker := NewConnectionTracker(participantTestInferencer{session: connected}, lifecycle, nil)
	outcomes := make(chan error, 1)
	tracker.SetOutcomeSink(func(err error) { outcomes <- err })
	session, err := tracker.ConnectSession(context.Background())
	if err != nil || session == nil {
		t.Fatalf("connect success = session %v, err %v", session, err)
	}
	if got, ready := tracker.Outcome(); !ready || got != nil {
		t.Fatalf("success outcome = %v, ready=%v", got, ready)
	}
	if got := <-outcomes; got != nil {
		t.Fatalf("published success = %v", got)
	}
	owned := lifecycle.OwnedSessionSnapshot()
	if !owned.Created || owned.Closed {
		t.Fatalf("owned session after connect = %+v", owned)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	connected.mu.Lock()
	closeCalls := connected.closeCalls
	connected.mu.Unlock()
	if closeCalls != 1 {
		t.Fatalf("underlying close calls = %d, want one", closeCalls)
	}
	tracker.SetOutcomeSink(func(error) {})
	if owned := lifecycle.OwnedSessionSnapshot(); !owned.Closed {
		t.Fatalf("close ownership not recorded: %+v", owned)
	}
}

func testParticipantConnectionFailure(t *testing.T) {
	closeErr := errors.New("close failed")
	connectErr := errors.New("connect failed")
	failedSession := newParticipantTestSession()
	failedSession.closeErr = closeErr
	failedLifecycle := NewParticipantLifecycle(rooms.ParticipantLifecycleOptions{})
	failedTracker := NewConnectionTracker(participantTestInferencer{session: failedSession, err: connectErr}, failedLifecycle, nil)
	failed, err := failedTracker.ConnectSession(context.Background())
	if failed != nil || !errors.Is(err, connectErr) || !errors.Is(err, closeErr) {
		t.Fatalf("error-plus-session result = session %v, err %v", failed, err)
	}
	failedSession.mu.Lock()
	closeCalls := failedSession.closeCalls
	failedSession.mu.Unlock()
	if closeCalls != 1 {
		t.Fatalf("error-plus-session close calls = %d, want one", closeCalls)
	}
	if got, ready := failedTracker.Outcome(); !ready || !errors.Is(got, connectErr) {
		t.Fatalf("failed outcome = %v, ready=%v", got, ready)
	}
}

func testParticipantConnectionInvalid(t *testing.T) {
	for name, inferencer := range map[string]messages.SessionInferencer{
		"nil inferencer": nil,
		"nil session":    participantTestInferencer{},
	} {
		t.Run(name, func(t *testing.T) {
			tracker := NewConnectionTracker(inferencer, nil, nil)
			if _, err := tracker.ConnectSession(context.Background()); err == nil {
				t.Fatal("connect unexpectedly succeeded")
			}
			if outcomeErr, ready := tracker.Outcome(); !ready || outcomeErr == nil {
				t.Fatal("failure outcome was not published")
			}
		})
	}
}

func TestParticipantTrackerAdmissionToolContinuationAndIdempotentCancel(t *testing.T) {
	admissionClosed := make(chan struct{})
	lifecycle := NewParticipantLifecycle(rooms.ParticipantLifecycleOptions{AdmissionClosed: admissionClosed})
	underlying := newParticipantTestSession()
	tracked := NewTrackedSession(underlying, lifecycle, admissionClosed)
	lifecycle.SetOwnedSession(tracked)
	lifecycle.MarkSessionCreated()
	lifecycle.Observe(participantMessageStart("response-1"))
	lifecycle.Observe(participantToolStart("call-1"))
	lifecycle.Observe(participantToolStart("call-2"))
	lifecycle.Observe(participantToolStart("call-3"))
	close(admissionClosed)
	lifecycle.MarkCoordinatorStopping(true, rooms.RoomTerminationMaxTurnsReached)

	if !tracked.SessionAdmissionAllows(messages.StreamMessage{Type: messages.StreamTypeResponseCancel}) || !tracked.SessionAdmissionAllows(messages.StreamMessage{Type: messages.StreamTypeSessionClose}) {
		t.Fatal("control messages were rejected after admission closed")
	}
	if !tracked.SessionAdmissionAllows(participantToolEnd("call-1")) || tracked.SessionAdmissionAllows(participantToolEnd("unknown")) {
		t.Fatal("tool admission did not preserve only the pending call")
	}
	if !tracked.SessionAdmissionAllowsCompleteMessage(messages.Message{ToolCallID: "call-2"}) {
		t.Fatal("pending complete tool result was rejected during grace")
	}
	if tracked.SessionAdmissionAllowsCompleteMessage(messages.Message{ToolCallID: "unknown"}) {
		t.Fatal("unknown complete tool result was admitted")
	}
	if outcome := tracked.SendWithOutcome(context.Background(), participantToolEnd("call-1")); !outcome.OK() {
		t.Fatalf("tool result send = %+v", outcome)
	}
	if outcome := tracked.SendWithOutcome(context.Background(), messages.StreamMessage{Type: messages.StreamTypeResponseCreate}); !outcome.OK() {
		t.Fatalf("continuation request = %+v", outcome)
	}
	if tracked.SessionAdmissionAllows(messages.StreamMessage{Type: messages.StreamTypeResponseCreate}) {
		t.Fatal("duplicate continuation was admitted")
	}
	if !tracked.SupportsResponseRequests() || !tracked.SupportsCompleteMessages() || !tracked.SupportsCompleteMessagesWithoutResponse() {
		t.Fatal("optional session capabilities were not forwarded")
	}

	cancelAdmission := make(chan struct{})
	cancelLifecycle := NewParticipantLifecycle(rooms.ParticipantLifecycleOptions{AdmissionClosed: cancelAdmission})
	cancelUnderlying := newParticipantTestSession()
	cancelTracked := NewTrackedSession(cancelUnderlying, cancelLifecycle, cancelAdmission)
	cancelLifecycle.SetOwnedSession(cancelTracked)
	cancelLifecycle.Observe(participantMessageStart("response-2"))
	close(cancelAdmission)
	cancelLifecycle.MarkCoordinatorStopping(true, rooms.RoomTerminationMaxDurationReached)
	cancelLifecycle.MarkBoundCancellation()
	cancelLifecycle.CancelActiveResponse()
	cancelLifecycle.CancelActiveResponse()
	cancelUnderlying.mu.Lock()
	var cancelCount int
	for _, message := range cancelUnderlying.sends {
		if message.Type == messages.StreamTypeResponseCancel {
			cancelCount++
		}
	}
	cancelUnderlying.mu.Unlock()
	if cancelCount != 1 {
		t.Fatalf("response cancel sends = %d, want one", cancelCount)
	}
	completeTracked := NewTrackedSession(newParticipantTestSession(), NewParticipantLifecycle(rooms.ParticipantLifecycleOptions{}), nil)
	if !completeTracked.SendMessage(context.Background(), messages.Message{ToolCallID: "call-2"}) || !completeTracked.SendMessageWithoutResponse(context.Background(), messages.Message{ToolCallID: "call-3"}) {
		t.Fatal("complete message capability forwarding failed")
	}
}

func TestParticipantTrackerTerminalCausalityAndSnapshots(t *testing.T) {
	testParticipantProviderCompletion(t)
	testParticipantGraceAndCancellation(t)
	testParticipantFailureAndDisconnect(t)
	testParticipantLiveness(t)
}

func testParticipantProviderCompletion(t *testing.T) {
	lifecycle := NewParticipantLifecycle(rooms.ParticipantLifecycleOptions{})
	lifecycle.MarkConnected(nil)
	lifecycle.MarkSessionCreated()
	lifecycle.Observe(messages.StreamMessage{Type: messages.StreamTypeSessionOpen})
	lifecycle.Observe(participantMessageStart("response-3"))
	if got := lifecycle.ObserveAdmittedTurn(); got != 1 {
		t.Fatalf("admitted turns = %d, want one", got)
	}
	if !lifecycle.ObserveTerminal(rooms.SessionTerminalObservation{
		TerminalReason:     string(messages.TerminalReasonProviderAuthoredCompletion),
		TerminalProvenance: string(messages.TerminalProvenanceProvider),
		OutputState:        string(messages.TerminalOutputComplete),
	}) {
		t.Fatal("provider completion was not observed")
	}
	observation := lifecycle.TerminalObservationSnapshot()
	if observation.TerminalReason != string(messages.TerminalReasonProviderAuthoredCompletion) || observation.OutputState != string(messages.TerminalOutputComplete) {
		t.Fatalf("completion observation = %+v", observation)
	}
	snapshot := lifecycle.Snapshot()
	if !snapshot.Connected || !snapshot.SessionOpened || snapshot.Turns != 1 {
		t.Fatalf("lifecycle snapshot = %+v", snapshot)
	}
}

func testParticipantGraceAndCancellation(t *testing.T) {
	grace := NewParticipantLifecycle(rooms.ParticipantLifecycleOptions{})
	grace.Observe(participantMessageStart("response-4"))
	grace.MarkCoordinatorStopping(true, rooms.RoomTerminationMaxDurationReached)
	if !grace.AdmitResponseTerminal() || !grace.ObserveTerminal(rooms.SessionTerminalObservation{
		TerminalReason:     string(messages.TerminalReasonProviderAuthoredCompletion),
		TerminalProvenance: string(messages.TerminalProvenanceProvider),
		OutputState:        string(messages.TerminalOutputComplete),
	}) {
		t.Fatal("bound response did not complete during grace")
	}
	if got := grace.TerminalObservationSnapshot(); got.TerminationDisposition != "completed_during_grace" || got.TerminationTrigger != "max_duration_reached_mid_response" {
		t.Fatalf("grace observation = %+v", got)
	}
	if grace.AdmitResponseTerminal() {
		t.Fatal("terminal remained admitted after grace completion")
	}

	cancelled := NewParticipantLifecycle(rooms.ParticipantLifecycleOptions{})
	cancelled.Observe(participantMessageStart("response-5"))
	cancelled.MarkCoordinatorStopping(true, rooms.RoomTerminationMaxTurnsReached)
	cancelled.MarkBoundCancellation()
	if cancelled.ObserveTerminal(rooms.SessionTerminalObservation{TerminalReason: string(messages.TerminalReasonProviderAuthoredCompletion), OutputState: string(messages.TerminalOutputComplete)}) {
		t.Fatal("late completion replaced bound cancellation")
	}
	if got := cancelled.TerminalObservationSnapshot(); got.TerminationDisposition != "cancelled_after_grace" || got.Classification != rooms.RoomBoundCancelledClassification {
		t.Fatalf("cancellation observation = %+v", got)
	}
}

func testParticipantFailureAndDisconnect(t *testing.T) {
	failure := NewParticipantLifecycle(rooms.ParticipantLifecycleOptions{})
	first := errors.New("first provider failure")
	failure.MarkConnected(first)
	if !failure.ObserveTerminal(rooms.SessionTerminalObservation{Classification: "transport", TerminalReason: string(messages.TerminalReasonTerminalFailure), TerminalProvenance: string(messages.TerminalProvenanceProvider), OutputState: string(messages.TerminalOutputPartial), Err: errors.New("specific provider failure"), Failure: true}) {
		t.Fatal("specific failure did not replace unknown fallback")
	}
	if got := failure.TerminalObservationSnapshot(); got.Classification != "transport" || !got.Failure {
		t.Fatalf("failure observation = %+v", got)
	}
	if reason, terminalErr, observed := failure.Terminal(); !observed || reason != rooms.ParticipantTerminationError || terminalErr == nil {
		t.Fatalf("failure terminal = %q, err=%v, observed=%v", reason, terminalErr, observed)
	}

	disconnected := NewParticipantLifecycle(rooms.ParticipantLifecycleOptions{})
	done := make(chan struct{})
	close(done)
	disconnected.SetTransportDone(done, nil)
	if !disconnected.TransportHasEnded() {
		t.Fatal("closed transport was not observed")
	}
	if disconnected.ObserveTerminal(rooms.SessionTerminalObservation{TerminalReason: string(messages.TerminalReasonProviderClose), FailingEvent: string(messages.StreamTypeSessionClose), Failure: true}) {
		t.Fatal("synthetic provider close became a second failure")
	}
	if reason, terminalErr, observed := disconnected.Terminal(); !observed || reason != rooms.ParticipantTerminationDisconnected || terminalErr != nil {
		t.Fatalf("disconnect terminal = %q, err=%v, observed=%v", reason, terminalErr, observed)
	}
}

func testParticipantLiveness(t *testing.T) {
	liveness := NewParticipantLifecycle(rooms.ParticipantLifecycleOptions{})
	liveness.MarkLivenessFailure(errors.New("silent"), rooms.ParticipantLivenessMetadata{Classification: "silent_provider", TerminalReason: messages.TerminalReasonTerminalFailure, TerminalProvenance: messages.TerminalProvenanceSession, OutputState: messages.TerminalOutputNone})
	classification, reason, provenance, output := liveness.TerminalMetadata()
	if classification != "silent_provider" || reason != messages.TerminalReasonTerminalFailure || provenance != messages.TerminalProvenanceSession || output != messages.TerminalOutputNone {
		t.Fatalf("liveness metadata = %q/%q/%q/%q", classification, reason, provenance, output)
	}
	liveness.MarkRunDone(nil)
	if !liveness.RunHasFinished() {
		t.Fatal("run completion was not recorded")
	}
}

func TestParticipantTrackerEdgeSurfacesAndTaxonomy(t *testing.T) {
	testParticipantEdgeReadinessAndTracked(t)
	testParticipantEdgeTransport(t)
	testParticipantEdgeTaxonomy(t)
}

func testParticipantEdgeReadinessAndTracked(t *testing.T) {
	lifecycle := NewParticipantLifecycle(rooms.ParticipantLifecycleOptions{StateChanged: make(chan struct{}, 1)})
	lifecycle.MarkDeviceReady()
	if !lifecycle.DeviceHasReady() {
		t.Fatal("device readiness was not recorded")
	}
	participantFailure := errors.New("participant failure")
	lifecycle.MarkParticipantFailure(participantFailure)
	if reason, got, observed := lifecycle.Terminal(); !observed || reason != rooms.ParticipantTerminationError || !errors.Is(got, participantFailure) {
		t.Fatalf("participant failure terminal = %q/%v/%v", reason, got, observed)
	}
	if got := lifecycle.TransportTerminalError(); got != nil {
		t.Fatalf("unexpected transport error = %v", got)
	}

	underlying := newParticipantTestSession()
	tracked := NewTrackedSession(underlying, lifecycle, nil)
	lifecycle.SetOwnedSession(tracked)
	if !tracked.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeSessionClose}) {
		t.Fatal("bool send was rejected")
	}
	if outcome := tracked.RequestResponse(context.Background()); !outcome.OK() {
		t.Fatalf("response request = %+v", outcome)
	}
	if tracked.TerminalError() != nil {
		t.Fatal("unexpected tracked terminal error")
	}
	if media, ok := tracked.(interface {
		RTCMedia() (audio.MediaEndpoints, bool)
	}); ok {
		_, _ = media.RTCMedia()
	}
	if err := lifecycle.CloseOwnedSession(); err != nil {
		t.Fatal(err)
	}
	underlying.mu.Lock()
	closeCalls := underlying.closeCalls
	underlying.mu.Unlock()
	if closeCalls != 1 {
		t.Fatalf("owned close calls = %d, want one", closeCalls)
	}
}

func testParticipantEdgeTransport(t *testing.T) {
	done := make(chan struct{})
	transportErr := errors.New("transport failed")
	withTransport := NewParticipantLifecycle(rooms.ParticipantLifecycleOptions{})
	withTransport.SetTransportDone(done, func() error { return transportErr })
	if withTransport.TransportHasEnded() {
		t.Fatal("open transport reported ended")
	}
	close(done)
	if !withTransport.TransportHasEnded() {
		t.Fatal("closed transport was not reported ended")
	}
}

func testParticipantEdgeTaxonomy(t *testing.T) {
	testParticipantTaxonomyProvenance(t)
	testParticipantTaxonomyTriggers(t)
	testParticipantTaxonomyCompatibility(t)
	testParticipantTaxonomyDefaults(t)
}

func testParticipantTaxonomyProvenance(t *testing.T) {
	for _, test := range []struct {
		reason, disposition, want string
	}{
		{string(messages.TerminalReasonProviderAuthoredCompletion), "completed", string(messages.TerminalProvenanceProvider)},
		{string(messages.TerminalReasonLoopSynthesizedCompletion), "completed", string(messages.TerminalProvenanceLoop)},
		{string(messages.TerminalReasonProviderClose), "disconnected", string(messages.TerminalProvenanceSession)},
		{string(messages.TerminalReasonReplayComplete), "completed", string(messages.TerminalProvenanceReplay)},
		{string(messages.TerminalReasonCancellation), "stopped", string(messages.TerminalProvenanceLoop)},
		{string(messages.TerminalReasonTerminalFailure), "failed", string(messages.TerminalProvenanceSession)},
	} {
		if got := participantTerminalProvenance(test.disposition, test.reason); got != test.want {
			t.Fatalf("provenance(%q,%q) = %q, want %q", test.disposition, test.reason, got, test.want)
		}
	}
	if got := participantTerminalProvenance("cancelled_after_grace", ""); got != string(messages.TerminalProvenanceRoom) {
		t.Fatalf("bound provenance = %q", got)
	}
	if participantOutputState(false, 2) != string(messages.TerminalOutputNone) || participantOutputState(true, 0) != string(messages.TerminalOutputNone) || participantOutputState(true, 1) != string(messages.TerminalOutputPartial) {
		t.Fatal("output state taxonomy changed")
	}
}

func testParticipantTaxonomyTriggers(t *testing.T) {
	if participantBoundTerminationTrigger(rooms.RoomTerminationMaxTurnsReached, false) != "max_turns_reached" || participantBoundTerminationTrigger(rooms.RoomTerminationMaxTurnsReached, true) != "max_turns_reached_mid_response" || participantBoundTerminationTrigger(rooms.RoomTerminationMaxDurationReached, false) != "max_duration_reached" || participantBoundTerminationTrigger(rooms.RoomTerminationMaxDurationReached, true) != "max_duration_reached_mid_response" || participantBoundTerminationTrigger(rooms.RoomTerminationStopped, false) != "stopped" {
		t.Fatal("bound trigger taxonomy changed")
	}
	if classifyParticipantSessionClose("provider_closed", "") != rooms.ParticipantTerminationDisconnected || classifyParticipantSessionClose("", messages.TerminalReasonTerminalFailure) != rooms.ParticipantTerminationError || classifyParticipantSessionClose("", messages.TerminalReasonSessionClose) != rooms.ParticipantTerminationEnded {
		t.Fatal("session close taxonomy changed")
	}
}

func testParticipantTaxonomyCompatibility(t *testing.T) {
	if firstParticipantToolCallID(" first ", "fallback") != " first " || firstParticipantToolCallID("", "fallback") != "fallback" {
		t.Fatal("tool call ID fallback changed")
	}
	if !participantCancellationOnly(nil) || !participantCancellationOnly(context.Canceled) || !participantCancellationOnly(errors.Join(context.Canceled, context.DeadlineExceeded)) || participantCancellationOnly(errors.New("not cancellation")) {
		t.Fatal("cancellation taxonomy changed")
	}
	if complete, withoutResponse := participantCompleteMessageCapabilitiesForSession(nil); complete || withoutResponse {
		t.Fatal("nil complete-message capability unexpectedly present")
	}
}

func testParticipantTaxonomyDefaults(t *testing.T) {
	local := &participantLifecycle{}
	if local.has(fbr) && local.has(fbc) {
		t.Fatal("empty bound was not classified as completed")
	}
	local.set(fbr, true)
	local.set(fbc, true)
	if !(local.has(fbr) && local.has(fbc)) {
		t.Fatal("cancelled bound was not classified after grace")
	}
	local.setObservation(obs{b: "stopped"})
	got := local.TerminalObservationSnapshot()
	if got.TerminalReason == "" || got.OutputState == "" || got.TerminalProvenance == "" {
		t.Fatal("terminal defaults were not filled")
	}
}

// TestRunnerRejectsInvalidParticipantBrowserToolsBeforeLiveConstruction
// preserves the legacy room admission guarantee at the public room boundary:
// malformed browser policy is rejected before any participant session is
// opened or host capability is constructed.
func TestRunnerRejectsInvalidParticipantBrowserToolsBeforeLiveConstruction(t *testing.T) {
	service := &fakeLiveService{handles: map[string]*fakeLiveHandle{
		"customer":  newFakeLiveHandle(),
		"assistant": newFakeLiveHandle(),
	}}
	manifest := rooms.Manifest{
		SchemaVersion: rooms.SchemaVersion,
		Room:          rooms.Room{MaxTurns: 1},
		Participants: []rooms.Participant{
			{
				ID: "customer", SystemPrompt: "customer", Provider: "openai", Model: "gpt-realtime", APIKeyEnv: "CUSTOMER_KEY", Tools: []string{},
				BrowserTools: &rooms.BrowserToolsConfig{Backend: "chrome"},
			},
			{ID: "assistant", SystemPrompt: "assistant", Provider: "openai", Model: "gpt-realtime", APIKeyEnv: "ASSISTANT_KEY", Tools: []string{}},
		},
	}
	runner := New(Dependencies{Live: service, Clock: platformclock.Real{}})
	_, err := runner.Run(context.Background(), nil, rooms.RoomRunOptions{Manifest: manifest})
	if err == nil || !errors.Is(err, rooms.ErrInvalidBrowserTools) {
		t.Fatalf("room admission error = %v, want invalid browser tools", err)
	}
	service.mu.Lock()
	openCount := len(service.requests)
	service.mu.Unlock()
	if openCount != 0 {
		t.Fatalf("live sessions opened before browser validation: %d", openCount)
	}
	var validationErr *rooms.ValidationError
	if !errors.As(err, &validationErr) || validationErr.Field != "participants[0].browserTools.backend" {
		t.Fatalf("validation error = %v, want participant-qualified browser backend field", err)
	}
}
