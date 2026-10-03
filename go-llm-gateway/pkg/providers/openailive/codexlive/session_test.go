package codexlive_test

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai/chatgptauth"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexlive"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc/fakecodex"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/quicksilver"
)

// ConnectSession creates the offer, creates the call on the ChatGPT backend
// with the signed-in store's token and the client version, applies the
// answer, attaches the sideband and opens the session under the call id.
func TestConnectCreatesTheCallOnTheChatGPTLoginAndOpensTheSession(t *testing.T) {
	ctx := deadline(t)
	r := newRig(t)
	session := r.connect(t, ctx)

	open := next(t, ctx, session)
	if value, ok := open.Value.(*messages.SessionOpenValue); open.Type != messages.StreamTypeSessionOpen || !ok || value.SessionID != fakecodex.DefaultCallID {
		t.Fatalf("first message = %s %+v, want SESSION.OPEN for the call", open.Type, open.Value)
	}
	created := next(t, ctx, session)
	if value, ok := created.Value.(*messages.SessionCreatedValue); !ok || value.Model != quicksilver.ModelCodex {
		t.Fatalf("second message = %s %+v, want SESSION.CREATED for gpt-live-1-codex", created.Type, created.Value)
	}
	calls := r.backend.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	call := calls[0]
	if call.Header.Get(codexrtc.HeaderVersion) != testVersion || call.Header.Get(codexrtc.HeaderOriginator) != codexrtc.DefaultOriginator {
		t.Fatalf("call headers = %v, want the client version and originator", call.Header)
	}
	if call.Session.Model != quicksilver.ModelCodex || call.Session.Audio.Output.Voice != quicksilver.DefaultVoice || call.Session.Delegation.Type != quicksilver.DelegationClient {
		t.Fatalf("call session = %+v", call.Session)
	}
	if !strings.Contains(call.OfferSDP, "opus/48000/2") {
		t.Fatal("the offer does not negotiate Opus")
	}
	if r.backend.Sidebands() != 1 {
		t.Fatalf("sidebands = %d, want 1", r.backend.Sidebands())
	}
	duplex, ok := session.(messages.SessionFullDuplex)
	if !ok || !duplex.FullDuplex() {
		t.Fatal("the session is not full duplex")
	}
	if requester, ok := session.(messages.SessionResponseCapability); !ok || requester.SupportsResponseRequests() {
		t.Fatal("the session accepts response requests")
	}
	if errs := r.backend.Errors(); len(errs) != 0 {
		t.Fatalf("backend errors: %v", errs)
	}
}

// Microphone PCM at 24 kHz reaches the backend's peer as 48 kHz Opus with
// its pitch intact, and the backend's 48 kHz Opus reaches the stream as
// 24 kHz PCM in a speech segment, pitch intact both ways.
func TestAudioFlowsBothWaysThroughOpusWithResampling(t *testing.T) {
	ctx := deadline(t)
	r := newRig(t)
	session := r.connect(t, ctx)
	skipOpen(t, ctx, session)

	const hz, seconds = 440.0, 1
	sendAudio(t, ctx, session, codec.EncodePCM16(tone(hz, 24000, 0, 24000*seconds)))
	var received []int16
	for len(received) < codexrtc.SampleRate*seconds*3/4 {
		frame, err := r.backend.Peer().ReadFrame(ctx)
		if err != nil {
			t.Fatalf("backend peer read: %v", err)
		}
		received = append(received, frame...)
	}
	// Skip the codec's start-up frames before measuring the pitch.
	if got := crossingsPerSecond(received[codexrtc.SampleRate/5:], codexrtc.SampleRate); got < hz*0.9 || got > hz*1.1 {
		t.Fatalf("input pitch at the backend = %.0f Hz, want about %.0f Hz", got, hz)
	}

	r.sendPeerTone(t, ctx, hz, 50)
	var output []int16
	var types []messages.StreamMessageType
	for len(output) < 24000*3/4 {
		msg := next(t, ctx, session)
		types = append(types, msg.Type)
		value, ok := msg.Value.(*messages.AudioDeltaValue)
		if !ok {
			continue
		}
		if msg.ResponseID != firstSeg {
			t.Fatalf("audio in %q, want segment %q", msg.ResponseID, firstSeg)
		}
		samples, err := codec.DecodePCM16(value.Content)
		if err != nil {
			t.Fatal(err)
		}
		output = append(output, samples...)
	}
	if types[0] != messages.StreamTypeMessageStart || types[1] != messages.StreamTypeAudioStart {
		t.Fatalf("segment opened with %v, want MESSAGE.START then AUDIO.START", types[:2])
	}
	if got := crossingsPerSecond(output[24000/5:], 24000); got < hz*0.9 || got > hz*1.1 {
		t.Fatalf("output pitch on the stream = %.0f Hz, want about %.0f Hz", got, hz)
	}
}

