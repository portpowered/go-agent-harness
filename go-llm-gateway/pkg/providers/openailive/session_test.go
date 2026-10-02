package openailive_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	live "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/fakelive"
)

func TestConnectSessionWaitsForSessionStartedWithCredentialHeaders(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newFake()
		session := connectFake(t, server, models.SessionConfig{Model: live.Model1, Voice: "cedar", InputAudioSampleRate: models.SampleRate16000, OutputAudioSampleRate: models.SampleRate16000})

		if open := next(t, session); open.Type != messages.StreamTypeSessionOpen {
			t.Fatalf("first message = %s, want SESSION.OPEN", open.Type)
		}
		created, ok := next(t, session).Value.(*messages.SessionCreatedValue)
		if !ok || created.SessionID != fakelive.DefaultSessionID || created.Model != live.Model1 {
			t.Fatalf("SESSION.CREATED = %+v, want the started session", created)
		}
		calls := server.DialCalls()
		if len(calls) != 1 || calls[0].Endpoint != live.DefaultEndpoint || calls[0].Headers["Authorization"] != "Bearer "+sessionKey {
			t.Fatalf("dial calls = %+v, want one bearer dial of the default endpoint", calls)
		}
		events := server.ClientEvents()
		start, ok := events[0].(live.SessionStart)
		if !ok || start.Session.Audio.Format.Rate != live.RatePCM16k || start.Session.Delegation.Type != live.DelegationClient {
			t.Fatalf("first client event = %#v, want session.start at 16 kHz with client delegation", events[0])
		}
		if rate := as[messages.SessionInputFormat](t, session).InputAudioSampleRate(); rate != live.RatePCM16k {
			t.Fatalf("InputAudioSampleRate = %d, want the session format", rate)
		}
	})
}

func TestConnectSessionReturnsTheStartupError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newFake()
		_, err := live.New(
			live.WithCredentialProvider(live.APIKeyCredentials(sessionKey)),
			live.WithWebSocketDialer(server.Dialer()),
		).ConnectSession(t.Context(), models.SessionConfig{Model: "gpt-live-2"})
		var startup *live.StartupError
		if !errors.As(err, &startup) || startup.Code != fakelive.CodeInvalidValue || startup.Param != "session.model" {
			t.Fatalf("ConnectSession = %v, want a typed startup error for session.model", err)
		}
		if server.CloseCount() != 1 {
			t.Fatalf("close count = %d, want the socket closed", server.CloseCount())
		}
	})
}

func TestConnectSessionRefusesMissingOrRejectedCredentials(t *testing.T) {
	server := newFake()
	cfg := pcmConfig()
	if _, err := live.New(live.WithWebSocketDialer(server.Dialer())).ConnectSession(t.Context(), cfg); !errors.Is(err, live.ErrCredentialProviderRequired) {
		t.Fatalf("no credential provider: %v", err)
	}
	failing := errors.New("token store locked")
	_, err := live.New(live.WithWebSocketDialer(server.Dialer()), live.WithCredentialProvider(func(context.Context) (map[string]string, error) {
		return nil, failing
	})).ConnectSession(t.Context(), cfg)
	if !errors.Is(err, failing) {
		t.Fatalf("failing credential provider: %v", err)
	}
	_, err = live.New(live.WithWebSocketDialer(server.Dialer()), live.WithCredentialProvider(live.APIKeyCredentials("sk-wrong"))).ConnectSession(t.Context(), cfg)
	if !errors.Is(err, fakelive.ErrUnauthorized) {
		t.Fatalf("wrong key: %v", err)
	}
	if _, err := live.New(live.WithCredentialProvider(live.APIKeyCredentials(sessionKey))).ConnectSession(t.Context(), cfg); err == nil {
		t.Fatal("ConnectSession without a dialer succeeded")
	}
	if _, err := live.New(live.WithWebSocketDialer(server.Dialer()), live.WithCredentialProvider(live.APIKeyCredentials(sessionKey))).ConnectSession(t.Context(), models.SessionConfig{}); !errors.Is(err, live.ErrInvalidSessionConfig) {
		t.Fatalf("empty model: %v", err)
	}
	headers, err := live.APIKeyCredentials(" ")(t.Context())
	if err != nil || len(headers) != 0 {
		t.Fatalf("blank key headers = %v, %v; want none", headers, err)
	}
}

