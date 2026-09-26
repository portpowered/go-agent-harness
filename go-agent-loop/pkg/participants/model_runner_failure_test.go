package participants

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestModelRunnerPublishSessionAudioFailurePreservesCauseAndMetadata(t *testing.T) {
	rootErr := errors.New("provider audio queue closed")
	runner := NewSessionModelRunner(nil, 1, nil)
	runner.currentPassID = 17

	runner.publishSessionAudioFailure(rootErr, false)

	delta, ok := runner.DeltaOutbox.Read()
	if !ok {
		t.Fatal("terminal audio failure was not published")
	}
	if delta.Type != messages.StreamTypeError {
		t.Fatalf("delta type = %q, want %q", delta.Type, messages.StreamTypeError)
	}
	if delta.ActorID != messages.Model {
		t.Fatalf("actor = %q, want %q", delta.ActorID, messages.Model)
	}
	if delta.LoopPassID != 17 {
		t.Fatalf("loop pass = %d, want 17", delta.LoopPassID)
	}
	value, ok := delta.Value.(*messages.ErrorValue)
	if !ok {
		t.Fatalf("delta value = %T, want *messages.ErrorValue", delta.Value)
	}
	if value.Message != rootErr.Error() {
		t.Fatalf("message = %q, want %q", value.Message, rootErr)
	}
	if !errors.Is(value.Err, rootErr) {
		t.Fatalf("error cause = %v, want %v", value.Err, rootErr)
	}
	if value.Classification != sessionAudioSendFailureClassification {
		t.Fatalf("classification = %q, want %q", value.Classification, sessionAudioSendFailureClassification)
	}
	if value.TerminalReason != messages.TerminalReasonTerminalFailure {
		t.Fatalf("terminal reason = %q, want %q", value.TerminalReason, messages.TerminalReasonTerminalFailure)
	}
	if value.TerminalProvenance != messages.TerminalProvenanceLoop {
		t.Fatalf("terminal provenance = %q, want %q", value.TerminalProvenance, messages.TerminalProvenanceLoop)
	}
	if value.OutputState != messages.TerminalOutputNone {
		t.Fatalf("output state = %q, want %q", value.OutputState, messages.TerminalOutputNone)
	}
}

func TestModelRunnerPublishSessionAudioFailureEvictsOrdinaryDelta(t *testing.T) {
	runner := NewSessionModelRunner(nil, 1, nil)
	if !runner.DeltaOutbox.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("stale"),
	}) {
		t.Fatal("ordinary delta was not queued")
	}

	runner.publishSessionAudioFailure(errors.New("audio write failed"), true)
	got, ok := runner.DeltaOutbox.Read()
	if !ok || got.Type != messages.StreamTypeError {
		t.Fatalf("queued delta = %#v, ok=%t, want terminal ERROR", got, ok)
	}
	value, ok := got.Value.(*messages.ErrorValue)
	if !ok || value.OutputState != messages.TerminalOutputPartial {
		t.Fatalf("terminal value = %#v, want partial output state", got.Value)
	}
}

type failingConnectInferencer struct{ err error }

func (f *failingConnectInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return nil, f.err
}

func TestSessionModelRunner_ConnectErrorWrapsFailure(t *testing.T) {
	si := &failingConnectInferencer{err: errors.New("handshake refused")}
	runner := NewSessionModelRunner(si, 8, nil)

	err := runner.Run(context.Background())
	if err == nil {
		t.Fatal("expected connect error, got nil")
	}
	if !errors.Is(err, si.err) {
		t.Fatalf("error should wrap underlying failure, got %v", err)
	}
	if got := err.Error(); !contains(got, "session connect") {
		t.Fatalf("error = %q, want prefix context %q", got, "session connect")
	}
}

