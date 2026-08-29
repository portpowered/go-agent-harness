package services

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestRateLimitRetryDecision(t *testing.T) {
	tests := []struct {
		name           string
		status         string
		code           string
		message        string
		statusDetails  string
		terminalReason messages.TerminalReason
		wantDelay      time.Duration
		wantEligible   bool
	}{
		{
			name:         "positive decimal",
			status:       " FAILED ",
			code:         rateLimitRetryCode,
			message:      "Please try again in 1.668s.",
			wantDelay:    1668 * time.Millisecond,
			wantEligible: true,
		},
		{
			name:         "provider message can contain context",
			status:       "failed",
			code:         rateLimitRetryCode,
			message:      "Request rate limit reached. Please try again in 0.25s.",
			wantDelay:    250 * time.Millisecond,
			wantEligible: true,
		},
		{
			name:         "leading decimal",
			status:       "failed",
			code:         rateLimitRetryCode,
			message:      "Please try again in .5s",
			wantDelay:    500 * time.Millisecond,
			wantEligible: true,
		},
		{
			name:         "cap",
			status:       "failed",
			code:         rateLimitRetryCode,
			message:      "Please try again in 45s",
			wantDelay:    maxRateLimitRetryDelay,
			wantEligible: true,
		},
		{
			name:         "missing message fallback",
			status:       "failed",
			code:         rateLimitRetryCode,
			wantDelay:    defaultRateLimitRetryDelay,
			wantEligible: true,
		},
		{
			name:         "malformed message fallback",
			status:       "failed",
			code:         rateLimitRetryCode,
			message:      "Please retry after a short while",
			wantDelay:    defaultRateLimitRetryDelay,
			wantEligible: true,
		},
		{
			name:         "zero fallback",
			status:       "failed",
			code:         rateLimitRetryCode,
			message:      "Please try again in 0s",
			wantDelay:    defaultRateLimitRetryDelay,
			wantEligible: true,
		},
		{
			name:         "negative fallback",
			status:       "failed",
			code:         rateLimitRetryCode,
			message:      "Please try again in -1s",
			wantDelay:    defaultRateLimitRetryDelay,
			wantEligible: true,
		},
		{
			name:         "non finite fallback",
			status:       "failed",
			code:         rateLimitRetryCode,
			message:      "Please try again in NaNs",
			wantDelay:    defaultRateLimitRetryDelay,
			wantEligible: true,
		},
		{
			name:         "incomplete is not eligible",
			status:       "incomplete",
			code:         rateLimitRetryCode,
			message:      "Please try again in 1s",
			wantEligible: false,
		},
		{
			name:         "cancelled is not eligible",
			status:       "cancelled",
			code:         rateLimitRetryCode,
			message:      "Please try again in 1s",
			wantEligible: false,
		},
		{
			name:           "cancellation reason is not eligible",
			status:         "failed",
			code:           rateLimitRetryCode,
			message:        "Please try again in 1s",
			terminalReason: messages.TerminalReasonCancellation,
			wantEligible:   false,
		},
		{
			name:         "completed is not eligible",
			status:       "completed",
			code:         rateLimitRetryCode,
			message:      "Please try again in 1s",
			wantEligible: false,
		},
		{
			name:         "substring code is not eligible",
			status:       "failed",
			code:         "quota_rate_limit_exceeded",
			message:      "Please try again in 1s",
			wantEligible: false,
		},
		{
			name:         "case changed code is not eligible",
			status:       "failed",
			code:         "RATE_LIMIT_EXCEEDED",
			message:      "Please try again in 1s",
			wantEligible: false,
		},
		{
			name:          "legacy compact details",
			status:        "failed",
			statusDetails: "reason=error, code=rate_limit_exceeded, message=Please try again in 1.5s.",
			wantDelay:     1500 * time.Millisecond,
			wantEligible:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			terminal := &messages.MessageEndValue{
				Type:                 "message_end",
				Status:               test.status,
				StatusDetails:        test.statusDetails,
				ProviderErrorCode:    test.code,
				ProviderErrorMessage: test.message,
				TerminalReason:       test.terminalReason,
			}
			gotDelay, gotEligible := rateLimitRetryDecision(terminal)
			if gotEligible != test.wantEligible {
				t.Fatalf("eligible = %t, want %t", gotEligible, test.wantEligible)
			}
			if gotEligible && gotDelay != test.wantDelay {
				t.Fatalf("delay = %s, want %s", gotDelay, test.wantDelay)
			}
			if !gotEligible && gotDelay != 0 {
				t.Fatalf("ineligible delay = %s, want zero", gotDelay)
			}
		})
	}
}

