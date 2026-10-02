package quicksilver_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	qs "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/quicksilver"
)

func decodeGolden(t *testing.T, name string) qs.Event {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(goldenDir, name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	event, err := qs.DecodeServerEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

// OpenClaw realtime-quicksilver-delegation.test.ts:295-298: an unmodelled
// type is reported, not rejected, and passes through unchanged.
func TestUnknownEventTypesDecodeToUnknownEventAndReEncodeUnchanged(t *testing.T) {
	frame := []byte(`{"type":"future.event","detail":{"x":1}}`)
	for _, decode := range []func([]byte) (qs.Event, error){qs.DecodeServerEvent, qs.DecodeClientEvent} {
		event, err := decode(frame)
		if err != nil {
			t.Fatal(err)
		}
		unknown, ok := event.(qs.UnknownEvent)
		if !ok || unknown.Type != "future.event" {
			t.Fatalf("event = %#v", event)
		}
		encoded, err := qs.EncodeEvent(unknown)
		if err != nil || string(encoded) != string(frame) {
			t.Fatalf("re-encoded %s, %v", encoded, err)
		}
	}
	// A server type is unknown to the client decoder and the reverse.
	if event, err := qs.DecodeClientEvent([]byte(`{"type":"turn.done"}`)); err != nil || event.EventType() != "turn.done" {
		t.Fatalf("server type through the client decoder = %#v, %v", event, err)
	}
	if _, ok := mustDecodeServer(t, `{"type":"session.close"}`).(qs.UnknownEvent); !ok {
		t.Fatal("client type through the server decoder was modelled")
	}
}

func errorGolden(t *testing.T, name string) qs.ErrorEvent {
	t.Helper()
	event, ok := decodeGolden(t, name).(qs.ErrorEvent)
	if !ok {
		t.Fatalf("%s is not an error event", name)
	}
	return event
}

func mustDecodeServer(t *testing.T, frame string) qs.Event {
	t.Helper()
	event, err := qs.DecodeServerEvent([]byte(frame))
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func TestMalformedFramesAreRejected(t *testing.T) {
	for _, frame := range []string{
		`not json`,
		`{"audio":"AAE="}`,
		`{"type":""}`,
		`{"type":7}`,
		`{"type":"turn.done","turn":"hello"}`,
		`{"type":"delegation.created","item":[]}`,
	} {
		if _, err := qs.DecodeServerEvent([]byte(frame)); !errors.Is(err, qs.ErrMalformedEvent) {
			t.Errorf("%s: err = %v, want ErrMalformedEvent", frame, err)
		}
	}
	if _, err := qs.DecodeClientEvent([]byte(`{"type":"session.context.append","content":"x"}`)); !errors.Is(err, qs.ErrMalformedEvent) {
		t.Fatalf("client err = %v", err)
	}
}

// Codex protocol_frameless_bidi.rs:72-90 and OpenClaw
// realtime-quicksilver-events.ts:83-89 join only the input_text parts and
// accept only client-targeted delegations.
func TestDelegationPromptJoinsInputTextAndOnlyClientTargetsAreWork(t *testing.T) {
	mixed, ok := decodeGolden(t, "server.delegation_created_mixed_parts").(qs.DelegationCreated)
	if !ok || !mixed.IsClient() || mixed.Prompt() != "curl https://example.com" {
		t.Fatalf("mixed delegation = %#v, prompt %q", mixed, mixed.Prompt())
	}
	server, ok := decodeGolden(t, "server.delegation_created_server_target").(qs.DelegationCreated)
	if !ok || server.IsClient() || server.Prompt() != "" {
		t.Fatalf("server-target delegation = %#v", server)
	}
}

// Codex protocol_common.rs:63-83 reads the top-level message, then the
// nested one; OpenClaw realtime-quicksilver-events.ts:140-156 treats the
// listed nested codes as credential failures.
func TestErrorTextAndCredentialFailure(t *testing.T) {
	cases := []struct {
		event    qs.ErrorEvent
		text     string
		authFail bool
	}{
		{qs.ErrorEvent{Message: "top", Error: &qs.ErrorDetail{Message: "nested"}}, "top", false},
		{errorGolden(t, "server.error_message"), "call failed", false},
		{errorGolden(t, "server.error_invalid_token"), "invalid_token", true},
		{errorGolden(t, "server.error_missing_scope"), "temporary provider rejection", false},
		{qs.ErrorEvent{Error: &qs.ErrorDetail{Code: "Token_Expired"}}, "Token_Expired", true},
		{qs.ErrorEvent{}, "", false},
	}
	for _, c := range cases {
		if c.event.Text() != c.text || c.event.AuthFailure() != c.authFail {
			t.Errorf("%#v: text %q auth %v, want %q %v", c.event, c.event.Text(), c.event.AuthFailure(), c.text, c.authFail)
		}
	}
}

func TestAudioPayloadsRoundTripAndOddPCMIsRefused(t *testing.T) {
	appended, err := qs.NewInputAudioAppend([]byte{0, 1})
	if err != nil || appended.Audio != "AAE=" {
		t.Fatalf("append = %#v, %v", appended, err)
	}
	if _, err := qs.NewInputAudioAppend([]byte{0}); !errors.Is(err, qs.ErrOddPCMLength) {
		t.Fatalf("odd PCM err = %v", err)
	}
	delta, ok := decodeGolden(t, "server.output_audio_delta").(qs.OutputAudioDelta)
	if !ok {
		t.Fatal("not an output delta")
	}
	if audio, err := delta.Bytes(); err != nil || len(audio) != 2 || audio[1] != 1 {
		t.Fatalf("audio = %v, %v", audio, err)
	}
	if _, err := (qs.OutputAudioDelta{Audio: "%%"}).Bytes(); err == nil {
		t.Fatal("bad base64 decoded")
	}
}