// Transcripts and turn.done map onto segments and utterances: assistant
// fragments open a segment that turn.done closes completed with the joined
// text; user fragments form an utterance that turn.done closes with the
// final transcript.
func TestTranscriptsAndTurnsMapOntoSegmentsAndUtterances(t *testing.T) {
	ctx := deadline(t)
	r := newRig(t)
	session := r.connect(t, ctx)
	skipOpen(t, ctx, session)

	if err := r.backend.Send(outputText("Hello"), outputText(" there."), turnDone(quicksilver.RoleAssistant, "Hello there.")); err != nil {
		t.Fatal(err)
	}
	segment := until(t, ctx, session, messages.StreamTypeMessageEnd)
	want := []messages.StreamMessageType{messages.StreamTypeMessageStart, messages.StreamTypeTranscriptDelta, messages.StreamTypeTranscriptDelta, messages.StreamTypeTranscriptEnd, messages.StreamTypeMessageEnd}
	if got := typesOf(segment); !slices.Equal(got, want) {
		t.Fatalf("segment = %v, want %v", got, want)
	}
	if end, ok := segment[3].Value.(*messages.TranscriptEndValue); !ok || end.FullText != "Hello there." || segment[3].Role != messages.RoleAssistant {
		t.Fatalf("assistant transcript = %+v", segment[3].Value)
	}
	if end := messageEnd(t, segment[4]); end.Status != completed || segment[4].ResponseID != firstSeg {
		t.Fatalf("segment end = %q in %q", end.Status, segment[4].ResponseID)
	}

	if err := r.backend.Send(inputText("what time"), inputText(" is it"), turnDone(quicksilver.RoleUser, "What time is it?")); err != nil {
		t.Fatal(err)
	}
	utterance := until(t, ctx, session, messages.StreamTypeTranscriptEnd)
	want = []messages.StreamMessageType{messages.StreamTypeInputItemAdded, messages.StreamTypeTranscriptDelta, messages.StreamTypeTranscriptDelta, messages.StreamTypeTranscriptEnd}
	if got := typesOf(utterance); !slices.Equal(got, want) {
		t.Fatalf("utterance = %v, want %v", got, want)
	}
	if end, ok := utterance[3].Value.(*messages.TranscriptEndValue); !ok || end.FullText != "What time is it?" || end.ItemID != firstUtt || utterance[3].Role != messages.RoleUser {
		t.Fatalf("user transcript end = %+v", utterance[3].Value)
	}
}