func TestRateLimitRetryDecisionNilTerminalIsIneligible(t *testing.T) {
	if delay, eligible := rateLimitRetryDecision(nil); eligible || delay != 0 {
		t.Fatalf("nil terminal decision = (%s, %t), want (0, false)", delay, eligible)
	}
}

func TestSessionProgressObserverRateLimitRetryKeepsScheduledLifecycle(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime-2.1-mini")
	probe := &scheduledInputDispatchProbe{}
	observer.scheduleAudioInputs([]ScheduledAudioInput{
		{AfterCompletedTurns: 0, PCM: []byte{1}, EndOfTurn: true},
		{AfterCompletedTurns: 1, PCM: []byte{2}, EndOfTurn: true},
	})
	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch initial scheduled input: %v", err)
	}
	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageStart,
		ResponseID: "response-failed",
		Role:       messages.RoleAssistant,
		Value:      messages.NewMessageStartValue(),
	})
	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeTextDelta,
		ResponseID: "response-failed",
		Role:       messages.RoleAssistant,
		Value:      messages.NewTextDeltaValue("partial provider work"),
	})

	failed := &messages.MessageEndValue{
		Type:                 "message_end",
		Status:               "failed",
		ProviderErrorCode:    rateLimitRetryCode,
		ProviderErrorMessage: "Please try again in 0.01s",
	}
	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		ResponseID: "response-failed",
		Role:       messages.RoleAssistant,
		Value:      failed,
	})
	delay, retry := observer.claimScheduledRateLimitRetry("response-failed", failed)
	if !retry || delay != 10*time.Millisecond {
		t.Fatalf("retry claim = (%s, %t), want (10ms, true)", delay, retry)
	}
	if observer.completedScheduled != 0 || observer.nextScheduledResponse != 1 {
		t.Fatalf("failed response changed schedule counters: completed=%d next=%d", observer.completedScheduled, observer.nextScheduledResponse)
	}
	if index, ok := observer.pendingScheduledRateLimitRetryIndex(); !ok || index != 0 {
		t.Fatalf("pending retry lifecycle = (%d, %t), want (0, true)", index, ok)
	}

	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageStart,
		ResponseID: "response-replacement",
		Role:       messages.RoleAssistant,
		Value:      messages.NewMessageStartValue(),
	})
	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeTextDelta,
		ResponseID: "response-replacement",
		Role:       messages.RoleAssistant,
		Value:      messages.NewTextDeltaValue("replacement completed"),
	})
	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		ResponseID: "response-replacement",
		Role:       messages.RoleAssistant,
		Value:      &messages.MessageEndValue{Type: "message_end", Status: "completed"},
	})
	if observer.completedScheduled != 1 || observer.turnsCompleted != 1 {
		t.Fatalf("replacement completion = scheduled:%d turns:%d, want 1/1", observer.completedScheduled, observer.turnsCompleted)
	}
	if observer.scheduledResponseByID["response-failed"] != 0 || observer.scheduledResponseByID["response-replacement"] != 0 {
		t.Fatalf("response IDs did not remain on lifecycle zero: %#v", observer.scheduledResponseByID)
	}
	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch following scheduled input: %v", err)
	}
	if len(probe.audio) != 2 || string(probe.audio[1]) != string([]byte{2}) {
		t.Fatalf("following scheduled input = %#v, want second input after replacement", probe.audio)
	}
}