func TestAudioRoundTripJoinsOddBytesAndSegmentsOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		output := bytes.Repeat([]byte{1, 2}, 2400) // 100 ms at 24 kHz
		server := newFake(fakelive.AwaitClient(live.TypeInputAudioAppend, 2), fakelive.Send(audioDelta(output)))
		session := connectFake(t, server, pcmConfig())
		skipOpen(t, session)

		sendAudio(t, session, []byte{1, 2, 3})
		sendAudio(t, session, []byte{4})
		sendAudio(t, session, []byte{5, 6})
		start := time.Now()
		received := collectUntil(t, session, start, messages.StreamTypeMessageEnd)

		want := []messages.StreamMessageType{messages.StreamTypeMessageStart, messages.StreamTypeAudioStart, messages.StreamTypeAudioDelta, messages.StreamTypeAudioEnd, messages.StreamTypeMessageEnd}
		if got := typesOf(received); !slices.Equal(got, want) {
			t.Fatalf("messages = %v, want %v", got, want)
		}
		delta, ok := received[2].msg.Value.(*messages.AudioDeltaValue)
		if !ok || !bytes.Equal(delta.Content, output) || delta.MediaType != live.AudioTypePCM {
			t.Fatalf("AUDIO.DELTA = %+v, want the output audio as audio/pcm", received[2].msg.Value)
		}
		for _, r := range received {
			if r.msg.ResponseID != firstSeg {
				t.Fatalf("%s response id = %q, want live_seg_1", r.msg.Type, r.msg.ResponseID)
			}
		}
		end := messageEnd(t, received[4].msg)
		if end.Status != completed || end.TerminalReason != messages.TerminalReasonProviderAuthoredCompletion {
			t.Fatalf("MESSAGE.END = %+v, want completed", end)
		}
		// The segment ends one gap after its audio would finish playing.
		if at := received[4].at; at != 100*time.Millisecond+live.DefaultSegmentGap {
			t.Fatalf("segment ended after %v, want %v", at, 100*time.Millisecond+live.DefaultSegmentGap)
		}
		if got := server.InputAudio(); !bytes.Equal(got, []byte{1, 2, 3, 4, 5, 6}) {
			t.Fatalf("server input audio = %v, want whole samples in order", got)
		}
	})
}

func TestTranscriptsMapToSegmentAndUtteranceStreams(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newFake(fakelive.AwaitStarted(), fakelive.Send(
			inputText("A table ", 1000, 1400),
			inputText("for two.", 1400, 1800),
			outputText("Would you ", 2000, 2300),
			outputText("like it?", 2300, 2600),
		))
		session := connectFake(t, server, pcmConfig())
		skipOpen(t, session)
		got := transcriptsOf(t, collectUntil(t, session, time.Now(), messages.StreamTypeMessageEnd))
		user, userEnd, assistant, assistantEnd, added := got.user, got.userEnd, got.assistant, got.assistantEnd, got.added
		if added != 1 || user != "A table for two." || userEnd == nil || userEnd.FullText != user || userEnd.ItemID != firstUtt {
			t.Fatalf("user transcript %q (end %+v, %d items), want one utterance", user, userEnd, added)
		}
		if assistant != "Would you like it?" || assistantEnd == nil || assistantEnd.FullText != assistant {
			t.Fatalf("assistant transcript %q (end %+v), want the segment text", assistant, assistantEnd)
		}
	})
}

func TestSegmentsCloseAtServerTimelineGapsAndOnceEach(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newFake(fakelive.AwaitStarted(), fakelive.Send(
			outputText("First.", 0, 400),
			outputText("Second.", 400+live.DefaultSegmentGap.Milliseconds(), 1500),
		))
		session := connectFake(t, server, pcmConfig())
		skipOpen(t, session)
		start := time.Now()
		first := collectUntil(t, session, start, messages.StreamTypeMessageEnd)
		second := collectUntil(t, session, start, messages.StreamTypeMessageEnd)

		if first[len(first)-1].msg.ResponseID != firstSeg || first[len(first)-1].at != 0 {
			t.Fatalf("first segment ended %+v, want live_seg_1 closed by the timeline gap at once", first[len(first)-1])
		}
		if second[0].msg.Type != messages.StreamTypeMessageStart || second[0].msg.ResponseID != secondSeg {
			t.Fatalf("second segment opened with %+v", second[0].msg)
		}
		if end := second[len(second)-1]; end.msg.ResponseID != secondSeg || end.at != live.DefaultSegmentGap {
			t.Fatalf("second segment ended %+v, want the idle gap", end)
		}
	})
}

