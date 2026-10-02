package quicksilver_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive"
	qs "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/quicksilver"
)

func mustBuild(t *testing.T, cfg models.SessionConfig) qs.SessionConfig {
	t.Helper()
	session, err := qs.BuildSession(cfg)
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	return session
}

func requireJSON(t *testing.T, value any, want string) {
	t.Helper()
	got, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if !sameJSON(t, got, []byte(want)) {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

// Codex codex-api/src/endpoint/realtime_websocket/methods_frameless_bidi_tests.rs:23-46.
func TestBuildSessionMatchesCodexSessionWithoutHistory(t *testing.T) {
	session := mustBuild(t, models.SessionConfig{Model: "gpt-live", Instructions: "instructions", Voice: "marin"})
	requireJSON(t, session, `{"model":"gpt-live","instructions":"instructions",
		"audio":{"output":{"voice":"marin"}},"delegation":{"type":"client"}}`)
}

// Codex methods_frameless_bidi_tests.rs:48-102: user and developer history
// is input_text, assistant history is output_text.
func TestBuildSessionMatchesCodexRoleBearingHistory(t *testing.T) {
	session := mustBuild(t, models.SessionConfig{
		Model: "gpt-live", Instructions: "instructions", Voice: "marin",
		Config: json.RawMessage(`{"initial_items":[
			{"role":"developer","text":"Remember this."},
			{"role":"user","text":"What do you remember?"},
			{"role":"assistant","text":"I remember."}]}`),
	})
	requireJSON(t, session.InitialItems, `[
		{"type":"message","role":"developer","content":[{"type":"input_text","text":"Remember this."}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"What do you remember?"}]},
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"I remember."}]}]`)
}

// OpenClaw extensions/openai/realtime-quicksilver-wire.test.ts:101-110: the
// codex model defaults to cove and keeps its own voices.
func TestBuildSessionDefaultsTheCodexModelToCoveAndRejectsOtherVoices(t *testing.T) {
	session := mustBuild(t, models.SessionConfig{Model: qs.ModelCodex})
	if session.Audio.Output.Voice != qs.DefaultVoice {
		t.Fatalf("voice = %q, want %q", session.Audio.Output.Voice, qs.DefaultVoice)
	}
	if got := mustBuild(t, models.SessionConfig{Model: qs.ModelCodex, Voice: "spruce"}); got.Audio.Output.Voice != "spruce" {
		t.Fatalf("voice = %q, want spruce", got.Audio.Output.Voice)
	}
	if _, err := qs.BuildSession(models.SessionConfig{Model: qs.ModelCodex, Voice: "marin"}); !errors.Is(err, qs.ErrInvalidSessionConfig) {
		t.Fatalf("public voice on the codex model: err = %v", err)
	}
}

// OpenClaw realtime-quicksilver-gateway-bridge.test.ts:418: hosts that
// control input send ack_filler false at call creation.
func TestBuildSessionSendsAckFillerFalseOnlyWhenAsked(t *testing.T) {
	session := mustBuild(t, models.SessionConfig{Model: qs.ModelCodex, Config: json.RawMessage(`{"ack_filler":false}`)})
	encoded, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"delegation":{"type":"client","ack_filler":false}`) {
		t.Fatalf("session %s lacks ack_filler false", encoded)
	}
	if plain := mustBuild(t, models.SessionConfig{Model: qs.ModelCodex}); plain.Delegation.AckFiller != nil {
		t.Fatal("ack_filler sent without being asked for")
	}
}

func TestBuildSessionRejectsInvalidConfig(t *testing.T) {
	for name, cfg := range map[string]models.SessionConfig{
		"missing model":        {},
		"long instructions":    {Model: qs.ModelCodex, Instructions: strings.Repeat("x", openailive.MaxInstructionsBytes+1)},
		"unknown option":       {Model: qs.ModelCodex, Config: json.RawMessage(`{"store":true}`)},
		"ack filler true":      {Model: qs.ModelCodex, Config: json.RawMessage(`{"ack_filler":true}`)},
		"unknown role":         {Model: qs.ModelCodex, Config: json.RawMessage(`{"initial_items":[{"role":"system","text":"x"}]}`)},
		"empty history text":   {Model: qs.ModelCodex, Config: json.RawMessage(`{"initial_items":[{"role":"user","text":" "}]}`)},
		"too much history":     {Model: qs.ModelCodex, Config: json.RawMessage(`{"initial_items":[` + strings.Repeat(`{"role":"user","text":"x"},`, openailive.MaxInitialItems) + `{"role":"user","text":"x"}]}`)},
		"options not a object": {Model: qs.ModelCodex, Config: json.RawMessage(`[]`)},
	} {
		if _, err := qs.BuildSession(cfg); !errors.Is(err, qs.ErrInvalidSessionConfig) {
			t.Errorf("%s: err = %v, want ErrInvalidSessionConfig", name, err)
		}
	}
}

// OpenClaw realtime-quicksilver-wire.ts:161-167 and Codex
// methods_frameless_bidi.rs:36-50: session.update carries no model.
func TestSessionUpdateDropsTheModel(t *testing.T) {
	update := qs.NewSessionUpdate(mustBuild(t, models.SessionConfig{Model: qs.ModelCodex, Instructions: " Speak briefly. "}))
	encoded, err := qs.EncodeEvent(update)
	if err != nil {
		t.Fatal(err)
	}
	if !sameJSON(t, encoded, []byte(`{"type":"session.update","session":{"instructions":"Speak briefly.",
		"audio":{"output":{"voice":"cove"}},"delegation":{"type":"client"}}}`)) {
		t.Fatalf("session.update = %s", encoded)
	}
}

// Codex methods_frameless_bidi_tests.rs:10-20: long text splits within the
// 500-byte wire limit without breaking a UTF-8 character.
func TestContextAppendsSplitLongTextOnCharacterBoundaries(t *testing.T) {
	for _, body := range []string{strings.Repeat("a", 1201), strings.Repeat("🙂", 200)} {
		var joined strings.Builder
		events := qs.ContextAppends(body, qs.ChannelSpeakable, "delegation-1")
		for _, event := range events {
			appended, ok := event.(qs.DelegationContextAppend)
			if !ok || appended.DelegationItemID != "delegation-1" || appended.Channel != qs.ChannelSpeakable {
				t.Fatalf("event = %#v", event)
			}
			chunk := appended.Content[0].Text
			if len(chunk) > qs.ContextAppendMaxBytes || !utf8.ValidString(chunk) {
				t.Fatalf("chunk of %d bytes, valid UTF-8 %v", len(chunk), utf8.ValidString(chunk))
			}
			joined.WriteString(chunk)
		}
		if joined.String() != body || len(events) < 2 {
			t.Fatalf("%d chunks did not rebuild the text", len(events))
		}
	}
}

func TestContextAppendsWithoutDelegationAreSessionContext(t *testing.T) {
	events := qs.ContextAppends("Greet the user briefly.", qs.ChannelSpeakable, "")
	want := qs.SessionContextAppend{Channel: qs.ChannelSpeakable, Content: text("Greet the user briefly.")}
	if len(events) != 1 || !equalEvent(events[0], want) {
		t.Fatalf("events = %#v", events)
	}
}

func equalEvent(got, want qs.Event) bool {
	left, err := qs.EncodeEvent(got)
	if err != nil {
		return false
	}
	right, err := qs.EncodeEvent(want)
	return err == nil && string(left) == string(right)
}