func TestSessionModelRunner_SessionCreatedUpdateFailureIsObservable(t *testing.T) {
	session := &outcomeRecordingSession{
		recordingSession: newRecordingSession(),
		outcomes: map[messages.StreamMessageType]messages.SessionSendOutcome{
			messages.StreamTypeSessionUpdate: {Status: messages.SessionSendTerminalFailure},
		},
	}
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, &messages.SessionUpdateConfig{
		Tools: []messages.ToolDefinition{{Name: "current_page_tool"}},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- runner.Run(ctx) }()

	if !session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionCreated,
		Value: messages.NewSessionCreatedValue("failed-config-session", "test"),
	}) {
		t.Fatal("failed to enqueue SESSION.CREATED")
	}
	failure := waitForDelta(t, ctx, runner, messages.StreamTypeError)
	value, ok := failure.Value.(*messages.ErrorValue)
	if !ok || value.Classification != unresolvedSessionUpdateClassification || !contains(value.Message, "tool definition update") {
		t.Fatalf("session-created update failure = %#v, want unresolved session update", failure.Value)
	}
	if sent := session.sentMessages(); len(sent) != 0 {
		t.Fatalf("rejected initial update sent %d provider messages, want zero", len(sent))
	}

	if err := session.Close(); err != nil {
		t.Fatalf("close session: %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
	case <-ctx.Done():
		t.Fatal("Run did not return after session close")
	}
}

func TestSessionModelRunner_BargeInSendFailurePropagatesFromRun(t *testing.T) {
	tests := []struct {
		name     string
		outcomes map[messages.StreamMessageType]messages.SessionSendOutcome
		want     []string
	}{
		{
			name: "cancel buffer full",
			outcomes: map[messages.StreamMessageType]messages.SessionSendOutcome{
				messages.StreamTypeResponseCancel: {Status: messages.SessionSendBufferFull},
			},
			want: []string{"response cancel", "buffer_full"},
		},
		{
			name: "audio closed after accepted cancel",
			outcomes: map[messages.StreamMessageType]messages.SessionSendOutcome{
				messages.StreamTypeResponseCancel: {Status: messages.SessionSendSucceeded},
				messages.StreamTypeAudioDelta:     {Status: messages.SessionSendClosed},
			},
			want: []string{"audio", "closed"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := &outcomeRecordingSession{
				recordingSession: newRecordingSession(),
				outcomes:         test.outcomes,
			}
			runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			errCh := make(chan error, 1)
			go func() { errCh <- runner.Run(ctx) }()
			session.recv.Write(ctx, messages.StreamMessage{
				Type:  messages.StreamTypeMessageStart,
				Value: messages.NewMessageStartValue(),
			})
			waitForDelta(t, ctx, runner, messages.StreamTypeMessageStart)
			runner.UserAudioInbox <- loudPCM()

			select {
			case err := <-errCh:
				if err == nil || !contains(err.Error(), "session") || !contains(err.Error(), "send failed") {
					t.Fatalf("Run error = %v, want propagated session send failure", err)
				}
				for _, fragment := range test.want {
					if !contains(err.Error(), fragment) {
						t.Fatalf("Run error = %v, want detail %q", err, fragment)
					}
				}
			case <-ctx.Done():
				t.Fatal("Run did not return after barge-in send failure")
			}
		})
	}
}

// A provider SESSION.CLOSE ends the session. An inference request the loop
// schedules afterwards (such as the follow-up to a final tool result) must not
// reach the closed wire as a late RESPONSE.CREATE or user turn.
func TestSessionModelRunner_InferenceRequestAfterSessionCloseSendsNothing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		history []messages.Message
	}{
		{
			name: "tool result continuation",
			history: []messages.Message{
				{Role: messages.RoleUser},
				{Role: messages.RoleAssistant, ToolCalls: []messages.ToolCall{{ID: "call-1", Name: "read_file"}}},
				{Role: messages.RoleTool, ToolCallID: "call-1", ContentParts: []messages.ContentPart{messages.NewTextPart("done")}},
			},
		},
		{
			name:    "user text",
			history: []messages.Message{messages.NewTextMessage(messages.RoleUser, "hello")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := newRecordingSession()
			runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			// The provider close is queued before the runner starts, so the
			// runner's provider preflight observes it before it reads the
			// inference request.
			session.recv.Write(ctx, messages.StreamMessage{
				Type:  messages.StreamTypeSessionClose,
				Value: messages.NewSessionCloseValue("", "session_closed"),
			})
			runner.Inbox.Write(ctx, messages.InferenceRequest{Messages: tc.history})

			errCh := make(chan error, 1)
			go func() { errCh <- runner.Run(ctx) }()
			waitForDelta(t, ctx, runner, messages.StreamTypeSessionClose)

			// The runner handles one select branch at a time. Once the request
			// has left the inbox, a later provider message is forwarded only
			// after the request has been fully handled.
			for runner.Inbox.Len() != 0 {
				select {
				case <-ctx.Done():
					t.Fatal("runner did not read the inference request")
				default:
					time.Sleep(time.Millisecond)
				}
			}
			session.recv.Write(ctx, messages.StreamMessage{
				Type:  messages.StreamTypeTextDelta,
				Value: messages.NewTextDeltaValue("barrier"),
			})
			waitForDelta(t, ctx, runner, messages.StreamTypeTextDelta)

			if sent := session.sentMessages(); len(sent) != 0 {
				t.Fatalf("sent %d messages after SESSION.CLOSE, want none: %#v", len(sent), sent)
			}
			closeRecordingSession(t, session)
			if err := <-errCh; err != nil {
				t.Fatalf("Run = %v, want nil", err)
			}
		})
	}
}

