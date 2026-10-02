package quicksilver_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	qs "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/quicksilver"
)

// Every golden under testdata/protocol is a literal frame copied from a
// reference source. Citations name the file and line of the literal:
//
//   - Codex: openai/codex revision 1e6185e522, paths under codex-rs/.
//   - OpenClaw: openclaw/openclaw revision 87af5763, paths under
//     extensions/openai/.
const goldenDir = "testdata/protocol"

func ptr[T any](value T) *T { return &value }

func text(value string) []qs.ContentPart {
	return []qs.ContentPart{{Type: qs.PartInputText, Text: value}}
}

func marinSession(instructions string) qs.SessionConfig {
	return qs.SessionConfig{
		Instructions: instructions,
		Audio:        qs.SessionAudio{Output: qs.AudioOutput{Voice: "marin"}},
		Delegation:   qs.Delegation{Type: qs.DelegationClient},
	}
}

// clientGoldens are the client frames, keyed by golden file name.
func clientGoldens() map[string]qs.Event {
	withHistory := marinSession("Speak briefly.")
	withHistory.InitialItems = []qs.InitialItem{
		{Type: qs.ItemTypeMessage, Role: qs.RoleUser, Content: text("Question")},
		{Type: qs.ItemTypeMessage, Role: qs.RoleAssistant, Content: []qs.ContentPart{{Type: qs.PartOutputText, Text: "Answer"}}},
	}
	return map[string]qs.Event{
		// OpenClaw realtime-quicksilver-wire.test.ts:79-93.
		"client.session_update_initial_items": qs.SessionUpdate{Session: withHistory},
		// OpenClaw realtime-quicksilver-bridge.test.ts:34-40.
		"client.session_update_direct": qs.SessionUpdate{Session: marinSession("Use delegation for real work.")},
		// OpenClaw realtime-quicksilver-delegation.test.ts:194-198.
		"client.session_context_append_speakable": qs.SessionContextAppend{Channel: qs.ChannelSpeakable, Content: text("Ready")},
		// OpenClaw realtime-quicksilver-bridge.test.ts:939-942.
		"client.session_context_append_plain": qs.SessionContextAppend{Content: text("Legacy speech request")},
		// OpenClaw realtime-quicksilver-bridge.test.ts:750-755.
		"client.delegation_context_append_speakable": qs.DelegationContextAppend{
			DelegationItemID: "delegation-1", Channel: qs.ChannelSpeakable, Content: text("The repository is clean."),
		},
		// Codex core/tests/suite/realtime_conversation.rs:3841-3846, with
		// commentary_text from lines 3691-3695.
		"client.delegation_context_append_commentary": qs.DelegationContextAppend{
			DelegationItemID: "delegation_stream", Channel: qs.ChannelCommentary,
			Content: text("[PROGRESS]seed first " + strings.Repeat("x", 94)),
		},
		// Codex core/tests/suite/realtime_conversation.rs:3858-3866, with
		// final_text from lines 3700-3702.
		"client.delegation_context_append_final": qs.DelegationContextAppend{
			DelegationItemID: "delegation_stream", Channel: qs.ChannelSpeakable, Content: text("[DONE]done"),
		},
		// Codex codex-api/src/endpoint/realtime_websocket/protocol.rs:75-76
		// (unit variant) and methods.rs:415-417; OpenClaw
		// realtime-quicksilver-protocol.ts:155.
		"client.session_close": qs.SessionClose{},
		// Shape from Codex realtime_websocket/protocol.rs:60-61 and OpenClaw
		// realtime-quicksilver-protocol.ts:12-17; the audio value is the
		// literal of Codex protocol_frameless_bidi_tests.rs:49.
		"client.input_audio_append": qs.InputAudioAppend{Audio: "AAE="},
	}
}