func TestSessionProgressObserverRateLimitRetrySupportsUntaggedTerminal(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime-2.1-mini")
	probe := &scheduledInputDispatchProbe{}
	observer.scheduleAudioInputs([]ScheduledAudioInput{{AfterCompletedTurns: 0, PCM: []byte{1}, EndOfTurn: true}})
	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch scheduled input: %v", err)
	}
	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageStart,
		ResponseID: "response-untagged-terminal",
		Value:      messages.NewMessageStartValue(),
	})
	failed := &messages.MessageEndValue{
		Type:                 "message_end",
		Status:               "failed",
		ProviderErrorCode:    rateLimitRetryCode,
		ProviderErrorMessage: "Please try again in 0.01s",
	}
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: failed})
	if delay, retry := observer.claimScheduledRateLimitRetry("", failed); !retry || delay != 10*time.Millisecond {
		t.Fatalf("untagged retry claim = (%s, %t), want (10ms, true)", delay, retry)
	}
}

func TestRateLimitRetryWaitStopsOnContextCancellation(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime-2.1-mini")
	probe := &scheduledInputDispatchProbe{}
	observer.scheduleAudioInputs([]ScheduledAudioInput{{AfterCompletedTurns: 0, PCM: []byte{1}, EndOfTurn: true}})
	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch scheduled input: %v", err)
	}
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, ResponseID: "response-cancelled-wait", Value: messages.NewMessageStartValue()})
	failed := &messages.MessageEndValue{Type: "message_end", Status: "failed", ProviderErrorCode: rateLimitRetryCode, ProviderErrorMessage: "Please try again in 0.03s"}
	terminal := messages.StreamMessage{Type: messages.StreamTypeMessageEnd, ResponseID: "response-cancelled-wait", Value: failed}
	observer.observe(terminal)

	session := newRateLimitRetrySession()
	loop, err := agentloop.New(duplexSessionLoopOptions(&rateLimitRetrySessionInferencer{session: session}, sessionLoopOptions{})...)
	if err != nil {
		t.Fatalf("create agent loop: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := retryScheduledRateLimitedResponse(ctx, nil, loop, observer, terminal); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled retry error = %v, want context cancellation", err)
	}
	if got := session.countSent(messages.StreamTypeResponseCreate); got != 0 {
		t.Fatalf("cancelled retry sent %d response.create messages, want zero", got)
	}
}

func TestSessionProgressObserverRateLimitRetryKeepsToolContinuationOwner(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime-2.1-mini")
	observer.setToolResultsEnabled(true)
	observer.scheduleAudioInputs([]ScheduledAudioInput{{AfterCompletedTurns: 0, PCM: []byte{1}, EndOfTurn: true}})
	probe := &scheduledInputDispatchProbe{}
	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch scheduled input: %v", err)
	}
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, ResponseID: "response-tool", Value: messages.NewMessageStartValue()})
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeToolCallStart, ResponseID: "response-tool", ToolCallId: "call-once", Value: messages.NewToolCallStartValue("call-once", "lookup")})
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, ResponseID: "response-tool", ToolCallId: "call-once", Value: messages.NewToolCallEndValue("call-once", "lookup", "{}")})
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, ResponseID: "response-tool", Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	observer.noteToolResultAccepted("call-once")
	observer.noteToolContinuationRequested()
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleTool, Value: messages.NewMessageStartValue()})
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleTool, ToolCallId: "call-once", Value: messages.NewTextDeltaValue("tool result")})
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleTool, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, ResponseID: "response-continuation-failed", Value: messages.NewMessageStartValue()})
	failed := &messages.MessageEndValue{Type: "message_end", Status: "failed", ProviderErrorCode: rateLimitRetryCode, ProviderErrorMessage: "Please try again in 0.01s"}
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, ResponseID: "response-continuation-failed", Value: failed})
	if delay, retry := observer.claimScheduledRateLimitRetry("response-continuation-failed", failed); !retry || delay != 10*time.Millisecond {
		t.Fatalf("continuation retry claim = (%s, %t), want (10ms, true)", delay, retry)
	}
	if observer.hasTerminalToolContinuationFailure() {
		t.Fatal("first eligible continuation failure became terminal while retry was pending")
	}
	if index, ok := observer.pendingScheduledRateLimitRetryIndex(); !ok || index != 0 {
		t.Fatalf("continuation retry lifecycle = (%d, %t), want (0, true)", index, ok)
	}

	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, ResponseID: "response-continuation-replacement", Value: messages.NewMessageStartValue()})
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: "response-continuation-replacement", Value: messages.NewTextDeltaValue("grounded answer")})
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, ResponseID: "response-continuation-replacement", Value: &messages.MessageEndValue{Type: "message_end", Status: "completed"}})

	observer.toolStateMu.Lock()
	state := observer.toolContinuations["call-once"]
	complete := state != nil && state.continuationComplete
	owner := ""
	if state != nil {
		owner = state.continuationResponseID
	}
	observer.toolStateMu.Unlock()
	if !complete || owner != "response-continuation-replacement" {
		t.Fatalf("replacement continuation state = complete:%t owner:%q, want true/new response", complete, owner)
	}
	if observer.completedScheduled != 1 {
		t.Fatalf("replacement continuation credited %d scheduled turns, want 1", observer.completedScheduled)
	}
}