func TestUserSpeechDuringASegmentDropsNoOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newFake(fakelive.AwaitStarted(), fakelive.Send(
			audioDelta([]byte{1, 0}),
			outputText("I can help ", 0, 300),
			inputText("mm-hmm", 200, 400),
			delegation("del_1"),
			audioDelta([]byte{2, 0}),
			outputText("with that.", 300, 600),
		))
		session := connectFake(t, server, pcmConfig())
		skipOpen(t, session)
		received := collectUntil(t, session, time.Now(), messages.StreamTypeMessageEnd)

		audio, text := 0, ""
		for _, r := range received {
			switch value := r.msg.Value.(type) {
			case *messages.AudioDeltaValue:
				audio++
			case *messages.TranscriptDeltaValue:
				if r.msg.Role == messages.RoleAssistant {
					text += value.Text
				}
			case *messages.MessageEndValue:
				if value.Status != completed {
					t.Fatalf("segment ended %q, want completed", value.Status)
				}
			}
			if r.msg.Type == messages.StreamTypeMessageStart && r.msg.ResponseID != firstSeg {
				t.Fatalf("overlap opened another segment %q", r.msg.ResponseID)
			}
		}
		if audio != 2 || text != "I can help with that." {
			t.Fatalf("segment output = %d audio deltas, text %q; want everything", audio, text)
		}
	})
}

func TestResponseCancelEndsTheSegmentAndDropsItsLaterOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newFake(
			fakelive.AwaitStarted(), fakelive.Send(audioDelta([]byte{1, 0})),
			fakelive.AwaitClient(live.TypeInputAudioAppend, 1),
			fakelive.Send(audioDelta([]byte{2, 0}), outputText("late", 100, 200)),
			fakelive.Wait(2*live.DefaultSegmentGap),
			fakelive.Send(audioDelta([]byte{3, 0})),
		)
		session := connectFake(t, server, pcmConfig())
		skipOpen(t, session)
		start := time.Now()
		collectUntil(t, session, start, messages.StreamTypeAudioDelta)

		// The cancel's context is short-lived: ending it must neither close
		// the session nor lose the segment's closing messages.
		cancelCtx, endCancel := context.WithCancel(t.Context())
		outcome := messages.SendSessionWithOutcome(cancelCtx, session, messages.StreamMessage{Type: messages.StreamTypeResponseCancel, Value: messages.NewResponseCancelValue()})
		endCancel()
		if !outcome.OK() {
			t.Fatalf("cancel: %+v", outcome)
		}
		cancelled := collectUntil(t, session, start, messages.StreamTypeMessageEnd)
		if end := messageEnd(t, cancelled[len(cancelled)-1].msg); end.Status != "cancelled" || end.TerminalReason != messages.TerminalReasonCancellation {
			t.Fatalf("cancelled segment ended %+v", end)
		}
		sendAudio(t, session, []byte{0, 0})
		resumed := collectUntil(t, session, start, messages.StreamTypeAudioDelta)
		if resumed[0].msg.ResponseID != secondSeg {
			t.Fatalf("after the cancel the next output = %+v, want a fresh segment", resumed[0].msg)
		}
		for _, r := range resumed {
			if value, ok := r.msg.Value.(*messages.AudioDeltaValue); ok && value.Content[0] != 3 {
				t.Fatalf("cancelled output %v reached the stream", value.Content)
			}
		}
	})
}

