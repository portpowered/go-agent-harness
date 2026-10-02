package openailive_test

import (
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gorilla/websocket"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	live "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/fakelive"
)

func TestProviderIdentityAndOptions(t *testing.T) {
	provider := live.New(live.WithEndpoint(" "), live.WithLogger(nil), live.WithClock(nil), live.WithCloseTimeout(0))
	if provider.Name() != live.ProviderName || provider.Name() != "openai-live" {
		t.Fatalf("Name = %q", provider.Name())
	}
	if live.NewDefaultWebSocketDialer() == nil {
		t.Fatal("no default dialer")
	}
	startup := &live.StartupError{Code: live.CodeUnknownParameter, Param: "session.voice", Message: "Unknown parameter."}
	if got := startup.Error(); !strings.Contains(got, "unknown_parameter (session.voice)") || !strings.Contains(got, "Unknown parameter.") {
		t.Fatalf("StartupError = %q", got)
	}
	if got := (&live.StartupError{Code: "x", Message: "y"}).Error(); strings.Contains(got, "(") {
		t.Fatalf("StartupError without param = %q", got)
	}
	synctest.Test(t, func(t *testing.T) {
		const endpoint = "wss://example.openai.azure.com/openai/v1/live/sessions"
		server := newFake()
		connectFake(t, server, pcmConfig(), live.WithEndpoint(endpoint), live.WithLogger(logging.DummyLogger()), live.WithClock(clock.Real{}))
		if calls := server.DialCalls(); calls[0].Endpoint != endpoint {
			t.Fatalf("dialed %q, want the configured endpoint", calls[0].Endpoint)
		}
	})
}

func TestConnectSessionFailsWhenSessionStartCannotBeSent(t *testing.T) {
	writeFailure := errors.New("write refused")
	server := fakelive.New(fakelive.WithConnFaults(fakelive.ConnFaults{Write: writeFailure}))
	_, err := live.New(live.WithCredentialProvider(live.APIKeyCredentials("")), live.WithWebSocketDialer(server.Dialer())).ConnectSession(t.Context(), pcmConfig())
	if !errors.Is(err, writeFailure) {
		t.Fatalf("ConnectSession = %v, want the write failure", err)
	}
}

func TestConnectSessionRejectsAMalformedAcknowledgement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		conn := newSilentConn()
		conn.firstFrame = []byte(`{"type":"session.started","session":5}`)
		_, err := live.New(live.WithCredentialProvider(live.APIKeyCredentials(sessionKey)), live.WithWebSocketDialer(silentDialer{conn: conn})).ConnectSession(t.Context(), pcmConfig())
		if !errors.Is(err, live.ErrMalformedEvent) {
			t.Fatalf("ConnectSession = %v, want a malformed-event error", err)
		}
	})
}

// Server notices, malformed frames and empty fragments add nothing but the
// mapped messages; the session keeps running.
func TestServerNoticesAndBadFramesDoNotDisturbTheStream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newFake(fakelive.AwaitStarted(),
			fakelive.Send(live.SessionUpdated{Session: live.SessionResource{ID: fakelive.DefaultSessionID}}),
			fakelive.Send(live.UsageUpdated{Usage: live.Usage{Seconds: 1}}, live.Info{Code: "notice", Message: "hello"}, delegation("del_1")),
			fakelive.SendRaw(websocket.TextMessage, []byte(`{"type":"session.output_audio.delta","delta":5}`)),
			fakelive.Send(live.OutputAudioDelta{Delta: "!!"}, live.OutputAudioDelta{Delta: ""}),
			fakelive.Send(outputText("Done.", 0, 100)),
		)
		session := connectFake(t, server, pcmConfig())
		skipOpen(t, session)
		if updated := next(t, session); updated.Type != messages.StreamTypeSessionUpdated {
			t.Fatalf("message = %s, want SESSION.UPDATED", updated.Type)
		}
		received := collectUntil(t, session, time.Now(), messages.StreamTypeMessageEnd)
		if received[0].msg.Type != messages.StreamTypeMessageStart || received[0].msg.ResponseID != firstSeg {
			t.Fatalf("notices leaked into the stream: %v", typesOf(received))
		}
	})
}

// A socket that cannot be written leaves the end to the read side; Close then
// gives up after its timeout.
func TestWriteFailureAfterStartLeavesTheSessionToTheReadSide(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		conn := newSilentConn()
		conn.failWritesAfterStart = true
		session, err := live.New(
			live.WithCredentialProvider(live.APIKeyCredentials(sessionKey)),
			live.WithWebSocketDialer(silentDialer{conn: conn}),
			live.WithCloseTimeout(time.Second),
		).ConnectSession(t.Context(), pcmConfig())
		if err != nil {
			t.Fatalf("ConnectSession: %v", err)
		}
		sendAudio(t, session, []byte{1, 0})
		synctest.Wait()
		if err := session.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if outcome := messages.SendSessionWithOutcome(t.Context(), session, messages.StreamMessage{Type: messages.StreamTypeMessageEnd}); outcome.Status != messages.SessionSendClosed {
			t.Fatalf("send after close = %+v, want closed", outcome)
		}
	})
}

func TestRTCMediaWriteFailsOnAClosedSessionAndCancelStopsPlayback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newFake(fakelive.AwaitStarted(), fakelive.Send(audioDelta(make([]byte, 48000))))
		session := connectFake(t, server, pcmConfig())
		endpoints := as[sharedaudio.MediaSession](t, session).RTCMedia()
		if _, err := endpoints.Inbound.ReadFrame(t.Context()); err != nil {
			t.Fatalf("read frame: %v", err)
		}
		playback := as[messages.SessionLocalPlayback](t, session)
		if !playback.InterruptLocalPlayback(t.Context()) {
			t.Fatal("no audible playback to interrupt")
		}
		if !session.Send(t.Context(), messages.StreamMessage{Type: messages.StreamTypeResponseCancel, Value: messages.NewResponseCancelValue()}) {
			t.Fatal("cancel refused")
		}
		if err := session.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if err := endpoints.Outbound.WriteFrame(t.Context(), sharedaudio.PCMFrame{Samples: []int16{1}}); err == nil {
			t.Fatal("RTC frame accepted after close")
		}
	})
}