func audioDeltaContent(t *testing.T, value any) []byte {
	t.Helper()
	delta, ok := value.(*messages.AudioDeltaValue)
	if !ok {
		t.Fatalf("value = %T, want *messages.AudioDeltaValue", value)
	}
	return delta.Content
}

func toolCallEndValue(t *testing.T, value any) *messages.ToolCallEndValue {
	t.Helper()
	end, ok := value.(*messages.ToolCallEndValue)
	if !ok {
		t.Fatalf("value = %T, want *messages.ToolCallEndValue", value)
	}
	return end
}

func closeSessionForTest(t *testing.T, session interface{ Close() error }) {
	t.Helper()
	if err := session.Close(); err != nil {
		t.Fatalf("session Close() error = %v", err)
	}
}

// closeFailingSession reports a transport close failure after an otherwise
// clean session, like a websocket whose close handshake fails.
type closeFailingSession struct {
	*recordingSession
	closeErr error
}

func (s *closeFailingSession) Close() error {
	if err := s.recordingSession.Close(); err != nil {
		return err
	}
	return s.closeErr
}

func TestSessionModelRunnerCleanStopIgnoresSessionCloseError(t *testing.T) {
	session := &closeFailingSession{recordingSession: newRecordingSession(), closeErr: errors.New("websocket close failed")}
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- runner.Run(ctx) }()
	close(runner.UserAudioInbox)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run after clean stop with close error = %v, want nil", err)
		}
	case <-ctx.Done():
		t.Fatal("Run did not return after UserAudioInbox closed")
	}
}

func TestJoinOnFailureOnlyAttachesCleanupToFailures(t *testing.T) {
	primary := errors.New("run failed")
	cleanup := errors.New("close failed")
	if err := joinOnFailure(nil, cleanup); err != nil {
		t.Fatalf("joinOnFailure(nil, cleanup) = %v, want nil", err)
	}
	if err := joinOnFailure(primary, nil); err != primary { //nolint:errorlint // Identity must be preserved when there is no cleanup error.
		t.Fatalf("joinOnFailure(primary, nil) = %v, want the primary error itself", err)
	}
	if err := joinOnFailure(primary, cleanup); !errors.Is(err, primary) || !errors.Is(err, cleanup) {
		t.Fatalf("joinOnFailure(primary, cleanup) = %v, want both errors", err)
	}
}

// Response identity bookkeeping must not grow with session length. Only the
// most recent responses can still deliver a late event, so older identities
// are retired from the lookup sets.
func TestSessionModelRunner_ResponseIdentityBookkeepingStaysBounded(t *testing.T) {
	ctx := context.Background()
	session := newRecordingSession()
	runner := NewSessionModelRunner(nil, 16, nil)
	state := newSessionResponseState()
	for turn := range 1000 {
		id := fmt.Sprintf("resp-%04d", turn)
		runner.forwardSessionMessageState(ctx, session, state, sessionMessage(messages.StreamTypeMessageStart, id))
		if turn%2 == 0 {
			if err := runner.forwardSessionAudioWithPolicyWithState(ctx, session, loudPCM(), messages.SessionAudioInputPolicyInterrupt, state); err != nil {
				t.Fatal(err)
			}
		}
		if turn%3 == 0 { // a replacement start retires the response
			runner.forwardSessionMessageState(ctx, session, state, sessionMessage(messages.StreamTypeMessageStart, id+"-next"))
			id += "-next"
		}
		runner.forwardSessionMessageState(ctx, session, state, sessionMessage(messages.StreamTypeMessageEnd, id))
		for _, ok := runner.DeltaOutbox.Read(); ok; _, ok = runner.DeltaOutbox.Read() {
		}
	}
	if got := retainedResponseIDs(state); got > 3*responseIDRetention {
		t.Fatalf("retained %d response identities after 1000 responses, want at most %d", got, 3*responseIDRetention)
	}
	// A late event of a recent response is still recognised.
	if !state.terminalResponseIDs.has("resp-0998") {
		t.Fatal("recent terminal response identity was not retained")
	}
}

