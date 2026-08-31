package participants

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// These tests pin the fix for the barge-in / RESPONSE.CANCEL defect family:
//
//  1. forwardQueuedSessionEvent used to send a control-plane event that opens
//     a new response (MESSAGE.END's commit-then-create end-of-turn boundary,
//     or an ordinary continuation) without checking whether the current
//     response was still tracked as in flight. A barge-in cancel does not
//     retire the response locally until its own MESSAGE.END is observed, so
//     an end-of-turn boundary queued immediately after the cancel (exactly
//     what --audio-interrupt and --audio-in-turn do) could race ahead of
//     that boundary -- reproducing the real wire ordering captured live in
//     scheduled-three-turn.session.json (response.cancel@t=2195ms,
//     response.create@t=2221ms, response.done@t=2273ms: the create beat the
//     done by 52ms). That is the state/timing mismatch behind the
//     provider's response_cancel_not_active rejection.
//  2. beginSessionResponse's retirement branch dropped the acknowledgement
//     bookkeeping (acknowledgementOutstanding/acknowledgementCancelled) for
//     a response it retired. That response's own MESSAGE.END, whenever it
//     eventually arrived, was never "owned" again (ownsSessionResponseEnd
//     rejects retired ids) so the reset that normally lives in the owned
//     MESSAGE.END branch never ran. acknowledgementOutstanding was left
//     stuck true, so the barge-in guard treated every later non-silent
//     audio frame as a live cancel target even once nothing was active,
//     producing a RESPONSE.CANCEL with no active response.

// helper: builds a fresh state with a real response already in flight.
func newInFlightRunState(t *testing.T, session messages.Session, runner *ModelRunner, responseID string) *sessionRunState {
	t.Helper()
	state := &sessionRunState{}
	state.ensureMaps()
	runner.forwardSessionMessageState(context.Background(), session, state, messages.StreamMessage{
		Type:       messages.StreamTypeMessageStart,
		ResponseID: responseID,
		Value:      messages.NewMessageStartValue(),
	})
	if !state.responseInFlight || state.currentResponseID != responseID {
		t.Fatalf("setup: response %q not tracked in flight: %+v", responseID, state)
	}
	return state
}

// Requirement 1: a cancel issued while a response is active stops it, and
// the customer's own end-of-turn boundary queued immediately after the
// cancel must not race ahead of that response's terminal MESSAGE.END.
func TestSessionModelRunner_EndOfTurnDeferredUntilCancelledResponseEnds(t *testing.T) {
	ctx := context.Background()
	session := newRecordingSession()
	runner := NewSessionModelRunner(nil, 8, nil)
	state := newInFlightRunState(t, session, runner, "resp-1")

	// Barge-in: contentful audio while resp-1 is active sends exactly one
	// cancel, then forwards the audio -- mirroring what the session runner
	// does before ever reaching UserEventInbox.
	runner.UserAudioInbox <- []byte{1, 2, 3}
	if err := runner.drainSessionAudioWithState(ctx, session, state); err != nil {
		t.Fatalf("drainSessionAudioWithState: %v", err)
	}
	if !state.responseCancelSent || !state.responseInFlight {
		t.Fatalf("state after barge-in audio = %+v, want cancelled and still in flight (no MESSAGE.END yet)", state)
	}

	// The finite audio source's end-of-turn boundary (--audio-interrupt /
	// --audio-in-turn) is queued next, exactly as session_live.go does:
	// SendAudioInput then, on EndOfTurn, SendSessionEvent(MESSAGE.END).
	runner.forwardQueuedSessionEvent(ctx, session, state, messages.StreamMessage{Type: messages.StreamTypeMessageEnd})

	sentBeforeBoundary := session.sentMessages()
	for _, msg := range sentBeforeBoundary {
		if msg.Type == messages.StreamTypeMessageEnd {
			t.Fatalf("end-of-turn boundary reached the provider before resp-1's own MESSAGE.END was observed: %#v", sentBeforeBoundary)
		}
	}
	if len(state.deferredSessionEvents) != 1 {
		t.Fatalf("deferredSessionEvents = %d, want the end-of-turn boundary held back", len(state.deferredSessionEvents))
	}

	// resp-1's own terminal boundary now arrives (the provider's ack of the
	// cancel). Only now may the deferred end-of-turn boundary reach the wire.
	runner.forwardSessionMessageState(ctx, session, state, messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		ResponseID: "resp-1",
		Value:      messages.NewMessageEndValue(messages.TokenUsage{}),
	})

	sent := session.sentMessages()
	if len(sent) != 3 {
		t.Fatalf("sent messages = %#v, want [RESPONSE.CANCEL, AUDIO.DELTA, MESSAGE.END]", sent)
	}
	if sent[0].Type != messages.StreamTypeResponseCancel {
		t.Fatalf("sent[0] = %s, want RESPONSE.CANCEL", sent[0].Type)
	}
	if sent[1].Type != messages.StreamTypeAudioDelta {
		t.Fatalf("sent[1] = %s, want AUDIO.DELTA", sent[1].Type)
	}
	if sent[2].Type != messages.StreamTypeMessageEnd {
		t.Fatalf("sent[2] = %s, want the deferred end-of-turn boundary, released only after resp-1 ended", sent[2].Type)
	}
	if state.responseInFlight || len(state.deferredSessionEvents) != 0 {
		t.Fatalf("state after boundary = %+v, want idle with deferred queue drained", state)
	}
}