// A server-side barge-in (output_audio_buffer.cleared) ends the open segment
// cancelled; a user transcript during the segment changes nothing, and the
// next output opens a new segment.
func TestServerBargeInEndsTheSegmentAndUserOverlapDoesNot(t *testing.T) {
	ctx := deadline(t)
	r := newRig(t)
	session := r.connect(t, ctx)
	skipOpen(t, ctx, session)

	if err := r.backend.Send(outputText("Let me explain"), inputText("wait")); err != nil {
		t.Fatal(err)
	}
	first := until(t, ctx, session, messages.StreamTypeTranscriptDelta)
	if first[len(first)-1].ResponseID != firstSeg {
		t.Fatalf("assistant fragment in %q", first[len(first)-1].ResponseID)
	}
	overlap := until(t, ctx, session, messages.StreamTypeTranscriptDelta)
	if got := typesOf(overlap); !slices.Equal(got, []messages.StreamMessageType{messages.StreamTypeInputItemAdded, messages.StreamTypeTranscriptDelta}) || overlap[1].Role != messages.RoleUser {
		t.Fatalf("user overlap = %v, want only the user utterance", got)
	}
	if err := r.backend.Send(quicksilver.OutputAudioBufferCleared{}, outputText("Sure.")); err != nil {
		t.Fatal(err)
	}
	cleared := until(t, ctx, session, messages.StreamTypeMessageEnd)
	if end := messageEnd(t, cleared[len(cleared)-1]); end.Status != cancelled || cleared[len(cleared)-1].ResponseID != firstSeg {
		t.Fatalf("cleared segment ended %q in %q, want cancelled", end.Status, cleared[len(cleared)-1].ResponseID)
	}
	resumed := until(t, ctx, session, messages.StreamTypeTranscriptDelta)
	if resumed[0].Type != messages.StreamTypeMessageStart || resumed[len(resumed)-1].ResponseID != secondSeg {
		t.Fatalf("output after the barge-in = %v in %q, want a new segment", typesOf(resumed), resumed[len(resumed)-1].ResponseID)
	}
}

// Close sends session.close on the sideband; the backend's normal close
// ends the session with close_requested.
func TestCloseRunsTheSessionCloseHandshake(t *testing.T) {
	ctx := deadline(t)
	r := newRig(t)
	session, err := r.provider().ConnectSession(ctx, codexConfig())
	if err != nil {
		t.Fatal(err)
	}
	r.awaitSideband(t, ctx)
	skipOpen(t, ctx, session)
	if err := r.backend.Send(outputText("Bye")); err != nil {
		t.Fatal(err)
	}
	until(t, ctx, session, messages.StreamTypeTranscriptDelta)
	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	rest := until(t, ctx, session, messages.StreamTypeSessionClose)
	closed := sessionClose(t, rest[len(rest)-1])
	if closed.Reason != quicksilverCloseRequested || closed.TerminalReason != messages.TerminalReasonSessionClose {
		t.Fatalf("SESSION.CLOSE = %+v, want close_requested", closed)
	}
	if end := messageEnd(t, rest[len(rest)-2]); end.Status != completed {
		t.Fatalf("open segment ended %q at close", end.Status)
	}
	events := r.backend.ClientEvents()
	if len(events) == 0 {
		t.Fatal("the backend received no client event")
	}
	if _, ok := events[len(events)-1].(quicksilver.SessionClose); !ok {
		t.Fatalf("last client event = %T, want session.close", events[len(events)-1])
	}
	if terminal, ok := session.(messages.SessionTerminalError); ok && terminal.TerminalError() != nil {
		t.Fatalf("terminal error after a requested close: %v", terminal.TerminalError())
	}
}

// A backend that never answers session.close does not hold Close past the
// close timeout.
func TestCloseIsBoundedByTheCloseTimeout(t *testing.T) {
	ctx := deadline(t)
	r := newRig(t, fakecodex.WithSidebandStall())
	session, err := r.provider(codexlive.WithCloseTimeout(200*time.Millisecond)).ConnectSession(ctx, codexConfig())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("Close took %v, want it bounded by the close timeout", elapsed)
	}
	<-session.Done()
}

// quicksilverCloseRequested is the reason of a close the client asked for.
const quicksilverCloseRequested = "close_requested"