func TestRunAgentLoopSessionRetriesScheduledToolContinuationOnce(t *testing.T) {
	const retryDelay = 30 * time.Millisecond

	session := newRateLimitRetrySession()
	inferencer := &rateLimitRetrySessionInferencer{session: session}
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime-2.1-mini")
	observer.scheduleAudioInputs([]ScheduledAudioInput{
		{AfterCompletedTurns: 0, PCM: []byte{1, 2}, EndOfTurn: true},
		{AfterCompletedTurns: 1, PCM: []byte{3, 4}, EndOfTurn: true},
	})
	executor := &rateLimitRetryToolExecutor{}
	started := time.Now()
	err := runAgentLoopSession(context.Background(), io.Discard, inferencer, sessionLoopOptions{
		CloseAfterScheduledAudio: true,
		ToolExecutor:             executor,
		ToolDefinitions:          []messages.ToolDefinition{{Name: "lookup", Description: "Look up one value."}},
		observer:                 observer,
	})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("runAgentLoopSession: %v; timeline=%v; observer={scheduled:%#v next:%d active:%q logical:%q completed:%d turns:%d}", err, session.timelineSnapshot(), observer.scheduledResponses, observer.nextScheduledResponse, observer.activeScheduledResponseID, observer.logicalScheduledResponseID, observer.completedScheduled, observer.turnsCompleted)
	}
	if elapsed < retryDelay {
		t.Fatalf("retry elapsed time = %s, want at least %s; timeline=%v", elapsed, retryDelay, session.timelineSnapshot())
	}

	if got := session.countSent(messages.StreamTypeResponseCreate); got != 2 {
		t.Fatalf("response.create count = %d, want initial continuation and exactly one retry", got)
	}
	if got := session.countSent(messages.StreamTypeAudioDelta); got != 2 {
		t.Fatalf("audio input count = %d, want exactly two scheduled inputs", got)
	}
	if got := session.countSent(messages.StreamTypeToolCallEnd); got != 1 {
		t.Fatalf("tool result count = %d, want exactly one result", got)
	}
	if calls := executor.callsSnapshot(); len(calls) != 1 || calls[0].ID != "call-retry-once" {
		t.Fatalf("tool executions = %#v, want exactly one call-retry-once", calls)
	}
	if observer.completedScheduled != 2 || observer.turnsCompleted != 2 {
		t.Fatalf("scheduled credits = scheduled:%d turns:%d, want 2/2", observer.completedScheduled, observer.turnsCompleted)
	}
	timeline := session.timelineSnapshot()
	replacementDone := timelineIndex(timeline, "in:MESSAGE.END:response-replacement")
	secondAudio := nthTimelineIndex(timeline, "out:AUDIO.DELTA", 2)
	if replacementDone < 0 || secondAudio < 0 || secondAudio <= replacementDone {
		t.Fatalf("later scheduled audio crossed replacement boundary: timeline=%v", timeline)
	}
}