// serverGoldens are the server frames, keyed by golden file name.
func serverGoldens() map[string]qs.Event {
	return map[string]qs.Event{
		// Codex core/tests/suite/realtime_conversation.rs:607-610.
		"server.session_started_codex": qs.SessionStarted{Session: &qs.SessionResource{ID: "rtc_core_test", Instructions: "backend prompt"}},
		// Codex core/tests/suite/realtime_conversation.rs:729-732.
		"server.session_updated_codex": qs.SessionUpdated{Session: &qs.SessionResource{ID: "sess_webrtc", Instructions: "backend prompt"}},
		// OpenClaw realtime-quicksilver-delegation.test.ts:218.
		"server.session_started_expiry": qs.SessionStarted{Session: &qs.SessionResource{ExpiresAt: 123}},
		// OpenClaw realtime-quicksilver-bridge.test.ts:189.
		"server.session_started_empty": qs.SessionStarted{Session: &qs.SessionResource{}},
		// OpenClaw realtime-quicksilver-delegation.test.ts:243.
		"server.session_updated_bare": qs.SessionUpdated{},
		// Codex codex-api/src/endpoint/realtime_websocket/protocol_frameless_bidi_tests.rs:47-52.
		"server.output_audio_delta": qs.OutputAudioDelta{Audio: "AAE=", StartMS: ptr[int64](0), EndMS: ptr[int64](100)},
		// Codex protocol_frameless_bidi_tests.rs:39-42.
		"server.input_transcript_added": qs.InputTranscriptAdded{Item: qs.TranscriptItem{ID: "input-1", Type: "input_transcript", Text: "hello"}},
		// OpenClaw realtime-quicksilver-delegation.test.ts:228.
		"server.output_transcript_added": qs.OutputTranscriptAdded{Item: qs.TranscriptItem{Text: "wor"}},
		// Codex protocol_frameless_bidi_tests.rs:43-46.
		"server.turn_done_user": qs.TurnDone{Turn: qs.Turn{ID: "turn-1", Role: qs.RoleUser, Transcript: "hello"}},
		// Codex protocol_frameless_bidi_tests.rs:21-30.
		"server.delegation_created_offset": qs.DelegationCreated{OffsetMS: ptr[int64](1000), Item: qs.DelegationItem{
			ID: "handoff-123", Type: qs.ItemTypeDelegation, Target: qs.TargetClient, Content: text("check the weather"),
		}},
		// Codex core/tests/suite/realtime_conversation.rs:3752-3759.
		"server.delegation_created_stream": qs.DelegationCreated{Item: qs.DelegationItem{
			ID: "delegation_stream", Type: qs.ItemTypeDelegation, Target: qs.TargetClient, Content: text("delegate streaming"),
		}},
		// OpenClaw realtime-quicksilver-delegation.test.ts:259-271.
		"server.delegation_created_mixed_parts": qs.DelegationCreated{Item: qs.DelegationItem{
			ID: "delegation-1", Type: qs.ItemTypeDelegation, Target: qs.TargetClient, Content: []qs.ContentPart{
				{Type: qs.PartInputText, Text: "curl https://exa"},
				{Type: qs.PartOutputText, Text: "ignored"},
				{Type: qs.PartInputText, Text: "mple.com"},
			},
		}},
		// OpenClaw realtime-quicksilver-delegation.test.ts:277.
		"server.delegation_created_server_target": qs.DelegationCreated{Item: qs.DelegationItem{
			ID: "delegation-2", Type: qs.ItemTypeDelegation, Target: "server", Content: []qs.ContentPart{},
		}},
		// OpenClaw realtime-quicksilver-delegation.test.ts:245.
		"server.output_audio_buffer_cleared": qs.OutputAudioBufferCleared{},
		// OpenClaw realtime-quicksilver-delegation.test.ts:286.
		"server.error_message": qs.ErrorEvent{Error: &qs.ErrorDetail{Message: "call failed"}},
		// OpenClaw realtime-quicksilver-delegation.test.ts:291.
		"server.error_invalid_token": qs.ErrorEvent{Error: &qs.ErrorDetail{Code: "invalid_token"}},
		// OpenClaw realtime-quicksilver-bridge.test.ts:486-489.
		"server.error_missing_scope": qs.ErrorEvent{Error: &qs.ErrorDetail{Code: "missing_scope", Message: "temporary provider rejection"}},
	}
}

func TestReferenceFramesDecodeToTypedEventsAndReEncodeEqual(t *testing.T) {
	want := clientGoldens()
	for name, event := range serverGoldens() {
		want[name] = event
	}
	files, err := filepath.Glob(filepath.Join(goldenDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".json")
		expected, ok := want[name]
		if !ok {
			t.Fatalf("golden %s has no expected event", name)
		}
		seen[name] = true
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(name, func(t *testing.T) { checkGolden(t, name, raw, expected) })
	}
	var missing []string
	for name := range want {
		if !seen[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("expected goldens without a testdata file: %v", missing)
	}
}

func checkGolden(t *testing.T, name string, raw []byte, expected qs.Event) {
	t.Helper()
	decode := qs.DecodeServerEvent
	if strings.HasPrefix(name, "client.") {
		decode = qs.DecodeClientEvent
	}
	got, err := decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("decoded\n%#v\nwant\n%#v", got, expected)
	}
	encoded, err := qs.EncodeEvent(got)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !sameJSON(t, encoded, raw) {
		t.Fatalf("re-encoded\n%s\nwant the same JSON as\n%s", encoded, raw)
	}
}

func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var left, right any
	if err := json.Unmarshal(a, &left); err != nil {
		t.Fatalf("unmarshal %s: %v", a, err)
	}
	if err := json.Unmarshal(b, &right); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
	return reflect.DeepEqual(left, right)
}