func retainedResponseIDs(state *sessionRunState) int {
	return state.cancelledResponseIDs.len() + state.retiredResponseIDs.len() + state.terminalResponseIDs.len()
}

// loudPCM returns 20 ms of 16 kHz PCM16 at speech level (about -12 dBFS).
func loudPCM() []byte {
	pcm := make([]byte, 640)
	for i := 0; i < len(pcm); i += 2 {
		sample := int16(8000)
		if i%4 == 0 {
			sample = -8000
		}
		pcm[i], pcm[i+1] = byte(uint16(sample)), byte(uint16(sample)>>8)
	}
	return pcm
}

// playbackSession is a provider session that owns local playback and reports
// whether the provider runs its own turn detection.
type playbackSession struct {
	*recordingSession
	providerVAD bool
	playback    messages.LocalPlaybackState
	interrupts  int
}

func (s *playbackSession) ProviderTurnDetection() bool                { return s.providerVAD }
func (s *playbackSession) LocalPlayback() messages.LocalPlaybackState { return s.playback }
func (s *playbackSession) InterruptLocalPlayback(context.Context) bool {
	s.interrupts++
	s.playback = messages.LocalPlaybackState{}
	return true
}

// pcmAtLevel returns 20 ms of 16 kHz PCM16 whose RMS is level.
func pcmAtLevel(level int16) []byte {
	pcm := make([]byte, 640)
	for i := 0; i < len(pcm); i += 2 {
		sample := level
		if i%4 == 0 {
			sample = -level
		}
		pcm[i], pcm[i+1] = byte(uint16(sample)), byte(uint16(sample)>>8)
	}
	return pcm
}

func sendUserAudio(t *testing.T, runner *ModelRunner, session messages.Session, state *sessionRunState, pcm []byte) {
	t.Helper()
	if err := runner.forwardSessionAudioWithPolicyWithState(context.Background(), session, pcm, messages.SessionAudioInputPolicyInterrupt, state); err != nil {
		t.Fatalf("forward user audio: %v", err)
	}
}

// The response is done but seconds of its audio are still playing (the
// provider delivers faster than real time). The user hears the agent talking
// and speaks over it: that must stop playback -- there is no response left to
// cancel, so the interrupt is local playback plus truncation.
func TestSessionModelRunner_SpeechInterruptsPlaybackAfterResponseDone(t *testing.T) {
	session := &playbackSession{recordingSession: newRecordingSession()}
	runner := NewSessionModelRunner(nil, 16, nil)
	state := newInFlightRunState(t, session, runner, "resp-done")
	runner.forwardSessionMessageState(context.Background(), session, state, sessionMessage(messages.StreamTypeMessageEnd, "resp-done"))
	session.playback = messages.LocalPlaybackState{Active: true, Level: 1500}

	sendUserAudio(t, runner, session, state, pcmAtLevel(8000))
	if session.interrupts != 1 || countSent(session.sentMessages(), messages.StreamTypeResponseCancel) != 0 {
		t.Fatalf("playback interrupts=%d cancels=%d, want one local playback interrupt and no RESPONSE.CANCEL", session.interrupts, countSent(session.sentMessages(), messages.StreamTypeResponseCancel))
	}
}

