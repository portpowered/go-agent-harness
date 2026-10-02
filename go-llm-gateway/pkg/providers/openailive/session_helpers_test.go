package openailive_test

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	live "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/fakelive"
)

const (
	sessionKey = "sk-live-test"
	firstSeg   = "live_seg_1"
	secondSeg  = "live_seg_2"
	firstUtt   = "live_utt_1"
	completed  = "completed"
)

// as asserts session implements capability T.
func as[T any](t *testing.T, session messages.Session) T {
	t.Helper()
	capability, ok := session.(T)
	if !ok {
		var want T
		t.Fatalf("session %T does not implement %T", session, &want)
	}
	return capability
}

// connectFake connects a provider session to a fake server that requires the
// API-key bearer, and closes it when the test ends.
func connectFake(t *testing.T, server *fakelive.Server, cfg models.SessionConfig, options ...live.Option) messages.Session {
	t.Helper()
	options = append([]live.Option{
		live.WithCredentialProvider(live.APIKeyCredentials(sessionKey)),
		live.WithWebSocketDialer(server.Dialer()),
	}, options...)
	session, err := live.New(options...).ConnectSession(t.Context(), cfg)
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("close session: %v", err)
		}
	})
	return session
}

func newFake(steps ...fakelive.Step) *fakelive.Server {
	return fakelive.New(fakelive.WithAPIKey(sessionKey), fakelive.WithScript(steps...))
}

func pcmConfig() models.SessionConfig {
	return models.SessionConfig{Model: live.Model1, Instructions: "Be concise."}
}

// next reads one message. Inside a synctest bubble a session that never
// sends one is reported as a deadlock instead of hanging.
func next(t *testing.T, session messages.Session) messages.StreamMessage {
	t.Helper()
	msg, ok := <-session.Receive().Chan()
	if !ok {
		t.Fatal("receive buffer closed")
	}
	if msg.ResponsePurpose != "" {
		t.Fatalf("message %s carries response purpose %q; GPT-Live segments never answer a request", msg.Type, msg.ResponsePurpose)
	}
	return msg
}

// timed is one received message and the virtual time it arrived at.
type timed struct {
	msg messages.StreamMessage
	at  time.Duration
}

// collectUntil reads messages until stop matches one, recording arrival
// times relative to start.
func collectUntil(t *testing.T, session messages.Session, start time.Time, stop messages.StreamMessageType) []timed {
	t.Helper()
	var out []timed
	for {
		msg := next(t, session)
		out = append(out, timed{msg: msg, at: time.Since(start)})
		if msg.Type == stop {
			return out
		}
	}
}

// skipOpen consumes SESSION.OPEN and SESSION.CREATED.
func skipOpen(t *testing.T, session messages.Session) {
	t.Helper()
	for _, want := range []messages.StreamMessageType{messages.StreamTypeSessionOpen, messages.StreamTypeSessionCreated} {
		if got := next(t, session); got.Type != want {
			t.Fatalf("message = %s, want %s", got.Type, want)
		}
	}
}

func typesOf(received []timed) []messages.StreamMessageType {
	out := make([]messages.StreamMessageType, len(received))
	for i, r := range received {
		out[i] = r.msg.Type
	}
	return out
}

func audioDelta(audio []byte) live.OutputAudioDelta {
	return live.OutputAudioDelta{Delta: base64.StdEncoding.EncodeToString(audio)}
}

func outputText(text string, startMS, endMS int64) live.OutputTranscriptDelta {
	return live.OutputTranscriptDelta{EventID: "evt_out", Delta: text, StartMS: startMS, EndMS: endMS}
}

func inputText(text string, startMS, endMS int64) live.InputTranscriptDelta {
	return live.InputTranscriptDelta{EventID: "evt_in", Delta: text, StartMS: startMS, EndMS: endMS}
}

func sendAudio(t *testing.T, session messages.Session, audio []byte) {
	t.Helper()
	outcome := messages.SendSessionWithOutcome(t.Context(), session, messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue(audio)})
	if !outcome.OK() {
		t.Fatalf("send audio: %+v", outcome)
	}
}

func messageEnd(t *testing.T, msg messages.StreamMessage) *messages.MessageEndValue {
	t.Helper()
	value, ok := msg.Value.(*messages.MessageEndValue)
	if !ok {
		t.Fatalf("value = %T, want *MessageEndValue", msg.Value)
	}
	return value
}

func sessionClose(t *testing.T, msg messages.StreamMessage) *messages.SessionCloseValue {
	t.Helper()
	value, ok := msg.Value.(*messages.SessionCloseValue)
	if msg.Type != messages.StreamTypeSessionClose || !ok {
		t.Fatalf("message = %s %T, want SESSION.CLOSE", msg.Type, msg.Value)
	}
	return value
}