// The backend hanging up the sideband ends the session as a provider close.
func TestBackendHangUpEndsTheSession(t *testing.T) {
	ctx := deadline(t)
	r := newRig(t)
	session := r.connect(t, ctx)
	skipOpen(t, ctx, session)
	if err := r.backend.HangUp(); err != nil {
		t.Fatal(err)
	}
	rest := until(t, ctx, session, messages.StreamTypeSessionClose)
	if closed := sessionClose(t, rest[len(rest)-1]); closed.Reason != "remote_hangup" || closed.TerminalReason != messages.TerminalReasonProviderClose {
		t.Fatalf("SESSION.CLOSE = %+v, want remote_hangup", closed)
	}
}

// A dropped sideband is redialed with the same identity; events after the
// reconnect reach the stream, and the session survives.
func TestADroppedSidebandReconnects(t *testing.T) {
	ctx := deadline(t)
	r := newRig(t)
	session := r.connect(t, ctx)
	skipOpen(t, ctx, session)
	if err := r.backend.DropSideband(); err != nil {
		t.Fatal(err)
	}
	if err := r.backend.WaitSidebands(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if err := r.backend.Send(outputText("Still here."), turnDone(quicksilver.RoleAssistant, "Still here.")); err != nil {
		t.Fatal(err)
	}
	segment := until(t, ctx, session, messages.StreamTypeMessageEnd)
	if end := messageEnd(t, segment[len(segment)-1]); end.Status != completed {
		t.Fatalf("segment after the reconnect ended %q", end.Status)
	}
	if errs := r.backend.Errors(); len(errs) != 0 {
		t.Fatalf("backend errors (the reconnect must repeat the call's identity): %v", errs)
	}
}

// A credential error event ends the session with a terminal error that says
// to sign in again.
func TestACredentialErrorEventIsFatal(t *testing.T) {
	for _, code := range []string{"invalid_token", "authentication_error", "token_expired"} {
		t.Run(code, func(t *testing.T) {
			ctx := deadline(t)
			r := newRig(t)
			session := r.connect(t, ctx)
			skipOpen(t, ctx, session)
			if err := r.backend.Send(quicksilver.ErrorEvent{Error: &quicksilver.ErrorDetail{Code: code}}); err != nil {
				t.Fatal(err)
			}
			assertSignInAgain(t, until(t, ctx, session, messages.StreamTypeSessionClose), session)
		})
	}
}

// A sideband redial the backend answers 401 is fatal the same way.
func TestARejectedReconnectIsFatal(t *testing.T) {
	ctx := deadline(t)
	r := newRig(t)
	session := r.connect(t, ctx)
	skipOpen(t, ctx, session)
	r.backend.RejectSidebands(http.StatusUnauthorized)
	if err := r.backend.DropSideband(); err != nil {
		t.Fatal(err)
	}
	assertSignInAgain(t, until(t, ctx, session, messages.StreamTypeSessionClose), session)
}

// A call the backend no longer knows ends the session without retries.
func TestAReconnectToAnEndedCallEndsTheSession(t *testing.T) {
	ctx := deadline(t)
	r := newRig(t)
	session := r.connect(t, ctx)
	skipOpen(t, ctx, session)
	r.backend.RejectSidebands(http.StatusGone)
	if err := r.backend.DropSideband(); err != nil {
		t.Fatal(err)
	}
	rest := until(t, ctx, session, messages.StreamTypeSessionClose)
	if closed := sessionClose(t, rest[len(rest)-1]); closed.Reason != "call_ended" || closed.TerminalReason != messages.TerminalReasonProviderClose {
		t.Fatalf("SESSION.CLOSE = %+v, want call_ended", closed)
	}
}

func assertSignInAgain(t *testing.T, rest []messages.StreamMessage, session messages.Session) {
	t.Helper()
	closed := sessionClose(t, rest[len(rest)-1])
	if closed.Reason != "authentication_failed" || closed.TerminalReason != messages.TerminalReasonTerminalFailure {
		t.Fatalf("SESSION.CLOSE = %+v, want a terminal authentication failure", closed)
	}
	failure, ok := rest[len(rest)-2].Value.(*messages.ErrorValue)
	if !ok || failure.IsNonTerminal() || !strings.Contains(failure.Message, "sign in again") || !errors.Is(failure.Err, codexlive.ErrSignInAgain) {
		t.Fatalf("error before the close = %+v, want the terminal sign-in error", rest[len(rest)-2].Value)
	}
	terminal, ok := session.(messages.SessionTerminalError)
	if !ok || !errors.Is(terminal.TerminalError(), codexlive.ErrSignInAgain) {
		t.Fatal("the session's terminal error is not the sign-in error")
	}
	<-session.Done()
}

// Call creation rejected with 401, or no stored login, fails ConnectSession
// with the sign-in error before any session exists.
func TestConnectFailsFastWithoutAUsableLogin(t *testing.T) {
	ctx := deadline(t)
	rejected := newRig(t, fakecodex.WithCallStatus(http.StatusUnauthorized))
	if _, err := rejected.provider().ConnectSession(ctx, codexConfig()); !errors.Is(err, codexlive.ErrSignInAgain) {
		t.Fatalf("ConnectSession with a rejected token = %v, want the sign-in error", err)
	}
	missing := newRig(t)
	missing.store = signIn(t, "", "")
	_, err := missing.provider().ConnectSession(ctx, codexConfig())
	if !errors.Is(err, codexlive.ErrSignInAgain) || !errors.Is(err, chatgptauth.ErrNotLoggedIn) {
		t.Fatalf("ConnectSession without a login = %v, want the sign-in error", err)
	}
	if calls := missing.backend.Calls(); len(calls) != 0 {
		t.Fatalf("a call was created without a login: %d", len(calls))
	}
	if _, err := codexlive.New().ConnectSession(ctx, codexConfig()); !errors.Is(err, codexlive.ErrCredentialRequired) {
		t.Fatalf("ConnectSession without credentials = %v", err)
	}
}

// Against the fake backend: a client delegation.created reaches the stream as
// DELEGATION.CREATED with its task, and the result sent back as
// CONTEXT.APPEND reaches the backend as delegation.context.append on the
// speakable channel.
func TestADelegationRoundTripsThroughTheBackend(t *testing.T) {
	ctx := deadline(t)
	r := newRig(t)
	session := r.connect(t, ctx)
	skipOpen(t, ctx, session)
	if err := r.backend.Send(
		inputText("book a table"),
		quicksilver.DelegationCreated{Item: quicksilver.DelegationItem{
			ID: "del_1", Type: quicksilver.ItemTypeDelegation, Target: quicksilver.TargetClient,
			Content: []quicksilver.ContentPart{{Type: quicksilver.PartInputText, Text: "Book a table for two."}},
		}},
	); err != nil {
		t.Fatal(err)
	}
	got := until(t, ctx, session, messages.StreamTypeDelegationCreated)
	delegation, ok := got[len(got)-1].Value.(*messages.DelegationCreatedValue)
	if !ok || delegation.ID != "del_1" || delegation.Task != "Book a table for two." || got[len(got)-1].ResponseID != "" {
		t.Fatalf("DELEGATION.CREATED = %+v", got[len(got)-1].Value)
	}
	id := delegation.ID
	outcome := messages.SendSessionWithOutcome(ctx, session, messages.StreamMessage{
		Type:  messages.StreamTypeContextAppend,
		Value: &messages.ContextAppendValue{Kind: messages.ContextAppendCommentary, DelegationID: &id, Content: "Booked for seven."},
	})
	if !outcome.OK() {
		t.Fatalf("CONTEXT.APPEND: %+v", outcome)
	}
	events, err := r.backend.WaitClientEvents(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := events[0].(quicksilver.DelegationContextAppend)
	if !ok || result.DelegationItemID != "del_1" || result.Channel != quicksilver.ChannelSpeakable || result.Content[0].Text != "Booked for seven." {
		t.Fatalf("client event = %#v, want the result on the speakable channel", events[0])
	}
}