// Without echo cancellation the microphone hears the agent's own playback at
// about the playback level. That echo alone must not barge in, while real
// speech clearly above the playback does.
func TestSessionModelRunner_EchoAloneDoesNotBargeInButSpeechOverPlaybackDoes(t *testing.T) {
	session := &playbackSession{recordingSession: newRecordingSession(), playback: messages.LocalPlaybackState{Active: true, Level: 6000}}
	runner := NewSessionModelRunner(nil, 16, nil)
	state := newInFlightRunState(t, session, runner, "resp-playing")
	for range 25 { // half a second of unity-gain echo
		sendUserAudio(t, runner, session, state, pcmAtLevel(6000))
	}
	if got := countSent(session.sentMessages(), messages.StreamTypeResponseCancel); got != 0 || session.interrupts != 0 {
		t.Fatalf("echo of playback: cancels=%d interrupts=%d, want none", got, session.interrupts)
	}
	sendUserAudio(t, runner, session, state, pcmAtLevel(20000))
	if got := countSent(session.sentMessages(), messages.StreamTypeResponseCancel); got != 1 {
		t.Fatalf("speech over playback: cancels=%d, want 1", got)
	}
}

// Background noise below speech level never barges in.
func TestSessionModelRunner_QuietNoiseDoesNotBargeIn(t *testing.T) {
	session := &playbackSession{recordingSession: newRecordingSession()}
	runner := NewSessionModelRunner(nil, 16, nil)
	state := newInFlightRunState(t, session, runner, "resp-noise")
	for range 50 {
		sendUserAudio(t, runner, session, state, pcmAtLevel(60))
	}
	if got := countSent(session.sentMessages(), messages.StreamTypeResponseCancel); got != 0 {
		t.Fatalf("noise cancels=%d, want 0", got)
	}
}

// When the provider runs turn detection it stops local playback on
// speech_started itself. The runner still cancels the response before the
// speech reaches the provider, but leaves playback (and echo judgement) to the
// provider.
func TestSessionModelRunner_ProviderTurnDetectionOwnsPlayback(t *testing.T) {
	session := &playbackSession{recordingSession: newRecordingSession(), providerVAD: true, playback: messages.LocalPlaybackState{Active: true, Level: 12000}}
	runner := NewSessionModelRunner(nil, 16, nil)
	state := newInFlightRunState(t, session, runner, "resp-vad")
	sendUserAudio(t, runner, session, state, pcmAtLevel(3000))
	sent := session.sentMessages()
	if countSent(sent, messages.StreamTypeResponseCancel) != 1 || messages.CancelStopsPlayback(sent[0]) || session.interrupts != 0 {
		t.Fatalf("provider VAD barge-in sent=%#v interrupts=%d, want one playback-keeping cancel", sent, session.interrupts)
	}
}

// A user turn's response request (MESSAGE.END: commit + response.create) that
// has been sent but not yet opened is answered before a continuation requested
// after it. The first response to open is the user's turn, not the
// continuation: it must stay interruptible, and the continuation is the next.
func TestSessionModelRunner_ContinuationBindsToItsOwnRequestNotAnEarlierUserTurn(t *testing.T) {
	ctx := context.Background()
	session := newRecordingSession()
	runner := NewSessionModelRunner(nil, 16, nil)
	state := newSessionResponseState()

	runner.forwardQueuedSessionEvent(ctx, session, state, messages.StreamMessage{Type: messages.StreamTypeMessageEnd})
	runner.forwardQueuedSessionEvent(ctx, session, state, continuationCreate())
	runner.forwardSessionMessageState(ctx, session, state, sessionMessage(messages.StreamTypeMessageStart, "resp-user"))
	if state.continuationInFlight {
		t.Fatalf("user-turn response was bound as the tool continuation: %+v", state)
	}
	sendUserAudio(t, runner, session, state, loudPCM())
	if got := countSent(session.sentMessages(), messages.StreamTypeResponseCancel); got != 1 {
		t.Fatalf("user-turn response was not interruptible: cancels=%d", got)
	}
	runner.forwardSessionMessageState(ctx, session, state, sessionMessage(messages.StreamTypeMessageEnd, "resp-user"))
	runner.forwardSessionMessageState(ctx, session, state, sessionMessage(messages.StreamTypeMessageStart, "resp-continuation"))
	if !state.continuationInFlight || state.continuationResponseID != "resp-continuation" {
		t.Fatalf("continuation bound to %q, want resp-continuation", state.continuationResponseID)
	}
	sendUserAudio(t, runner, session, state, loudPCM())
	if got := countSent(session.sentMessages(), messages.StreamTypeResponseCancel); got != 1 {
		t.Fatalf("tool continuation was cancelled: cancels=%d", got)
	}
}