func TestRunAgentLoopSessionStopsAfterConsecutiveRateLimitFailure(t *testing.T) {
	const retryDelay = 30 * time.Millisecond

	sink := &diagnosticRecordSink{}
	session := newRateLimitRetrySession()
	session.continuationFailures = []rateLimitRetryTerminal{
		{code: rateLimitRetryCode, message: "Please try again in 0.03s", details: "reason=error, code=rate_limit_exceeded, message=Please try again in 0.03s"},
		{code: rateLimitRetryCode, message: "Please try again in 0.03s", details: "reason=error, code=rate_limit_exceeded, message=Please try again in 0.03s"},
	}
	observer := newSessionProgressObserver(sink, nil, "openai", "gpt-realtime-2.1-mini")
	observer.scheduleAudioInputs([]ScheduledAudioInput{{AfterCompletedTurns: 0, PCM: []byte{1, 2}, EndOfTurn: true}})
	executor := &rateLimitRetryToolExecutor{}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := runAgentLoopSession(ctx, io.Discard, &rateLimitRetrySessionInferencer{session: session}, sessionLoopOptions{
		CloseAfterScheduledAudio: true,
		ToolExecutor:             executor,
		ToolDefinitions:          []messages.ToolDefinition{{Name: "lookup", Description: "Look up one value."}},
		observer:                 observer,
	})
	elapsed := time.Since(started)
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("consecutive failure did not terminate promptly: %v; timeline=%v", err, session.timelineSnapshot())
	}
	if err == nil {
		t.Fatal("consecutive rate-limit failure returned nil")
	}
	if elapsed < retryDelay || elapsed >= 250*time.Millisecond {
		t.Fatalf("consecutive failure elapsed time = %s, want retry delay and bounded completion; timeline=%v", elapsed, session.timelineSnapshot())
	}
	if got := session.countSent(messages.StreamTypeResponseCreate); got != 2 {
		t.Fatalf("response.create count = %d, want initial attempt plus exactly one replacement", got)
	}
	if got := session.countSent(messages.StreamTypeAudioDelta); got != 1 {
		t.Fatalf("audio input count = %d, want one scheduled input", got)
	}
	if got := session.countSent(messages.StreamTypeToolCallEnd); got != 1 {
		t.Fatalf("tool result count = %d, want exactly one result", got)
	}
	if calls := executor.callsSnapshot(); len(calls) != 1 {
		t.Fatalf("tool executions = %#v, want exactly one execution", calls)
	}
	if observer.completedScheduled != 0 || observer.turnsCompleted != 0 {
		t.Fatalf("failed scheduled credits = scheduled:%d turns:%d, want 0/0", observer.completedScheduled, observer.turnsCompleted)
	}
	if !errors.Is(err, ErrSessionToolContinuationIncomplete) || !errors.Is(err, ErrSessionScheduledAudioIncomplete) {
		t.Fatalf("consecutive failure error = %v, want tool and scheduled lifecycle sentinels", err)
	}
	var continuationErr *SessionToolContinuationError
	if !errors.As(err, &continuationErr) {
		t.Fatalf("consecutive failure error = %v, want SessionToolContinuationError", err)
	}
	if continuationErr.ProviderStatuses["call-retry-once"] != "failed" || continuationErr.ProviderCodes["call-retry-once"] != rateLimitRetryCode {
		t.Fatalf("consecutive failure provider metadata = statuses:%v codes:%v, want failed/%s", continuationErr.ProviderStatuses, continuationErr.ProviderCodes, rateLimitRetryCode)
	}
	if continuationErr.ProviderDetails["call-retry-once"] != "reason=error, code=rate_limit_exceeded, message=Please try again in 0.03s" {
		t.Fatalf("consecutive failure provider details = %q", continuationErr.ProviderDetails["call-retry-once"])
	}
	if !strings.Contains(err.Error(), rateLimitRetryCode) {
		t.Fatalf("consecutive failure error %q does not retain provider code", err)
	}
	failures := sink.events(SessionDiagnosticEventFailure)
	if len(failures) != 1 || failures[0].Fields[SessionDiagnosticFieldPendingContinuationCodes] != "call-retry-once="+rateLimitRetryCode {
		t.Fatalf("consecutive failure diagnostics = %#v, want one code-bearing record", failures)
	}
}

