package live

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live/mediagate"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

type recordingAudioInputSender struct {
	payload []byte
	policy  messages.SessionAudioInputPolicy
}

func (s *recordingAudioInputSender) sendAudioInput(_ context.Context, payload []byte, policy messages.SessionAudioInputPolicy) error {
	s.payload = append([]byte(nil), payload...)
	s.policy = policy
	return nil
}
func TestLoopAudioOutboundUsesOrderedPCMInputPolicy(t *testing.T) {
	sender := &recordingAudioInputSender{}
	var admitted sharedaudio.PCMFrame
	outbound := &loopAudioOutbound{sender: sender, onAdmit: func(frame sharedaudio.PCMFrame) { admitted = frame }}
	want := []int16{1, -2, 32767, -32768}
	frame := sharedaudio.PCMFrame{Samples: want, Format: sharedaudio.PCM16DeviceFormat(16000), StreamID: "capture", Sequence: 7}
	if err := outbound.WriteFrame(context.Background(), frame); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	got, err := codec.DecodePCM16(sender.payload)
	if err != nil {
		t.Fatalf("DecodePCM16: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("encoded samples = %v, want %v", got, want)
	}
	if sender.policy != messages.SessionAudioInputPolicyDefault {
		t.Fatalf("policy = %q, want default interrupting policy", sender.policy)
	}
	if !slices.Equal(admitted.Samples, want) || admitted.StreamID != frame.StreamID || admitted.Sequence != frame.Sequence {
		t.Fatalf("admitted frame = %+v, want metadata-preserving copy of %+v", admitted, frame)
	}
}

type replayReadinessSession struct {
	*testSession
	media sharedaudio.MediaEndpoints
}

func (s *replayReadinessSession) RTCMedia() sharedaudio.MediaEndpoints { return s.media }

type replayReadinessInferencer struct{ session messages.Session }

func (i *replayReadinessInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}
func awaitSessionOpen(t testing.TB, events <-chan session.LiveEvent) []session.LiveEvent {
	t.Helper()
	var got []session.LiveEvent
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("event stream closed before SESSION.OPEN")
			}
			got = append(got, event)
			if event.Kind == string(session.LiveEventStarted) {
				continue
			}
			if event.Kind == string(messages.StreamTypeSessionOpen) {
				return got
			}
		case <-deadline.C:
			t.Fatal("timed out waiting for SESSION.OPEN")
		}
	}
}
func waitForSentText(t testing.TB, provider *testSession, text string) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for !provider.hasText(text) {
		select {
		case <-deadline.C:
			t.Fatalf("text %q was not delivered", text)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}
func collectTestLiveEvents(events <-chan session.LiveEvent) (got []session.LiveEvent) {
	for event := range events {
		got = append(got, event)
	}
	return
}
func queueProviderMessages(t testing.TB, provider *testSession, messagesToQueue []messages.StreamMessage) {
	t.Helper()
	for _, message := range messagesToQueue {
		if !provider.receive.Write(context.Background(), message) {
			t.Fatalf("queue provider message %s", message.Type)
		}
	}
}
func assertEmptyResponseEvents(t testing.TB, events []session.LiveEvent) {
	t.Helper()
	faultIndex, terminalIndex := -1, -1
	for index, event := range events {
		switch event.Kind {
		case string(session.LiveEventLiveness):
			faultIndex = index
			if event.Liveness == nil || event.Liveness.Classification != "silent_provider_empty_response" || event.Liveness.ResponseID != "response-empty" {
				t.Fatalf("liveness event = %+v", event)
			}
		case string(session.LiveEventTerminal):
			terminalIndex = index
			if event.Terminal == nil || event.Terminal.Classification != "silent_provider_empty_response" || event.Terminal.TerminalReason != messages.TerminalReasonTerminalFailure {
				t.Fatalf("terminal event = %+v", event)
			}
		}
	}
	if faultIndex < 0 || terminalIndex < 0 || faultIndex >= terminalIndex {
		t.Fatalf("event order = fault %d, terminal %d, events %+v", faultIndex, terminalIndex, events)
	}
}
func TestReplayWaitsForSessionUpdatedBeforeFirstPCM(t *testing.T) {
	provider := newTestSession()
	frameSeen := make(chan struct{})
	frameOnce := frameSeen
	media := sharedaudio.NewSessionMediaAtRate(func(context.Context, sharedaudio.PCMFrame) error {
		select {
		case <-frameOnce:
		default:
			close(frameOnce)
		}
		return nil
	}, 24000)
	service := New(Dependencies{InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
		return &replayReadinessInferencer{session: &replayReadinessSession{testSession: provider, media: media.Endpoints()}}, nil
	}})
	handle, err := service.OpenLive(context.Background(), session.LiveRequest{
		SessionID: "replay-readiness",
		ReplayPlan: &session.LiveReplayPlan{
			WaitForSessionUpdated: true,
			AudioTurns:            []session.LiveReplayAudioTurn{{Chunks: [][]int16{{1, 2, 3}}}},
		},
		FinishAfterResponse: true,
	})
	if err != nil {
		t.Fatalf("OpenLive: %v", err)
	}
	if err := handle.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-frameSeen:
		t.Fatal("replay admitted PCM before the provider session.updated boundary")
	case <-time.After(20 * time.Millisecond):
	}
	for _, msg := range []messages.StreamMessage{
		{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("provider", "audio")},
		{Type: messages.StreamTypeSessionUpdated, Value: messages.NewSessionUpdatedValue("provider")},
	} {
		if !provider.receive.Write(context.Background(), msg) {
			t.Fatalf("queue provider message %s", msg.Type)
		}
	}
	select {
	case <-frameSeen:
	case <-time.After(time.Second):
		t.Fatal("replay did not admit PCM after session.updated")
	}
	cause := errors.New("stop readiness fixture")
	handle.Cancel(cause)
	if err := handle.Wait(); !errors.Is(err, cause) {
		t.Fatalf("Wait = %v, want cancellation cause", err)
	}
}
func assertFiniteResponseReplacement(t testing.TB, h *handle, interrupted messages.StreamMessage, replacementID string) {
	t.Helper()
	h.observeFiniteResponse(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: interrupted.ResponseID})
	if !h.observeFiniteResponse(interrupted) {
		t.Fatal("interrupted response was not recognized as a terminal boundary")
	}
	if h.gracefulStop || h.replayResponses != 0 {
		t.Fatalf("interrupted response stopped/count = %t/%d, want false/0", h.gracefulStop, h.replayResponses)
	}
	h.observeFiniteResponse(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: replacementID})
	h.observeFiniteResponse(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: replacementID, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	if !h.gracefulStop || h.replayResponses != 1 {
		t.Fatalf("replacement stop/count = %t/%d, want true/1", h.gracefulStop, h.replayResponses)
	}
}
func assertFiniteResponseCompletes(t testing.TB, h *handle, responseID string, value *messages.MessageEndValue) {
	t.Helper()
	h.observeFiniteResponse(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: responseID})
	if !h.observeFiniteResponse(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: responseID, Value: value}) {
		t.Fatal("finite response did not complete at its terminal boundary")
	}
	if !h.gracefulStop || h.replayResponses != 1 {
		t.Fatalf("finite response stop/count = %t/%d, want true/1", h.gracefulStop, h.replayResponses)
	}
}
func TestInterruptedFiniteResponseDoesNotFinishBeforeReplacement(t *testing.T) {
	h := &handle{request: session.LiveRequest{FinishAfterResponse: true}, captureComplete: true, responseStartWake: make(chan struct{})}
	assertFiniteResponseReplacement(t, h, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "response-original", Value: &messages.MessageEndValue{Type: "message_end", TerminalReason: messages.TerminalReasonPartialOutput, OutputState: messages.TerminalOutputPartial}}, "response-replacement")
}
func TestReplayCancelledProviderResponseDoesNotFinishBeforeReplacement(t *testing.T) {
	h := &handle{
		request: session.LiveRequest{
			FinishAfterResponse: true,
			ReplayPlan: &session.LiveReplayPlan{
				InterruptionReplacementExpected: true,
			},
		},
		captureComplete:   true,
		responseStartWake: make(chan struct{}),
	}
	assertFiniteResponseReplacement(t, h, messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		Role:       messages.RoleAssistant,
		ResponseID: "response-interrupted",
		Value: messages.NewMessageEndValueWithTerminal(
			messages.TokenUsage{},
			messages.TerminalReasonCancellation,
			messages.TerminalProvenanceProvider,
			messages.TerminalOutputNone,
		),
	}, "response-healthy")
}
func TestReplayCancelledProviderResponseFinishesWithoutReplacement(t *testing.T) {
	h := &handle{request: session.LiveRequest{FinishAfterResponse: true, ReplayPlan: &session.LiveReplayPlan{}}, captureComplete: true, responseStartWake: make(chan struct{})}
	assertFiniteResponseCompletes(t, h, "response-cancelled-only", messages.NewMessageEndValueWithTerminal(messages.TokenUsage{}, messages.TerminalReasonCancellation, messages.TerminalProvenanceProvider, messages.TerminalOutputNone))
}
func TestInterruptedFiniteResponseDoesNotFinishAfterPriorResponse(t *testing.T) {
	h := &handle{
		request:           session.LiveRequest{FinishAfterResponse: true},
		captureComplete:   true,
		responseStarted:   true,
		replayResponses:   1,
		responseStartWake: make(chan struct{}),
	}
	h.observeFiniteResponse(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "response-current"})
	partial := messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		Role:       messages.RoleAssistant,
		ResponseID: "response-current",
		Value:      &messages.MessageEndValue{Type: "message_end", TerminalReason: messages.TerminalReasonPartialOutput, OutputState: messages.TerminalOutputPartial},
	}
	if !h.observeFiniteResponse(partial) {
		t.Fatal("interrupted response was not recognized as a terminal boundary")
	}
	if h.gracefulStop || h.replayResponses != 1 {
		t.Fatalf("interrupted response after prior completion stopped/count = %t/%d, want false/1", h.gracefulStop, h.replayResponses)
	}
}
func TestBargeCaptureWaitsOnCancelledResponseBoundary(t *testing.T) {
	h := &handle{
		responseTerminalWake: make(chan struct{}),
		scheduledAudioCount:  3,
	}
	h.configureScheduledAudio(3, 0)
	result := make(chan error, 1)
	go func() { result <- h.waitForResponseBoundary(context.Background(), 2) }()
	h.observeResponseTerminal(messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		Role:       messages.RoleAssistant,
		ResponseID: "response-cancelled",
		Value: &messages.MessageEndValue{
			TerminalReason: messages.TerminalReasonPartialOutput,
		},
	})
	select {
	case err := <-result:
		t.Fatalf("boundary wait released after one cancelled response: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	h.observeResponseTerminal(messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		Role:       messages.RoleAssistant,
		ResponseID: "response-completed",
	})
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("boundary wait returned error after second terminal: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("boundary wait did not release after cancelled and completed response terminals")
	}
}
func TestScheduledBargeCompletionCountsCancelledTerminal(t *testing.T) {
	h := &handle{
		request: session.LiveRequest{
			FinishAfterResponse: true,
			ExpectedResponses:   3,
		},
		captureComplete:           true,
		responseStarted:           true,
		scheduledAudioCount:       3,
		observedResponseTerminals: 3,
		replayResponses:           2,
	}
	if !h.canFinishFiniteResponse() {
		t.Fatal("scheduled finite gate ignored the cancelled response terminal")
	}
}
func TestSuccessfulToolContinuationClearsFinitePendingCount(t *testing.T) {
	const callID = "call-finite-continuation"
	h := &handle{
		request:           session.LiveRequest{FinishAfterResponse: true},
		captureComplete:   true,
		responseStarted:   true,
		replayResponses:   0,
		pendingToolCalls:  1,
		toolContinuations: make(map[string]*liveToolContinuation),
	}
	callEnd := messages.StreamMessage{
		Type:       messages.StreamTypeToolCallEnd,
		Role:       messages.RoleAssistant,
		ToolCallId: callID,
		Value:      messages.NewToolCallEndValue(callID, "lookup", `{}`),
	}
	h.observeProviderToolCall(callEnd)
	h.observeFiniteResponse(callEnd)
	h.observeToolResult(callID, "lookup", true)
	h.observeToolResponseOutput(callID)
	toolMessage := messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleTool,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	}
	if continuationErr, complete := h.observeToolLifecycle(toolMessage); continuationErr != nil || complete {
		t.Fatalf("tool result lifecycle = error:%v complete:%t, want pending continuation", continuationErr, complete)
	}
	h.observeFiniteResponse(toolMessage)
	h.markContinuationOutput()
	messageEnd := messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		Role:       messages.RoleAssistant,
		ResponseID: "response-continuation",
		Value:      messages.NewMessageEndValue(messages.TokenUsage{}),
	}
	continuationErr, complete := h.observeToolLifecycle(messageEnd)
	if continuationErr != nil || !complete {
		t.Fatalf("tool continuation lifecycle = error:%v complete:%t, want successful completion", continuationErr, complete)
	}
	if !h.observeFiniteResponse(messageEnd, complete) {
		t.Fatal("successful tool continuation was not observed at the finite response boundary")
	}
	h.mu.Lock()
	pending := h.pendingToolCalls
	graceful := h.gracefulStop
	h.mu.Unlock()
	if pending != 0 || !graceful || h.replayResponses != 1 {
		t.Fatalf("successful continuation finite state = pending:%d graceful:%t responses:%d, want pending:0 graceful:true responses:1", pending, graceful, h.replayResponses)
	}
}
func TestToolContinuationWithNextProviderCallKeepsFinitePendingCount(t *testing.T) {
	const firstCallID = "call-finite-first"
	const nextCallID = "call-finite-next"
	h := &handle{
		request:           session.LiveRequest{FinishAfterResponse: true},
		captureComplete:   true,
		responseStarted:   true,
		toolContinuations: make(map[string]*liveToolContinuation),
		responseStartWake: make(chan struct{}),
	}
	firstCall := messages.StreamMessage{
		Type:       messages.StreamTypeToolCallEnd,
		Role:       messages.RoleAssistant,
		ToolCallId: firstCallID,
		Value:      messages.NewToolCallEndValue(firstCallID, "lookup", `{}`),
	}
	h.observeProviderToolCall(firstCall)
	h.observeFiniteResponse(firstCall)
	h.observeToolResult(firstCallID, "lookup", true)
	h.observeToolResponseOutput(firstCallID)
	toolResultEnd := messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleTool,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	}
	if continuationErr, complete := h.observeToolLifecycle(toolResultEnd); continuationErr != nil || complete {
		t.Fatalf("tool result lifecycle = error:%v complete:%t, want pending continuation", continuationErr, complete)
	}
	h.observeFiniteResponse(toolResultEnd)
	h.markContinuationOutput()
	nextCall := messages.StreamMessage{
		Type:       messages.StreamTypeToolCallEnd,
		Role:       messages.RoleAssistant,
		ToolCallId: nextCallID,
		Value:      messages.NewToolCallEndValue(nextCallID, "list", `{}`),
	}
	if continuationErr, complete := h.observeToolLifecycle(nextCall); continuationErr != nil || complete {
		t.Fatalf("next tool lifecycle = error:%v complete:%t, want pending continuation", continuationErr, complete)
	}
	h.observeFiniteResponse(nextCall)
	continuationEnd := messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		Role:       messages.RoleAssistant,
		ResponseID: "response-with-next-tool",
		Value:      messages.NewMessageEndValue(messages.TokenUsage{}),
	}
	continuationErr, complete := h.observeToolLifecycle(continuationEnd)
	if continuationErr != nil || !complete {
		t.Fatalf("continuation boundary = error:%v complete:%t, want successful prior continuation", continuationErr, complete)
	}
	h.observeFiniteResponse(continuationEnd, complete)
	if h.gracefulStop {
		t.Fatal("finite response marked graceful stop with a pending next provider tool call")
	}
	if h.pendingToolCalls != 1 {
		t.Fatalf("pending tool calls = %d, want 1 for the next provider call", h.pendingToolCalls)
	}
}
func TestOpeningContentWaitsForProviderAdmission(t *testing.T) {
	h := newHandle(session.LiveRequest{OpeningContentParts: []messages.ContentPart{messages.ImagePart{Bytes: []byte{1, 2, 3}}}}, nil, nil, nil, nil, defaultEventCapacity, nil, nil)
	result := make(chan error, 1)
	go func() { result <- h.waitOpeningReady(context.Background()) }()
	select {
	case err := <-result:
		t.Fatalf("opening wait returned before admission: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	h.markOpeningAdmitted(nil)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("opening wait after admission = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("opening wait did not release after provider admission")
	}
}

// An explicit control may register its media barrier before an automatic
// provider send reaches the wrapper. The automatic send must be allowed to
// finish so the model runner can dispatch the control; waiting on the control
// barrier from the runner itself would deadlock both operations.
func TestOrderedSessionAutomaticSendAheadOfPendingControlDoesNotDeadlock(t *testing.T) {
	gate := mediagate.New(nil)
	ackID, _, err := gate.RegisterAck()
	if err != nil {
		t.Fatalf("register control: %v", err)
	}
	automaticStarted := make(chan struct{})
	releaseAutomatic := make(chan struct{})
	controlSent := make(chan struct{})
	provider := &orderingSession{
		automaticStarted: automaticStarted,
		releaseAutomatic: releaseAutomatic,
		controlSent:      controlSent,
	}
	ordered := &orderedSession{inner: provider, media: gate}
	automaticDone := make(chan messages.SessionSendOutcome, 1)
	go func() {
		automaticDone <- ordered.SendWithOutcome(context.Background(), messages.StreamMessage{
			Type:  messages.StreamTypeTextDelta,
			Value: messages.NewTextDeltaValue("automatic"),
		})
	}()
	select {
	case <-automaticStarted:
	case <-time.After(time.Second):
		t.Fatal("automatic provider send did not start")
	}
	controlDone := make(chan messages.SessionSendOutcome, 1)
	go func() {
		controlDone <- ordered.SendWithOutcome(context.Background(), messages.StreamMessage{
			Type:            messages.StreamTypeMessageEnd,
			ActorProvidedID: ackID,
			Value:           messages.NewMessageEndValue(messages.TokenUsage{}),
		})
	}()
	select {
	case <-controlSent:
		t.Fatal("control overtook the automatic send")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseAutomatic)
	select {
	case outcome := <-automaticDone:
		if !outcome.OK() {
			t.Fatalf("automatic send outcome = %+v", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("automatic provider send did not finish")
	}
	select {
	case outcome := <-controlDone:
		if !outcome.OK() {
			t.Fatalf("control send outcome = %+v", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("control provider send deadlocked behind automatic send")
	}
	select {
	case <-controlSent:
	case <-time.After(time.Second):
		t.Fatal("control provider send did not run")
	}
}

type orderingSession struct{ automaticStarted, releaseAutomatic, controlSent chan struct{} }

func (s *orderingSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if msg.Type == messages.StreamTypeMessageEnd {
		close(s.controlSent)
		return true
	}
	select {
	case <-s.automaticStarted:
	default:
		close(s.automaticStarted)
	}
	select {
	case <-s.releaseAutomatic:
		return true
	case <-ctx.Done():
		return false
	}
}
func (s *orderingSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return messages.NewTypedBuffer[messages.StreamMessage](1)
}
func (s *orderingSession) Done() <-chan struct{} { return nil }
func (s *orderingSession) Close() error          { return nil }
func TestToolResponseFailureBeforeAcceptedResultIsNotAContinuation(t *testing.T) {
	const callID = "call-original-response"
	h := &handle{toolContinuations: make(map[string]*liveToolContinuation)}
	h.observeProviderToolCall(messages.StreamMessage{
		Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant,
		ToolCallId: callID, Value: messages.NewToolCallEndValue(callID, "read_image", `{}`),
	})
	failure := messages.StreamMessage{
		Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant,
		Value: &messages.MessageEndValue{Type: "message_end", Status: "failed"},
	}
	if err, complete := h.observeToolLifecycle(failure); err != nil || complete {
		t.Fatalf("original tool response = error:%v complete:%t, want no continuation classification", err, complete)
	}
	h.observeToolResult(callID, "read_image", true)
	toolEnd := messages.StreamMessage{
		Type: messages.StreamTypeMessageEnd, Role: messages.RoleTool,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	}
	if err, complete := h.observeToolLifecycle(toolEnd); err != nil || complete {
		t.Fatalf("accepted result after original failure = error:%v complete:%t, want pending continuation", err, complete)
	}
}
func TestRejectedContinuationAdmissionPreservesEarlierRequest(t *testing.T) {
	h := &handle{toolContinuations: map[string]*liveToolContinuation{
		"accepted": {callID: "accepted", resultAccepted: true, continuationRequested: true},
		"pending":  {callID: "pending", resultAccepted: true},
	}}
	rollback := h.beginContinuationAdmission()
	if !h.toolContinuations["accepted"].continuationRequested || !h.toolContinuations["pending"].continuationRequested {
		t.Fatal("admission did not mark every accepted result")
	}
	rollback()
	if !h.toolContinuations["accepted"].continuationRequested {
		t.Fatal("rollback erased an earlier accepted continuation")
	}
	if h.toolContinuations["pending"].continuationRequested {
		t.Fatal("rollback retained the rejected continuation")
	}
}
func TestRejectedToolResultAdmissionRestoresPriorState(t *testing.T) {
	const callID = "call-existing"
	h := &handle{toolContinuations: map[string]*liveToolContinuation{
		callID: {callID: callID, name: "original", outputObserved: true},
	}}
	rollback := h.beginToolResultAdmission(callID, "read_image", true)
	rollback()
	state := h.toolContinuations[callID]
	if state.resultAccepted || state.continuationRequested || state.name != "original" || !state.outputObserved {
		t.Fatalf("rollback state = %+v, want original provider state", state)
	}
	rollbackNew := h.beginToolResultAdmission("call-rejected", "lookup", false)
	rollbackNew()
	if _, ok := h.toolContinuations["call-rejected"]; ok {
		t.Fatal("rollback retained state created only for a rejected result")
	}
}
func TestMediaRequirementsRespectCapturePlaybackDirections(t *testing.T) {
	media := sharedaudio.NewSessionMediaAtRate(nil, 24000)
	t.Cleanup(func() { _ = media.Close() })
	if !(mediaRequirements{outbound: true}).satisfiedBy(sharedaudio.MediaEndpoints{Outbound: media.Endpoints().Outbound}) || !(mediaRequirements{inbound: true}).satisfiedBy(sharedaudio.MediaEndpoints{Inbound: media.Endpoints().Inbound}) || (mediaRequirements{inbound: true, outbound: true}).satisfiedBy(sharedaudio.MediaEndpoints{Inbound: media.Endpoints().Inbound}) || (mediaRequirements{inbound: true, outbound: true}).satisfiedBy(sharedaudio.MediaEndpoints{Outbound: media.Endpoints().Outbound}) {
		t.Fatal("direction-aware media admission mismatch")
	}
}