func TestSendRefusesMessagesWithNoWireEvent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newFake()
		session := connectFake(t, server, pcmConfig())
		skipOpen(t, session)
		for _, msg := range []messages.StreamMessage{
			{Type: messages.StreamTypeResponseCreate, Value: messages.NewResponseCreateValue()},
			{Type: messages.StreamTypeToolCallEnd, Value: messages.NewToolCallEndValue("call_1", "lookup", "{}")},
			{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("typed")},
			{Type: messages.StreamTypeAudioDelta},
		} {
			outcome := messages.SendSessionWithOutcome(t.Context(), session, msg)
			if outcome.Status != messages.SessionSendTerminalFailure || !errors.Is(outcome.Err, live.ErrNoWireEvent) {
				t.Fatalf("Send(%s) = %+v, want a terminal failure", msg.Type, outcome)
			}
		}
		for _, msg := range []messages.StreamMessage{
			{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
			{Type: messages.StreamTypeSessionUpdate, Value: messages.NewSessionUpdateValue(&messages.SessionUpdateConfig{Instructions: "x"})},
			{Type: messages.StreamTypeResponseCancel, Value: messages.NewResponseCancelValue()},
		} {
			if !session.Send(t.Context(), msg) {
				t.Fatalf("Send(%s) failed, want a local success", msg.Type)
			}
		}
		if messages.SupportsSessionResponseRequests(session) {
			t.Fatal("session reports response requests")
		}
		if outcome := messages.RequestSessionResponse(t.Context(), session); outcome.Status != messages.SessionSendTerminalFailure {
			t.Fatalf("RequestSessionResponse = %+v, want a terminal failure", outcome)
		}
		detector, ok := session.(messages.SessionTurnDetection)
		if !ok || !detector.ProviderTurnDetection() {
			t.Fatal("session does not own turn detection")
		}
		if marker, ok := session.(messages.SessionInitialConfigMarker); !ok || !marker.InitialSessionConfigSent() {
			t.Fatal("session does not report its startup configuration as sent")
		}
		if events := server.ClientEvents(); len(events) != 1 {
			t.Fatalf("client events = %d, want only session.start on the wire", len(events))
		}
		cancelled, cancel := context.WithCancel(t.Context())
		cancel()
		if outcome := messages.SendSessionWithOutcome(cancelled, session, messages.StreamMessage{Type: messages.StreamTypeMessageEnd}); outcome.Status != messages.SessionSendCancelled {
			t.Fatalf("cancelled send = %+v", outcome)
		}
	})
}

func TestErrorEventsAreNonTerminalDiagnostics(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		param := "audio"
		server := newFake(fakelive.AwaitStarted(), fakelive.Send(live.ErrorEvent{EventID: "evt_err", Error: live.Error{
			Type: live.ErrorTypeInvalidRequest, Code: live.CodeInvalidAudio, Message: "odd PCM", Param: &param, ClientEventID: "evt_audio_1",
		}}, live.ErrorEvent{Error: live.Error{Type: live.ErrorTypeInvalidRequest}}))
		session := connectFake(t, server, pcmConfig())
		skipOpen(t, session)
		for _, want := range []messages.ErrorValue{
			{Code: live.CodeInvalidAudio, Message: "odd PCM", Param: param, EventID: "evt_audio_1"},
			{Message: "openai live error"},
		} {
			msg := next(t, session)
			value, ok := msg.Value.(*messages.ErrorValue)
			if !ok || !value.IsNonTerminal() || value.Code != want.Code || value.Message != want.Message || value.Param != want.Param || value.EventID != want.EventID || value.ErrorType != live.ErrorTypeInvalidRequest {
				t.Fatalf("ERROR = %+v, want non-terminal %+v", msg.Value, want)
			}
		}
	})
}

