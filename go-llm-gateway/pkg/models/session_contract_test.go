package models

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

var updateModelsGolden = flag.Bool("update", false, "update the gateway model JSON golden file")

//go:embed testdata/session_models.golden
var sessionModelsGolden []byte

type modelJSONCase struct {
	name    string
	value   any
	decode  func([]byte) (any, error)
	compare func(*testing.T, any, any)
}

var wantModelCaseNames = []string{
	"AudioFormat",
	"SampleRate",
	"SessionModality",
	"TurnDetectionConfig",
	"SessionConfig",
	"SessionEventType",
	"SessionEvent",
	"Role",
	"ToolCall",
	"ContentPart",
	"TextPart",
	"ImagePart",
	"AudioPart",
	"VideoPart",
	"EmbeddingPart",
	"Message",
	"ToolDefinition",
	"ToolParameter",
	"TokenUsage",
}

func decodeModelAs[T any](data []byte) (any, error) {
	var got T
	if err := json.Unmarshal(data, &got); err != nil {
		return nil, err
	}
	return got, nil
}

func compareModelValues(t *testing.T, want, got any) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round-trip value mismatch:\nwant: %#v\n got: %#v", want, got)
	}
}

func compareModelJSONRepresentation(t *testing.T, want, got any) {
	t.Helper()
	// ContentPart is an intentionally open interface. Its public JSON shape has
	// no discriminator, so encoding/json cannot reconstruct its concrete type;
	// compare the exact representable JSON object instead.
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal expected JSON representation: %v", err)
	}
	var wantValue any
	if err := json.Unmarshal(wantJSON, &wantValue); err != nil {
		t.Fatalf("decode expected JSON representation: %v", err)
	}
	if !reflect.DeepEqual(wantValue, got) {
		t.Fatalf("JSON representation mismatch:\nwant: %#v\n got: %#v", wantValue, got)
	}
}

func populatedTurnDetectionConfig() TurnDetectionConfig {
	createResponse := true
	return TurnDetectionConfig{
		Type:              "server_vad",
		Threshold:         0.72,
		PrefixPaddingMs:   240,
		SilenceDurationMs: 680,
		CreateResponse:    &createResponse,
	}
}

func populatedToolDefinition() ToolDefinition {
	return ToolDefinition{
		Name:        "lookup_weather",
		Description: "Look up the weather for a city.",
		Parameters: []ToolParameter{
			{
				Name:        "city",
				Type:        "string",
				Description: "City name.",
				Required:    true,
			},
		},
	}
}

func populatedSessionConfig() SessionConfig {
	return SessionConfig{
		Model:                 "grok-realtime-contract",
		Modalities:            []SessionModality{SessionModalityText, SessionModalityAudio},
		Voice:                 "Rex",
		Instructions:          "Answer concisely and announce tool calls.",
		InputAudioFormat:      AudioFormatPCM16,
		OutputAudioFormat:     AudioFormatG711Ulaw,
		InputAudioSampleRate:  SampleRate24000,
		OutputAudioSampleRate: SampleRate16000,
		Tools:                 []ToolDefinition{populatedToolDefinition()},
		TurnDetection:         func() *TurnDetectionConfig { v := populatedTurnDetectionConfig(); return &v }(),
		InputAudioTranscription: func() *InputAudioTranscriptionConfig {
			v := InputAudioTranscriptionConfig{Enabled: true, Model: DefaultInputAudioTranscriptionModel}
			return &v
		}(),
		Config: json.RawMessage(`{"temperature":0.65,"provider":"xai"}`),
	}
}

func populatedMessage() Message {
	index := 3
	return Message{
		Role:               RoleAssistant,
		Refusal:            "",
		ToolCalls:          []ToolCall{{ID: "call_weather_1", Name: "lookup_weather", Arguments: `{"city":"Seattle"}`}},
		ToolCallID:         "call_weather_1",
		Name:               "weather-agent",
		Index:              &index,
		GlobalIndex:        17,
		ActorProvidedID:    "provider-msg-17",
		ActorProvidedIndex: 4,
		ActorStreamID:      "stream-abc",
		ActorID:            "model",
	}
}

func populatedSessionEvent() SessionEvent {
	return SessionEvent{
		Type: SessionEventSessionCreated,
		Data: json.RawMessage(`{"session_id":"sess-contract-1","model":"grok-realtime-contract"}`),
	}
}

