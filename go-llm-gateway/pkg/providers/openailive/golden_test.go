package openailive_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	live "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive"
)

const (
	goldenDir            = "testdata/protocol"
	restaurantPrompt     = "Help the caller plan a restaurant reservation. Confirm details before booking."
	availabilityPrompt   = "Check restaurant availability. Ask before confirming a booking."
	sampleAudio          = "AACAAIAAAIAAAP9/AIAAgA=="
	azureAudio           = "<base64-encoded-24khz-pcm16le-mono-audio>"
	delegationABC        = "del_abc123"
	responsesDelegation  = "item_delegation_456"
	sessionABC           = "live_abc123"
	invalidRequestType   = "invalid_request_error"
	delegationMetadata   = "delegation"
	restaurantExpiration = 1788555600
)

func ptr[T any](value T) *T { return &value }

func pcm24() *live.AudioFormat { return &live.AudioFormat{Type: live.AudioTypePCM, Rate: live.RatePCM24k} }

func marin() *live.AudioOutput { return &live.AudioOutput{Voice: live.Voice{Name: "marin"}} }

func clientDelegation() *live.Delegation { return &live.Delegation{Type: live.DelegationClient} }

// restaurantSession is the resolved session the spec's examples share.
func restaurantSession(id string, delegation *live.Delegation) live.SessionResource {
	return live.SessionResource{
		ID: id, Status: live.SessionStatusActive, ExpiresAt: restaurantExpiration,
		SessionConfig: live.SessionConfig{
			Model:        live.Model1,
			Instructions: restaurantPrompt,
			Input:        []live.InitialItem{},
			Audio:        &live.SessionAudio{Format: pcm24(), Output: marin()},
			Delegation:   delegation,
		},
	}
}