func TestSessionClosedReasonsMapToTerminalReasons(t *testing.T) {
	tests := []struct {
		reason      string
		openSegment bool
		terminal    messages.TerminalReason
		output      messages.TerminalOutputState
	}{
		{live.CloseReasonCloseRequested, false, messages.TerminalReasonSessionClose, messages.TerminalOutputNotApplicable},
		{live.CloseReasonExpired, true, messages.TerminalReasonProviderClose, messages.TerminalOutputNotApplicable},
		{live.CloseReasonContent, true, messages.TerminalReasonProviderClose, messages.TerminalOutputPartial},
		{live.CloseReasonContent, false, messages.TerminalReasonProviderClose, messages.TerminalOutputNotApplicable},
		{live.CloseReasonRemoteHangup, false, messages.TerminalReasonProviderClose, messages.TerminalOutputNotApplicable},
		{live.CloseReasonConnectionLost, true, messages.TerminalReasonTerminalFailure, messages.TerminalOutputPartial},
		{live.CloseReasonConnectionLost, false, messages.TerminalReasonTerminalFailure, messages.TerminalOutputNotApplicable},
		{"future_reason", false, messages.TerminalReasonProviderClose, messages.TerminalOutputNotApplicable},
	}
	for _, tt := range tests {
		name := tt.reason
		if tt.openSegment {
			name += "/open-segment"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				steps := []fakelive.Step{fakelive.AwaitStarted()}
				if tt.openSegment {
					steps = append(steps, fakelive.Send(audioDelta([]byte{1, 0})))
				}
				server := newFake(append(steps, fakelive.CloseSession(tt.reason))...)
				session := connectFake(t, server, pcmConfig())
				skipOpen(t, session)
				received := collectUntil(t, session, time.Now(), messages.StreamTypeSessionClose)
				if tt.openSegment && received[len(received)-2].msg.Type != messages.StreamTypeMessageEnd {
					t.Fatalf("messages = %v, want the open segment closed before SESSION.CLOSE", typesOf(received))
				}
				closed := sessionClose(t, received[len(received)-1].msg)
				if closed.TerminalReason != tt.terminal || closed.OutputState != tt.output || closed.Reason != tt.reason ||
					closed.TerminalProvenance != messages.TerminalProvenanceProvider || closed.SessionID != fakelive.DefaultSessionID {
					t.Fatalf("SESSION.CLOSE = %+v, want %s/%s with reason %q", closed, tt.terminal, tt.output, tt.reason)
				}
				<-session.Done()
				if err := as[messages.SessionTerminalError](t, session).TerminalError(); err != nil {
					t.Fatalf("TerminalError = %v, want none after session.closed", err)
				}
			})
		})
	}
}

func TestDroppedSocketReportsUnconfirmedFinalization(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newFake(fakelive.AwaitStarted(), fakelive.Send(audioDelta([]byte{1, 0}), inputText("hello", 0, 100)), fakelive.Drop())
		session := connectFake(t, server, pcmConfig())
		skipOpen(t, session)
		received := collectUntil(t, session, time.Now(), messages.StreamTypeSessionClose)
		types := typesOf(received)
		if !slices.Contains(types, messages.StreamTypeMessageEnd) || !slices.Contains(types, messages.StreamTypeTranscriptEnd) {
			t.Fatalf("messages = %v, want the open segment and utterance closed", types)
		}
		closed := sessionClose(t, received[len(received)-1].msg)
		if closed.TerminalReason != messages.TerminalReasonTerminalFailure || closed.Reason != "finalization_unconfirmed" || closed.OutputState != messages.TerminalOutputPartial {
			t.Fatalf("SESSION.CLOSE = %+v, want unconfirmed finalization", closed)
		}
		<-session.Done()
		if err := as[messages.SessionTerminalError](t, session).TerminalError(); err == nil {
			t.Fatal("TerminalError = nil, want the transport failure")
		}
	})
}

func TestCloseRunsTheSessionCloseHandshake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newFake()
		session := connectFake(t, server, pcmConfig())
		skipOpen(t, session)
		if err := session.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		events := server.ClientEvents()
		if _, ok := events[len(events)-1].(live.SessionClose); !ok {
			t.Fatalf("last client event = %#v, want session.close", events[len(events)-1])
		}
		closed := sessionClose(t, next(t, session))
		if closed.TerminalReason != messages.TerminalReasonSessionClose || closed.Reason != live.CloseReasonCloseRequested {
			t.Fatalf("SESSION.CLOSE = %+v, want the requested close", closed)
		}
		if err := session.Close(); err != nil {
			t.Fatalf("second Close: %v", err)
		}
	})
}