func exportedModelCases() []modelJSONCase {
	return []modelJSONCase{
		{name: "AudioFormat", value: AudioFormatG711Ulaw, decode: decodeModelAs[AudioFormat], compare: compareModelValues},
		{name: "SampleRate", value: SampleRate24000, decode: decodeModelAs[SampleRate], compare: compareModelValues},
		{name: "SessionModality", value: SessionModalityAudio, decode: decodeModelAs[SessionModality], compare: compareModelValues},
		{name: "TurnDetectionConfig", value: populatedTurnDetectionConfig(), decode: decodeModelAs[TurnDetectionConfig], compare: compareModelValues},
		{name: "SessionConfig", value: populatedSessionConfig(), decode: decodeModelAs[SessionConfig], compare: compareModelValues},
		{name: "SessionEventType", value: SessionEventResponseDone, decode: decodeModelAs[SessionEventType], compare: compareModelValues},
		{name: "SessionEvent", value: populatedSessionEvent(), decode: decodeModelAs[SessionEvent], compare: compareModelValues},
		{name: "Role", value: RoleAssistant, decode: decodeModelAs[Role], compare: compareModelValues},
		{name: "ToolCall", value: ToolCall{ID: "call_contract", Name: "lookup_weather", Arguments: `{"city":"Seattle"}`}, decode: decodeModelAs[ToolCall], compare: compareModelValues},
		{name: "ContentPart", value: ContentPart(TextPart{Text: "contract content"}), decode: decodeModelAs[any], compare: compareModelJSONRepresentation},
		{name: "TextPart", value: TextPart{Text: "contract text"}, decode: decodeModelAs[TextPart], compare: compareModelValues},
		{name: "ImagePart", value: ImagePart{URL: "https://example.com/contract.png", Bytes: []byte("png-bytes"), MediaType: "image/png"}, decode: decodeModelAs[ImagePart], compare: compareModelValues},
		{name: "AudioPart", value: AudioPart{URL: "https://example.com/contract.wav", Bytes: []byte("wav-bytes"), MediaType: "audio/wav"}, decode: decodeModelAs[AudioPart], compare: compareModelValues},
		{name: "VideoPart", value: VideoPart{URL: "https://example.com/contract.mp4", Bytes: []byte("mp4-bytes"), MediaType: "video/mp4"}, decode: decodeModelAs[VideoPart], compare: compareModelValues},
		{name: "EmbeddingPart", value: EmbeddingPart{URL: "https://example.com/contract.safetensors", Bytes: []byte("vector-bytes"), MediaType: "application/vnd.safetensors"}, decode: decodeModelAs[EmbeddingPart], compare: compareModelValues},
		{name: "Message", value: populatedMessage(), decode: decodeModelAs[Message], compare: compareModelValues},
		{name: "ToolDefinition", value: populatedToolDefinition(), decode: decodeModelAs[ToolDefinition], compare: compareModelValues},
		{name: "ToolParameter", value: ToolParameter{Name: "limit", Type: "number", Description: "Maximum results.", Required: false}, decode: decodeModelAs[ToolParameter], compare: compareModelValues},
		{name: "TokenUsage", value: TokenUsage{PromptTokens: 21, CompletionTokens: 34, TotalTokens: 55, ReasoningTokens: 8}, decode: decodeModelAs[TokenUsage], compare: compareModelValues},
	}
}

func assertModelCaseRegistry(t *testing.T, cases []modelJSONCase) {
	t.Helper()
	if len(cases) != len(wantModelCaseNames) {
		t.Fatalf("expected %d exported model cases, got %d", len(wantModelCaseNames), len(cases))
	}
	for i, wantName := range wantModelCaseNames {
		if cases[i].name != wantName {
			t.Fatalf("model case %d: want %q, got %q", i, wantName, cases[i].name)
		}
		if cases[i].value == nil || reflect.ValueOf(cases[i].value).IsZero() {
			t.Fatalf("model case %q must use a non-zero populated value", cases[i].name)
		}
	}
}

func TestModels_S11JSONConformance(t *testing.T) {
	cases := exportedModelCases()
	assertModelCaseRegistry(t, cases)

	for _, modelCase := range cases {
		modelCase := modelCase
		t.Run(modelCase.name, func(t *testing.T) {
			encoded, err := json.Marshal(modelCase.value)
			if err != nil {
				t.Fatalf("marshal populated value: %v", err)
			}
			if len(encoded) == 0 || bytes.Equal(encoded, []byte("null")) {
				t.Fatalf("populated value serialized to empty JSON: %s", encoded)
			}

			decoded, err := modelCase.decode(encoded)
			if err != nil {
				t.Fatalf("unmarshal serialized value %s: %v", encoded, err)
			}
			modelCase.compare(t, modelCase.value, decoded)
		})
	}
}