func TestRunAgentLoopSessionDoesNotRetryNonRateLimitFailure(t *testing.T) {
	const providerCode = "server_error"

	session := newRateLimitRetrySession()
	session.continuationFailures = []rateLimitRetryTerminal{{
		code:    providerCode,
		message: "provider failed without retry",
		details: "reason=error, code=server_error, message=provider failed without retry",
	}}
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime-2.1-mini")
	observer.scheduleAudioInputs([]ScheduledAudioInput{{AfterCompletedTurns: 0, PCM: []byte{1}, EndOfTurn: true}})
	executor := &rateLimitRetryToolExecutor{}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := runAgentLoopSession(ctx, io.Discard, &rateLimitRetrySessionInferencer{session: session}, sessionLoopOptions{
		CloseAfterScheduledAudio: true,
		ToolExecutor:             executor,
		ToolDefinitions:          []messages.ToolDefinition{{Name: "lookup", Description: "Look up one value."}},
		observer:                 observer,
	})
	elapsed := time.Since(started)
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("non-rate-limit failure did not terminate promptly: %v; timeline=%v", err, session.timelineSnapshot())
	}
	if err == nil {
		t.Fatal("non-rate-limit failure returned nil")
	}
	if elapsed >= 250*time.Millisecond {
		t.Fatalf("non-rate-limit failure waited for retry: elapsed=%s timeline=%v", elapsed, session.timelineSnapshot())
	}
	if got := session.countSent(messages.StreamTypeResponseCreate); got != 1 {
		t.Fatalf("response.create count = %d, want no replacement", got)
	}
	if got := session.countSent(messages.StreamTypeAudioDelta); got != 1 || session.countSent(messages.StreamTypeToolCallEnd) != 1 {
		t.Fatalf("non-rate-limit input/tool counts = audio:%d tool:%d, want 1/1", got, session.countSent(messages.StreamTypeToolCallEnd))
	}
	if observer.completedScheduled != 0 || observer.turnsCompleted != 0 {
		t.Fatalf("non-rate-limit credits = scheduled:%d turns:%d, want 0/0", observer.completedScheduled, observer.turnsCompleted)
	}
	if !errors.Is(err, ErrSessionToolContinuationIncomplete) || !errors.Is(err, ErrSessionScheduledAudioIncomplete) {
		t.Fatalf("non-rate-limit error = %v, want tool and scheduled lifecycle sentinels", err)
	}
	var continuationErr *SessionToolContinuationError
	if !errors.As(err, &continuationErr) || continuationErr.ProviderCodes["call-retry-once"] != providerCode {
		t.Fatalf("non-rate-limit provider metadata = %#v, want code %s", continuationErr, providerCode)
	}
}