// clientGoldens are the client events quoted in spec sections 1.6, 1.8 and
// 1.10, keyed by golden file name.
func clientGoldens() map[string]live.Event {
	return map[string]live.Event{
		"client.session_start": live.SessionStart{EventID: "evt_start_001", Session: live.SessionConfig{
			Model: live.Model1, Instructions: restaurantPrompt,
			Audio:      &live.SessionAudio{Format: pcm24(), Output: marin()},
			Delegation: clientDelegation(),
		}},
		"client.session_update": live.SessionUpdate{EventID: "evt_update_001", Session: live.SessionPatch{
			Delegation: &live.Delegation{Type: live.DelegationResponses, Responses: &live.ResponsesDelegationConfig{
				Instructions: ptr(availabilityPrompt), MaxOutputTokens: ptr(1024),
			}},
		}},
		"client.input_audio_append":   live.InputAudioAppend{Audio: sampleAudio},
		"client.input_audio_mute":     live.InputAudioMute{EventID: "evt_mute_001"},
		"client.instructions_append":  live.InstructionsAppend{EventID: "evt_instructions_001", Content: "The caller prefers outdoor seating."},
		"client.thinking_append":      live.ThinkingAppend{EventID: "evt_thinking_001", DelegationID: ptr(delegationABC), Content: "Checking availability for two guests at 7 PM."},
		"client.commentary_append":    live.CommentaryAppend{EventID: "evt_commentary_001", DelegationID: ptr(delegationABC), Content: "There is an outdoor table for two at 7 PM. Ask whether to reserve it."},
		"client.response_create":      live.ResponseCreate{EventID: "evt_response_001"},
		"client.session_close":        live.SessionClose{EventID: "evt_close_001"},
		"client.azure_session_close":  live.SessionClose{},
		"client.azure_input_audio_append": live.InputAudioAppend{Audio: azureAudio},
		"client.response_item_create_message": live.ResponseItemCreate{EventID: "evt_item_001", Item: json.RawMessage(
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"Please check for a table for two at 7 PM."}]}`)},
		"client.response_item_create_function_output": live.ResponseItemCreate{EventID: "event_function_output_1", Item: json.RawMessage(
			`{"type":"function_call_output","call_id":"call_123","output":"{\"temperature\":62,\"conditions\":\"rain\"}"}`)},
		"client.azure_session_start": live.SessionStart{Session: live.SessionConfig{
			Model: live.Model1, Instructions: "Be concise.", Delegation: clientDelegation(),
		}},
	}
}

// sessionServerGoldens are the session-lifecycle server events of spec
// sections 1.7 and 1.8.
func sessionServerGoldens() map[string]live.Event {
	responses := &live.Delegation{Type: live.DelegationResponses, Responses: &live.ResponsesDelegationConfig{
		Model: "gpt-6-astra", Instructions: ptr(availabilityPrompt), MaxOutputTokens: ptr(1024), Tools: []live.Tool{},
	}}
	return map[string]live.Event{
		"server.session_started": live.SessionStarted{EventID: "evt_started_001", ClientEventID: "evt_start_001",
			Session: restaurantSession(sessionABC, clientDelegation())},
		"server.session_updated": live.SessionUpdated{EventID: "evt_updated_001", ClientEventID: "evt_update_001",
			Session: restaurantSession("live_def456", responses)},
		"server.session_closed": live.SessionClosed{EventID: "evt_closed_001", ClientEventID: "evt_close_001",
			Reason: live.CloseReasonCloseRequested, Session: restaurantSession(sessionABC, clientDelegation()),
			Usage: live.Usage{Seconds: 45.8}},
		"server.azure_session_started": live.SessionStarted{Session: live.SessionResource{ID: "sess_123", SessionConfig: live.SessionConfig{
			Model: live.Model1, Instructions: "Be concise.",
			Audio: &live.SessionAudio{Output: marin()}, Delegation: clientDelegation(),
		}}},
		"server.input_audio_muted":   live.InputAudioMuted{EventID: "evt_muted_001", ClientEventID: "evt_mute_001"},
		"server.commentary_appended": live.CommentaryAppended{EventID: "evt_commentary_002", ClientEventID: "evt_commentary_001", StartMS: 5200, EndMS: 5400},
		"server.output_audio_delta_sideband": live.OutputAudioDelta{Delta: sampleAudio, StartMS: ptr[int64](1000), EndMS: ptr[int64](1200)},
		"server.azure_output_audio_delta":    live.OutputAudioDelta{Delta: azureAudio, StartMS: ptr[int64](0), EndMS: ptr[int64](100)},
		"server.input_transcript_delta": live.InputTranscriptDelta{EventID: "evt_input_transcript_001",
			Delta: "A table for two at seven, please.", StartMS: 1600, EndMS: 3400},
		"server.azure_input_transcript_delta": live.InputTranscriptDelta{Delta: "Hello", StartMS: 600, EndMS: 800},
		"server.output_transcript_delta": live.OutputTranscriptDelta{EventID: "evt_output_transcript_001",
			Delta: "Would you like me to reserve that table?", StartMS: 5400, EndMS: 7200},
		"server.usage_updated": live.UsageUpdated{EventID: "evt_usage_001", Usage: live.Usage{Seconds: 32.5},
			ContextWindow: &live.ContextWindow{UsageRatio: 0.12}},
		"server.info": live.Info{EventID: "evt_info_001", Code: "data_channel_permissions",
			Message: "The frontend data channel is configured with restricted event permissions."},
	}
}

// otherServerGoldens are the delegation, error and transport events of spec
// sections 1.7, 1.10 and 1.11.
func otherServerGoldens() map[string]live.Event {
	return map[string]live.Event{
		"server.delegation_created_client": live.DelegationCreated{EventID: "evt_delegation_001", OffsetMS: 3600,
			Delegation: live.DelegationInfo{ID: delegationABC, Type: delegationMetadata, Target: live.DelegationClient}},
		"server.delegation_created_responses": live.DelegationCreated{EventID: "event_delegation", OffsetMS: 1000,
			Delegation: live.DelegationInfo{ID: responsesDelegation, Type: delegationMetadata, Target: live.DelegationResponses, ResponseID: "resp_123"}},
		"server.response_event_text_delta": live.ResponseEvent{EventID: "evt_response_002", DelegationID: ptr("del_responses123"), Event: json.RawMessage(
			`{"type":"response.output_text.delta","item_id":"msg_abc123","output_index":0,"content_index":0,"delta":"An outdoor table is available at 7 PM.","sequence_number":3,"logprobs":[]}`)},
		"server.response_event_function_call": live.ResponseEvent{DelegationID: ptr(responsesDelegation), Event: json.RawMessage(
			`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_123","name":"get_weather","arguments":"{\"location\":\"Seattle\"}"}}`)},
		"server.error_unknown_parameter": live.ErrorEvent{EventID: "evt_error_001", Error: live.Error{
			Type: invalidRequestType, Code: live.CodeUnknownParameter, Message: "Unknown parameter: 'session.voice'.",
			Param: ptr("session.voice"), ClientEventID: "evt_invalid_001"}},
		"server.error_immutable_field_update": live.ErrorEvent{EventID: "event_error", Error: live.Error{
			Type: invalidRequestType, Code: live.CodeImmutableFieldUpdate, Message: "The delegation type cannot change after session startup.",
			Param: ptr("session.delegation.type"), ClientEventID: "event_update"}},
		"server.error_invalid_audio_azure": live.ErrorEvent{Error: live.Error{
			Type: invalidRequestType, Code: live.CodeInvalidAudio, Message: "PCM16 audio must contain an even number of bytes",
			Param: ptr("audio"), ClientEventID: "event_audio_1"}},
		"server.transport_failed": live.TransportFailed{EventID: "event_call_4", SessionID: "live_u0_123", Error: live.Error{
			Type: live.ErrorTypeCall, Code: "provider_invite_failed", Message: "provider rejected the call", Param: ptr("")}},
		"server.dtmf_received": live.DTMFReceived{EventID: "event_dtmf_1", Key: "5"},
	}
}

func allGoldens() map[string]live.Event {
	all := clientGoldens()
	for _, group := range []map[string]live.Event{sessionServerGoldens(), otherServerGoldens()} {
		for name, event := range group {
			all[name] = event
		}
	}
	return all
}

// TestSpecExamplesDecodeToTypedEventsAndReEncodeEqual decodes every literal
// JSON example of the spec, checks the typed value, re-encodes it and checks
// the result is the same JSON.
func TestSpecExamplesDecodeToTypedEventsAndReEncodeEqual(t *testing.T) {
	want := allGoldens()
	files, err := filepath.Glob(filepath.Join(goldenDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".json")
		seen[name] = true
		t.Run(name, func(t *testing.T) {
			expected, ok := want[name]
			if !ok {
				t.Fatalf("golden %s has no expected typed value", name)
			}
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			checkGolden(t, name, raw, expected)
		})
	}
	missing := []string{}
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

func checkGolden(t *testing.T, name string, raw []byte, expected live.Event) {
	t.Helper()
	decode := live.DecodeServerEvent
	if strings.HasPrefix(name, "client.") {
		decode = live.DecodeClientEvent
	}
	got, err := decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.EventType() != expected.EventType() {
		t.Fatalf("decoded type %q, want %q", got.EventType(), expected.EventType())
	}
	if normalized := compactRaw(t, got); !reflect.DeepEqual(normalized, expected) {
		t.Fatalf("decoded\n%#v\nwant\n%#v", normalized, expected)
	}
	encoded, err := live.EncodeEvent(got)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !sameJSON(t, encoded, raw) {
		t.Fatalf("re-encoded\n%s\nwant the same JSON as\n%s", encoded, raw)
	}
}

// compactRaw removes insignificant whitespace from the raw nested JSON that
// some events carry, so they compare with compact expectations.
func compactRaw(t *testing.T, event live.Event) live.Event {
	t.Helper()
	switch typed := event.(type) {
	case live.ResponseItemCreate:
		typed.Item = compactJSON(t, typed.Item)
		return typed
	case live.ResponseEvent:
		typed.Event = compactJSON(t, typed.Event)
		return typed
	}
	return event
}

func compactJSON(t *testing.T, raw json.RawMessage) json.RawMessage {
	t.Helper()
	var out bytes.Buffer
	if err := json.Compact(&out, raw); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
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