// Requirement 2: no cancel is ever emitted when no response is active. This
// pins the retirement fix in beginSessionResponse: an acknowledgement whose
// id gets retired before its own MESSAGE.END arrives must not leave
// acknowledgementOutstanding stuck true, because that would make ordinary,
// unrelated audio look like a live barge-in target forever after.
func TestSessionModelRunner_RetiredAcknowledgementDoesNotStrandCancelGuard(t *testing.T) {
	ctx := context.Background()
	session := newRecordingSession()
	runner := NewSessionModelRunner(nil, 8, nil)
	state := &sessionRunState{}
	state.ensureMaps()

	// The acknowledgement was requested (its response.create was accepted)
	// before its response.created came back -- the documented
	// "response-created-before-first-audio" window.
	state.acknowledgementOutstanding = true
	runner.forwardSessionMessageState(ctx, session, state, messages.StreamMessage{
		Type:       messages.StreamTypeMessageStart,
		ResponseID: "resp-ack",
		Value:      messages.NewMessageStartValue(),
	})
	if state.currentResponseID != "resp-ack" || !state.acknowledgementOutstanding {
		t.Fatalf("setup: acknowledgement not tracked: %+v", state)
	}

	// A different response starts before resp-ack's own MESSAGE.END is
	// observed -- resp-ack is retired mid-flight.
	runner.forwardSessionMessageState(ctx, session, state, messages.StreamMessage{
		Type:       messages.StreamTypeMessageStart,
		ResponseID: "resp-next",
		Value:      messages.NewMessageStartValue(),
	})
	if state.acknowledgementOutstanding {
		t.Fatalf("acknowledgementOutstanding stuck true after its response was retired: %+v", state)
	}
	if _, retired := state.retiredResponseIDs["resp-ack"]; !retired {
		t.Fatalf("resp-ack was not retired: %+v", state)
	}

	// resp-ack's own MESSAGE.END finally arrives, late. It must be silently
	// dropped (its id is retired) rather than disturbing resp-next.
	runner.forwardSessionMessageState(ctx, session, state, messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		ResponseID: "resp-ack",
		Value:      messages.NewMessageEndValue(messages.TokenUsage{}),
	})
	if state.currentResponseID != "resp-next" || !state.responseInFlight {
		t.Fatalf("stale resp-ack MESSAGE.END disturbed resp-next tracking: %+v", state)
	}

	// resp-next completes normally.
	runner.forwardSessionMessageState(ctx, session, state, messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		ResponseID: "resp-next",
		Value:      messages.NewMessageEndValue(messages.TokenUsage{}),
	})
	if state.responseInFlight || state.acknowledgementOutstanding {
		t.Fatalf("state after resp-next completed = %+v, want fully idle", state)
	}

	// Nothing is active now. Ordinary, unrelated non-silent audio must be
	// forwarded directly -- with the bug, the stranded acknowledgementOutstanding
	// flag would make this send a RESPONSE.CANCEL with nothing to cancel,
	// which is exactly what the provider rejects with response_cancel_not_active.
	runner.UserAudioInbox <- []byte{9, 9, 9}
	if err := runner.drainSessionAudioWithState(ctx, session, state); err != nil {
		t.Fatalf("drainSessionAudioWithState: %v", err)
	}
	sent := session.sentMessages()
	if len(sent) != 1 || sent[0].Type != messages.StreamTypeAudioDelta {
		t.Fatalf("sent = %#v, want exactly one AUDIO.DELTA and no RESPONSE.CANCEL", sent)
	}
}