func TestScheduledFailureRetainsProviderMetadataWithoutRetry(t *testing.T) {
	const providerCode = "server_error"

	sink := &diagnosticRecordSink{}
	observer := newSessionProgressObserver(sink, nil, "openai", "gpt-realtime-2.1-mini")
	probe := &scheduledInputDispatchProbe{}
	observer.scheduleAudioInputs([]ScheduledAudioInput{{AfterCompletedTurns: 0, PCM: []byte{1}, EndOfTurn: true}})
	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch scheduled input: %v", err)
	}
	const responseID = "scheduled-failure"
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, ResponseID: responseID, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()})
	terminal := &messages.MessageEndValue{
		Type:                 "message_end",
		Status:               "failed",
		StatusDetails:        "reason=error, code=server_error, message=provider failed without retry",
		ProviderErrorCode:    providerCode,
		ProviderErrorMessage: "provider failed without retry",
	}
	terminalMessage := messages.StreamMessage{Type: messages.StreamTypeMessageEnd, ResponseID: responseID, Role: messages.RoleAssistant, Value: terminal}
	observer.observe(terminalMessage)
	if !observer.hasTerminalScheduledResponseFailure() || !shouldStopSessionLoop(terminalMessage, sessionLoopOptions{observer: observer, CloseAfterScheduledAudio: true}, false) {
		t.Fatal("scheduled provider failure was not treated as terminal")
	}
	if delay, retry := observer.claimScheduledRateLimitRetry(responseID, terminal); retry || delay != 0 {
		t.Fatalf("direct scheduled failure unexpectedly claimed retry = (%s, %t)", delay, retry)
	}
	err := scheduledAudioCompletionError(nil, sessionLoopOptions{CloseAfterScheduledAudio: true, observer: observer})
	var incomplete *SessionScheduledAudioIncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("scheduled failure error = %v, want typed incomplete error", err)
	}
	if incomplete.ProviderStatus != "failed" || incomplete.ProviderErrorCode != providerCode || incomplete.ProviderDetails != terminal.StatusDetails {
		t.Fatalf("scheduled provider metadata = %+v, want failed/%s/%q", incomplete, providerCode, terminal.StatusDetails)
	}
	if !strings.Contains(err.Error(), providerCode) || !strings.Contains(err.Error(), terminal.StatusDetails) {
		t.Fatalf("scheduled failure error %q lost provider metadata", err)
	}
	if err = observer.finish(err); err == nil {
		t.Fatal("observer finish erased scheduled failure")
	}
	failures := sink.events(SessionDiagnosticEventFailure)
	if len(failures) != 1 || failures[0].Fields[fieldProviderErrorCode] != providerCode {
		t.Fatalf("scheduled failure diagnostics = %#v, want provider code %s", failures, providerCode)
	}
}

type rateLimitRetrySessionInferencer struct {
	session *rateLimitRetrySession
}

func (i *rateLimitRetrySessionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	i.session.emit(messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("rate-limit-retry", "audio_inference"),
	})
	return i.session, nil
}

type rateLimitRetrySession struct {
	recv *messages.TypedBuffer[messages.StreamMessage]
	done chan struct{}
	once sync.Once

	mu                   sync.Mutex
	sent                 []messages.StreamMessage
	timeline             []string
	responseCreates      int
	initialSent          bool
	continuationFailures []rateLimitRetryTerminal
}

type rateLimitRetryTerminal struct {
	status  string
	code    string
	message string
	details string
}

func newRateLimitRetrySession() *rateLimitRetrySession {
	return &rateLimitRetrySession{
		recv: messages.NewTypedBuffer[messages.StreamMessage](64),
		done: make(chan struct{}),
	}
}

func (s *rateLimitRetrySession) Send(_ context.Context, msg messages.StreamMessage) bool {
	s.mu.Lock()
	s.sent = append(s.sent, msg)
	label := "out:" + string(msg.Type)
	if msg.Type == messages.StreamTypeResponseCreate {
		s.responseCreates++
		label += ":" + string(rune('0'+s.responseCreates))
	}
	s.timeline = append(s.timeline, label)
	responseCreate := s.responseCreates
	inputTurn := msg.Type == messages.StreamTypeMessageEnd
	initialResponse := inputTurn && !s.initialSent
	secondScheduledResponse := inputTurn && s.initialSent
	if initialResponse {
		s.initialSent = true
	}
	s.mu.Unlock()

	if initialResponse {
		s.emitInitialToolResponse()
	} else if secondScheduledResponse {
		s.emitSecondScheduledResponse()
	}
	if msg.Type == messages.StreamTypeResponseCreate {
		s.emitResponseCreateResult(responseCreate)
	}
	return true
}