func TestCloseWithoutSessionClosedReportsUnconfirmedFinalization(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := fakelive.New(fakelive.WithAPIKey(sessionKey), fakelive.WithDropOnClose())
		session := connectFake(t, server, pcmConfig())
		skipOpen(t, session)
		if err := session.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if closed := sessionClose(t, next(t, session)); closed.Reason != "finalization_unconfirmed" {
			t.Fatalf("SESSION.CLOSE = %+v, want unconfirmed finalization", closed)
		}
	})
}

func TestCloseGivesUpAfterTheCloseTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		conn := newSilentConn()
		session, err := live.New(
			live.WithCredentialProvider(live.APIKeyCredentials(sessionKey)),
			live.WithWebSocketDialer(silentDialer{conn: conn}),
			live.WithCloseTimeout(time.Second),
		).ConnectSession(t.Context(), pcmConfig())
		if err != nil {
			t.Fatalf("ConnectSession: %v", err)
		}
		start := time.Now()
		if err := session.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if waited := time.Since(start); waited != time.Second {
			t.Fatalf("Close waited %v, want the 1s close timeout", waited)
		}
	})
}

func TestConnectSessionStopsWaitingWhenTheContextEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		conn := newSilentConn()
		conn.withholdStart = true
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		_, err := live.New(
			live.WithCredentialProvider(live.APIKeyCredentials(sessionKey)),
			live.WithWebSocketDialer(silentDialer{conn: conn}),
		).ConnectSession(ctx, pcmConfig())
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ConnectSession = %v, want the context deadline", err)
		}
	})
}

// transcripts is the transcript content of a received message run.
type transcripts struct {
	user, assistant       string
	userEnd, assistantEnd *messages.TranscriptEndValue
	added                 int
}

func transcriptsOf(t *testing.T, received []timed) transcripts {
	t.Helper()
	var got transcripts
	for _, r := range received {
		switch value := r.msg.Value.(type) {
		case *messages.InputItemAddedValue:
			got.added++
			if value.ItemID != firstUtt {
				t.Fatalf("INPUT_ITEM.ADDED = %q, want %s", value.ItemID, firstUtt)
			}
		case *messages.TranscriptDeltaValue:
			if r.msg.Role != messages.RoleUser {
				got.assistant += value.Text
				continue
			}
			got.user += value.Text
			if value.ItemID != firstUtt || r.msg.ResponseID != "" {
				t.Fatalf("user delta %+v carries response %q, want only the utterance id", value, r.msg.ResponseID)
			}
		case *messages.TranscriptEndValue:
			if r.msg.Role == messages.RoleUser {
				got.userEnd = value
			} else {
				got.assistantEnd = value
			}
		}
	}
	return got
}

func delegation(id string) live.DelegationCreated {
	return live.DelegationCreated{EventID: "evt_del", OffsetMS: 300, Delegation: live.DelegationInfo{ID: id, Type: "delegation", Target: live.DelegationClient}}
}

func TestContextEndRunsTheCloseHandshake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newFake()
		ctx, cancel := context.WithCancel(t.Context())
		session, err := live.New(
			live.WithCredentialProvider(live.APIKeyCredentials(sessionKey)),
			live.WithWebSocketDialer(server.Dialer()),
		).ConnectSession(ctx, pcmConfig())
		if err != nil {
			t.Fatalf("ConnectSession: %v", err)
		}
		cancel()
		<-session.Done()
		events := server.ClientEvents()
		if _, ok := events[len(events)-1].(live.SessionClose); !ok {
			t.Fatalf("last client event = %#v, want session.close after the context ended", events[len(events)-1])
		}
		if err := session.Close(); err != nil {
			t.Fatalf("Close after the handshake: %v", err)
		}
	})
}

func TestSegmentGapIsConfigurable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const gap = 200 * time.Millisecond
		server := newFake(fakelive.AwaitStarted(), fakelive.Send(outputText("Short.", 0, 100), inputText("ok", 100, 200)))
		session := connectFake(t, server, pcmConfig(), live.WithSegmentGap(gap), live.WithSegmentGap(0))
		skipOpen(t, session)
		start := time.Now()
		received := collectUntil(t, session, start, messages.StreamTypeMessageEnd)
		if end := received[len(received)-1]; end.at != gap {
			t.Fatalf("segment ended after %v, want the configured %v gap", end.at, gap)
		}
	})
}