func TestModels_S3GoldenJSON(t *testing.T) {
	cases := exportedModelCases()
	assertModelCaseRegistry(t, cases)

	var got strings.Builder
	for _, modelCase := range cases {
		encoded, err := json.Marshal(modelCase.value)
		if err != nil {
			t.Fatalf("marshal %s: %v", modelCase.name, err)
		}
		fmt.Fprintf(&got, "%s = %s\n", modelCase.name, encoded)
	}
	actual := []byte(got.String())

	if *updateModelsGolden {
		_, sourceFile, _, ok := runtime.Caller(0)
		if !ok {
			t.Fatal("locate model golden source file")
		}
		goldenPath := filepath.Join(filepath.Dir(sourceFile), "testdata", "session_models.golden")
		if err := os.WriteFile(goldenPath, actual, 0o644); err != nil {
			t.Fatalf("write model golden %s: %v", goldenPath, err)
		}
		t.Logf("updated %s", goldenPath)
		return
	}

	if !bytes.Equal(actual, sessionModelsGolden) {
		t.Fatalf("model golden mismatch; run `go test ./pkg/models -run TestModels_S3GoldenJSON -update` to regenerate")
	}
}

func TestModels_S11JSONTags(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{name: "TurnDetectionConfig", value: populatedTurnDetectionConfig()},
		{name: "SessionConfig", value: populatedSessionConfig()},
		{name: "SessionEvent", value: populatedSessionEvent()},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			encoded, err := json.Marshal(testCase.value)
			if err != nil {
				t.Fatalf("marshal %s: %v", testCase.name, err)
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &object); err != nil {
				t.Fatalf("decode %s as object: %v", testCase.name, err)
			}

			typ := reflect.TypeOf(testCase.value)
			value := reflect.ValueOf(testCase.value)
			for fieldIndex := 0; fieldIndex < typ.NumField(); fieldIndex++ {
				field := typ.Field(fieldIndex)
				tag := field.Tag.Get("json")
				if tag == "" || tag == "-" {
					continue
				}
				key := strings.Split(tag, ",")[0]
				if key == "" {
					key = field.Name
				}
				got, ok := object[key]
				if !ok {
					t.Errorf("field %s must be present under declared JSON key %q in %s", field.Name, key, encoded)
					continue
				}
				want, err := json.Marshal(value.Field(fieldIndex).Interface())
				if err != nil {
					t.Fatalf("marshal field %s: %v", field.Name, err)
				}
				if !bytes.Equal(got, want) {
					t.Errorf("field %s under JSON key %q: want %s, got %s", field.Name, key, want, got)
				}
			}
		})
	}
}

func TestModels_S11UnknownProviderFields(t *testing.T) {
	cases := []struct {
		name   string
		value  any
		decode func([]byte) (any, error)
	}{
		{name: "TurnDetectionConfig", value: populatedTurnDetectionConfig(), decode: decodeModelAs[TurnDetectionConfig]},
		{name: "SessionConfig", value: populatedSessionConfig(), decode: decodeModelAs[SessionConfig]},
		{name: "SessionEvent", value: populatedSessionEvent(), decode: decodeModelAs[SessionEvent]},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			encoded, err := json.Marshal(testCase.value)
			if err != nil {
				t.Fatalf("marshal known value: %v", err)
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &object); err != nil {
				t.Fatalf("decode known value: %v", err)
			}
			object["provider_added_sentinel"] = json.RawMessage(`"ignored-by-model"`)
			withUnknown, err := json.Marshal(object)
			if err != nil {
				t.Fatalf("marshal provider-extended value: %v", err)
			}

			decoded, err := testCase.decode(withUnknown)
			if err != nil {
				t.Fatalf("provider-added unknown field must be ignored: %v", err)
			}
			if !reflect.DeepEqual(testCase.value, decoded) {
				t.Fatalf("known fields changed after unknown-field decode:\nwant: %#v\n got: %#v", testCase.value, decoded)
			}
			reencoded, err := json.Marshal(decoded)
			if err != nil {
				t.Fatalf("marshal decoded value: %v", err)
			}
			if bytes.Contains(reencoded, []byte("provider_added_sentinel")) {
				t.Fatalf("provider-added field leaked into model representation: %s", reencoded)
			}
		})
	}
}