func (s *rateLimitRetrySession) emitInitialToolResponse() {
	const responseID = "response-tool"
	s.emit(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageStartValue()})
	s.emit(messages.StreamMessage{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, ResponseID: responseID, ToolCallId: "call-retry-once", Value: messages.NewToolCallStartValue("call-retry-once", "lookup")})
	s.emit(messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ResponseID: responseID, ToolCallId: "call-retry-once", Value: messages.NewToolCallEndValue("call-retry-once", "lookup", "{}")})
	s.emit(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
}

func (s *rateLimitRetrySession) emitSecondScheduledResponse() {
	const responseID = "response-second"
	s.emit(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageStartValue()})
	s.emit(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewTextDeltaValue("second scheduled answer")})
	s.emit(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: responseID, Value: &messages.MessageEndValue{Type: "message_end", Status: "completed"}})
}

func (s *rateLimitRetrySession) emitResponseCreateResult(attempt int) {
	if attempt <= len(s.continuationFailures) {
		failure := s.continuationFailures[attempt-1]
		if failure.status == "" {
			failure.status = "failed"
		}
		responseID := "response-continuation-failed"
		if attempt > 1 {
			responseID += "-second"
		}
		s.emit(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageStartValue()})
		s.emit(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: responseID, Value: &messages.MessageEndValue{
			Type:                 "message_end",
			Status:               failure.status,
			StatusDetails:        failure.details,
			ProviderErrorCode:    failure.code,
			ProviderErrorMessage: failure.message,
		}})
		return
	}
	switch attempt {
	case 1:
		const responseID = "response-continuation-failed"
		s.emit(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageStartValue()})
		s.emit(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: responseID, Value: &messages.MessageEndValue{
			Type:                 "message_end",
			Status:               "failed",
			ProviderErrorCode:    rateLimitRetryCode,
			ProviderErrorMessage: "Please try again in 0.03s",
		}})
	case 2:
		const responseID = "response-replacement"
		s.emit(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageStartValue()})
		s.emit(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewTextDeltaValue("replacement answer")})
		s.emit(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: responseID, Value: &messages.MessageEndValue{Type: "message_end", Status: "completed"}})
	}
}

func (s *rateLimitRetrySession) emit(msg messages.StreamMessage) {
	label := "in:" + string(msg.Type)
	if msg.Type == messages.StreamTypeMessageEnd {
		if value, ok := msg.Value.(*messages.MessageEndValue); ok && value != nil {
			label += ":" + msg.ResponseID
		}
	}
	s.mu.Lock()
	s.timeline = append(s.timeline, label)
	s.mu.Unlock()
	s.recv.Write(context.Background(), msg)
}

func (s *rateLimitRetrySession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recv
}

func (s *rateLimitRetrySession) Done() <-chan struct{} { return s.done }

func (s *rateLimitRetrySession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

func (s *rateLimitRetrySession) countSent(want messages.StreamMessageType) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, msg := range s.sent {
		if msg.Type == want {
			count++
		}
	}
	return count
}

func (s *rateLimitRetrySession) timelineSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.timeline...)
}

type rateLimitRetryToolExecutor struct {
	mu    sync.Mutex
	calls []messages.ToolCall
}

func (e *rateLimitRetryToolExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.mu.Lock()
	e.calls = append(e.calls, call)
	e.mu.Unlock()
	return messages.ToolCallResponse{Content: "tool result"}, nil
}

func (e *rateLimitRetryToolExecutor) callsSnapshot() []messages.ToolCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]messages.ToolCall(nil), e.calls...)
}

func timelineIndex(timeline []string, want string) int {
	for index, value := range timeline {
		if value == want {
			return index
		}
	}
	return -1
}

func nthTimelineIndex(timeline []string, want string, occurrence int) int {
	seen := 0
	for index, value := range timeline {
		if value != want {
			continue
		}
		seen++
		if seen == occurrence {
			return index
		}
	}
	return -1
}