// Requirement 3: response lifecycle state is tracked correctly across
// back-to-back responses, so a cancel issued after one response retires
// another targets the response that is actually current -- never the stale,
// already-retired id.
func TestSessionModelRunner_BargeInAfterRetirementTargetsCurrentResponseID(t *testing.T) {
	ctx := context.Background()
	session := newRecordingSession()
	runner := NewSessionModelRunner(nil, 8, nil)
	state := newInFlightRunState(t, session, runner, "resp-a")

	// resp-b supersedes resp-a before resp-a's own MESSAGE.END arrives.
	runner.forwardSessionMessageState(ctx, session, state, messages.StreamMessage{
		Type:       messages.StreamTypeMessageStart,
		ResponseID: "resp-b",
		Value:      messages.NewMessageStartValue(),
	})
	if state.currentResponseID != "resp-b" {
		t.Fatalf("current response after retirement = %q, want resp-b", state.currentResponseID)
	}

	// A genuine barge-in now targets whichever response is actually live.
	runner.UserAudioInbox <- []byte{4, 5, 6}
	if err := runner.drainSessionAudioWithState(ctx, session, state); err != nil {
		t.Fatalf("drainSessionAudioWithState: %v", err)
	}
	if _, cancelled := state.cancelledResponseIDs["resp-b"]; !cancelled {
		t.Fatalf("cancelledResponseIDs = %v, want resp-b (the live response)", state.cancelledResponseIDs)
	}
	if _, cancelled := state.cancelledResponseIDs["resp-a"]; cancelled {
		t.Fatalf("cancelledResponseIDs = %v, want resp-a (already retired) untouched", state.cancelledResponseIDs)
	}
	sent := session.sentMessages()
	if len(sent) != 2 || sent[0].Type != messages.StreamTypeResponseCancel || sent[1].Type != messages.StreamTypeAudioDelta {
		t.Fatalf("sent = %#v, want exactly one RESPONSE.CANCEL then AUDIO.DELTA", sent)
	}

	// resp-a's stale MESSAGE.END, arriving late, must not be owned or
	// mistaken for resp-b's boundary.
	runner.forwardSessionMessageState(ctx, session, state, messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		ResponseID: "resp-a",
		Value:      messages.NewMessageEndValue(messages.TokenUsage{}),
	})
	if state.currentResponseID != "resp-b" || !state.responseInFlight {
		t.Fatalf("stale resp-a MESSAGE.END disturbed resp-b tracking: %+v", state)
	}

	// resp-b's own MESSAGE.END correctly ends the cancelled response.
	runner.forwardSessionMessageState(ctx, session, state, messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		ResponseID: "resp-b",
		Value:      messages.NewMessageEndValue(messages.TokenUsage{}),
	})
	if state.currentResponseID != "" || state.responseInFlight {
		t.Fatalf("state after resp-b ended = %+v, want idle", state)
	}
	if _, terminal := state.terminalResponseIDs["resp-b"]; !terminal {
		t.Fatalf("resp-b was not recorded terminal: %+v", state)
	}
}

// Requirement 4: the non-interrupt happy path is unaffected. An ordinary
// end-of-turn boundary sent while no response is active must reach the
// provider immediately, not be deferred.
func TestSessionModelRunner_EndOfTurnNotDeferredWhenIdle(t *testing.T) {
	ctx := context.Background()
	session := newRecordingSession()
	runner := NewSessionModelRunner(nil, 8, nil)
	state := &sessionRunState{}
	state.ensureMaps()

	runner.forwardQueuedSessionEvent(ctx, session, state, messages.StreamMessage{Type: messages.StreamTypeMessageEnd})

	sent := session.sentMessages()
	if len(sent) != 1 || sent[0].Type != messages.StreamTypeMessageEnd {
		t.Fatalf("sent = %#v, want the end-of-turn boundary forwarded immediately", sent)
	}
	if len(state.deferredSessionEvents) != 0 {
		t.Fatalf("deferredSessionEvents = %d, want none -- nothing was active to wait on", len(state.deferredSessionEvents))
	}
}

// Requirement 4 (over-triggering direction, coordinator follow-up): a
// response can be genuinely in flight with no cancel ever sent -- e.g. the
// provider auto-started a response for the very audio the customer is still
// finishing, matching how test fixtures and server-side VAD auto-response
// both behave. The customer's own end-of-turn boundary for that same
// response must still reach the wire immediately; deferring it here would
// wait on a RESPONSE.CANCEL acknowledgement that was never sent and will
// never arrive, hanging the session until its deadline. This reproduces, at
// the model_runner unit level, the exact shape of
// TestFamilyDTerminationShapesThroughShippedProcess/natural in
// agent-cli/test/integration: the fixture starts a response as soon as it
// sees non-silent audio and only finalizes it once it observes the client's
// own response.create -- withholding that response.create hangs forever.
func TestSessionModelRunner_EndOfTurnNotDeferredWhenResponseActiveWithNoCancel(t *testing.T) {
	ctx := context.Background()
	session := newRecordingSession()
	runner := NewSessionModelRunner(nil, 8, nil)
	state := newInFlightRunState(t, session, runner, "resp-natural")

	if state.responseCancelSent {
		t.Fatalf("setup: no cancel should have been sent yet: %+v", state)
	}

	// The customer's own end-of-turn boundary for the response that is
	// already streaming -- no barge-in, no cancel, just the ordinary close
	// of the customer's turn.
	runner.forwardQueuedSessionEvent(ctx, session, state, messages.StreamMessage{Type: messages.StreamTypeMessageEnd})

	sent := session.sentMessages()
	if len(sent) != 1 || sent[0].Type != messages.StreamTypeMessageEnd {
		t.Fatalf("sent = %#v, want the end-of-turn boundary forwarded immediately (no cancel is in flight to wait on)", sent)
	}
	if len(state.deferredSessionEvents) != 0 {
		t.Fatalf("deferredSessionEvents = %d, want none -- a response with no outstanding cancel must not block the customer's own end-of-turn boundary", len(state.deferredSessionEvents))
	}
}