func TestModels_S11ZeroAndOptionalWireForms(t *testing.T) {
	turnDetectionZero := TurnDetectionConfig{}
	configWithoutTurnDetection := SessionConfig{Model: "zero-contract"}
	configWithExplicitZeroTurnDetection := SessionConfig{
		Model:         "zero-contract",
		TurnDetection: &turnDetectionZero,
	}
	configWithoutRawPayload := SessionConfig{Model: "zero-contract"}
	configWithNullRawPayload := SessionConfig{Model: "zero-contract", Config: json.RawMessage("null")}
	configWithEmptyRawPayload := SessionConfig{Model: "zero-contract", Config: json.RawMessage{}}
	eventWithoutRawPayload := SessionEvent{Type: SessionEventResponseCreate}
	eventWithNullRawPayload := SessionEvent{Type: SessionEventResponseCreate, Data: json.RawMessage("null")}

	cases := []struct {
		name  string
		value any
		want  string
	}{
		{name: "required-zero-turn-detection", value: TurnDetectionConfig{}, want: `{"type":""}`},
		{name: "required-zero-session-config", value: SessionConfig{}, want: `{"model":""}`},
		{name: "required-zero-session-event", value: SessionEvent{}, want: `{"type":""}`},
		{name: "optional-zero-values-omitted", value: TurnDetectionConfig{Type: "server_vad", Threshold: 0, PrefixPaddingMs: 0, SilenceDurationMs: 0}, want: `{"type":"server_vad"}`},
		{name: "absent-turn-detection", value: configWithoutTurnDetection, want: `{"model":"zero-contract"}`},
		{name: "explicit-zero-turn-detection", value: configWithExplicitZeroTurnDetection, want: `{"model":"zero-contract","turn_detection":{"type":""}}`},
		{name: "absent-raw-payload", value: configWithoutRawPayload, want: `{"model":"zero-contract"}`},
		{name: "non-nil-null-raw-payload", value: configWithNullRawPayload, want: `{"model":"zero-contract","config":null}`},
		{name: "non-nil-empty-raw-payload-is-omitted", value: configWithEmptyRawPayload, want: `{"model":"zero-contract"}`},
		{name: "absent-event-payload", value: eventWithoutRawPayload, want: `{"type":"response.create"}`},
		{name: "non-nil-null-event-payload", value: eventWithNullRawPayload, want: `{"type":"response.create","data":null}`},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			got, err := json.Marshal(testCase.value)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != testCase.want {
				t.Fatalf("wire JSON: want %s, got %s", testCase.want, got)
			}
		})
	}
}

func TestModels_S11SessionEventConstructors(t *testing.T) {
	config := json.RawMessage(`{"model":"grok-realtime-contract","modalities":["text","audio"]}`)
	cases := []struct {
		name     string
		got      SessionEvent
		wantType SessionEventType
		wantData []byte
		wantJSON string
	}{
		{
			name:     "audio-buffer-append",
			got:      NewAudioBufferAppendEvent("AQIDBA=="),
			wantType: SessionEventInputAudioBufferAppend,
			wantData: []byte(`{"audio":"AQIDBA=="}`),
			wantJSON: `{"type":"input_audio_buffer.append","data":{"audio":"AQIDBA=="}}`,
		},
		{
			name:     "audio-buffer-commit",
			got:      NewAudioBufferCommitEvent(),
			wantType: SessionEventInputAudioBufferCommit,
			wantJSON: `{"type":"input_audio_buffer.commit"}`,
		},
		{
			name:     "audio-buffer-clear",
			got:      NewAudioBufferClearEvent(),
			wantType: SessionEventInputAudioBufferClear,
			wantJSON: `{"type":"input_audio_buffer.clear"}`,
		},
		{
			name:     "response-create",
			got:      NewResponseCreateEvent(),
			wantType: SessionEventResponseCreate,
			wantJSON: `{"type":"response.create"}`,
		},
		{
			name:     "response-cancel",
			got:      NewResponseCancelEvent(),
			wantType: SessionEventResponseCancel,
			wantJSON: `{"type":"response.cancel"}`,
		},
		{
			name:     "session-update",
			got:      NewSessionUpdateEvent(config),
			wantType: SessionEventSessionUpdate,
			wantData: config,
			wantJSON: `{"type":"session.update","data":{"model":"grok-realtime-contract","modalities":["text","audio"]}}`,
		},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.got.Type != testCase.wantType {
				t.Fatalf("event type: want %q, got %q", testCase.wantType, testCase.got.Type)
			}
			if !bytes.Equal(testCase.got.Data, testCase.wantData) {
				t.Fatalf("event payload: want %s, got %s", testCase.wantData, testCase.got.Data)
			}
			encoded, err := json.Marshal(testCase.got)
			if err != nil {
				t.Fatalf("marshal event: %v", err)
			}
			if string(encoded) != testCase.wantJSON {
				t.Fatalf("event JSON: want %s, got %s", testCase.wantJSON, encoded)
			}
		})
	}
}
