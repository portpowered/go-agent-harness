package anthropic

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

func TestMapToolToInputSchemaPreservesCompletePageSchema(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"moves":{"type":"array","items":{"type":"object","properties":{"face":{"type":"string","enum":["R","U"]},"turns":{"type":"integer","minimum":1}},"required":["face","turns"],"additionalProperties":false}}},"required":["moves"],"additionalProperties":false}`)
	input := mapToolToInputSchema(models.ToolDefinition{ParameterSchema: schema})
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal complete Anthropic schema: %v", err)
	}
	var got, want any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("decode serialized Anthropic schema: %v", err)
	}
	if err := json.Unmarshal(schema, &want); err != nil {
		t.Fatalf("decode expected schema: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("serialized Anthropic schema = %#v, want %#v", got, want)
	}
}

func TestCompleteToolInputSchema(t *testing.T) {
	tests := []struct {
		name    string
		raw     json.RawMessage
		wantOK  bool
		checkFn func(t *testing.T, schema anthropic.ToolInputSchemaParam)
	}{
		{
			name:   "nil schema is not complete",
			raw:    nil,
			wantOK: false,
		},
		{
			name:   "empty schema is not complete",
			raw:    json.RawMessage(``),
			wantOK: false,
		},
		{
			name:   "malformed JSON is not complete",
			raw:    json.RawMessage(`{"type":"object",`),
			wantOK: false,
		},
		{
			name:   "non-object schema type is not complete",
			raw:    json.RawMessage(`{"type":"array","items":{"type":"string"}}`),
			wantOK: false,
		},
		{
			name:   "JSON null decodes to a nil field map and is not complete",
			raw:    json.RawMessage(`null`),
			wantOK: false,
		},
		{
			name:   "object schema with no properties defaults to an empty map",
			raw:    json.RawMessage(`{"type":"object","required":["x"],"additionalProperties":false}`),
			wantOK: true,
			checkFn: func(t *testing.T, schema anthropic.ToolInputSchemaParam) {
				props, ok := schema.Properties.(map[string]any)
				if !ok {
					t.Fatalf("expected Properties to be map[string]any, got %T", schema.Properties)
				}
				if len(props) != 0 {
					t.Errorf("expected empty properties map, got %v", props)
				}
				if !reflect.DeepEqual(schema.Required, []string{"x"}) {
					t.Errorf("expected required [x], got %v", schema.Required)
				}
				if schema.ExtraFields["additionalProperties"] != false {
					t.Errorf("expected additionalProperties carried through ExtraFields, got %v", schema.ExtraFields)
				}
				for _, key := range []string{"type", "properties", "required"} {
					if _, present := schema.ExtraFields[key]; present {
						t.Errorf("expected ExtraFields to omit %q, got %v", key, schema.ExtraFields)
					}
				}
			},
		},
		{
			name:   "omitted type defaults to object and preserves nested properties plus root extensions",
			raw:    json.RawMessage(`{"properties":{"move":{"type":"object","properties":{"face":{"type":"string","enum":["R","U"]}},"required":["face"]}},"required":["move"],"$schema":"https://json-schema.org/draft/2020-12/schema"}`),
			wantOK: true,
			checkFn: func(t *testing.T, schema anthropic.ToolInputSchemaParam) {
				if string(schema.Type) != "object" {
					t.Errorf("expected type object, got %q", schema.Type)
				}
				props, ok := schema.Properties.(map[string]any)
				if !ok {
					t.Fatalf("expected Properties to be map[string]any, got %T", schema.Properties)
				}
				var wantMove any
				if err := json.Unmarshal(json.RawMessage(`{"type":"object","properties":{"face":{"type":"string","enum":["R","U"]}},"required":["face"]}`), &wantMove); err != nil {
					t.Fatalf("decode expected nested schema: %v", err)
				}
				if !reflect.DeepEqual(props["move"], wantMove) {
					t.Fatalf("nested move schema = %#v, want %#v", props["move"], wantMove)
				}
				if !reflect.DeepEqual(schema.Required, []string{"move"}) {
					t.Errorf("expected required [move], got %v", schema.Required)
				}
				if schema.ExtraFields["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
					t.Errorf("expected $schema carried through ExtraFields, got %v", schema.ExtraFields)
				}
				for _, key := range []string{"type", "properties", "required"} {
					if _, present := schema.ExtraFields[key]; present {
						t.Errorf("expected ExtraFields to omit %q, got %v", key, schema.ExtraFields)
					}
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			schema, ok := completeToolInputSchema(tc.raw)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if tc.checkFn != nil {
				tc.checkFn(t, schema)
			}
		})
	}
}

func TestMapToolToInputSchemaFallsBackWithoutParameterSchema(t *testing.T) {
	// A tool with a malformed/absent ParameterSchema must fall back to the
	// flattened models.ToolParameter list rather than returning an empty schema.
	input := mapToolToInputSchema(models.ToolDefinition{
		ParameterSchema: json.RawMessage(`{"type":"array"}`),
		Parameters: []models.ToolParameter{
			{Name: "city", Type: "string", Description: "City name", Required: true},
		},
	})
	if string(input.Type) != "object" {
		t.Errorf("expected fallback type object, got %q", input.Type)
	}
	props, ok := input.Properties.(map[string]any)
	if !ok {
		t.Fatalf("expected Properties to be map[string]any, got %T", input.Properties)
	}
	cityProp, ok := props["city"].(map[string]any)
	if !ok {
		t.Fatalf("expected city property, got %v", props)
	}
	if cityProp["type"] != "string" || cityProp["description"] != "City name" {
		t.Errorf("unexpected city property, got %v", cityProp)
	}
	if !reflect.DeepEqual(input.Required, []string{"city"}) {
		t.Errorf("expected required [city], got %v", input.Required)
	}
}

func TestMessagesToParams_UserMessageTextOnly(t *testing.T) {
	system, params, err := messagesToParams([]models.Message{models.NewTextMessage(models.RoleUser, "Hello")})
	if err != nil {
		t.Fatal(err)
	}
	if len(system) != 0 {
		t.Fatalf("expected no system blocks, got %d", len(system))
	}
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	if params[0].Role != anthropic.MessageParamRoleUser {
		t.Fatal("expected user message")
	}
	if len(params[0].Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(params[0].Content))
	}
	if params[0].Content[0].GetText() == nil || *params[0].Content[0].GetText() != "Hello" {
		t.Errorf("expected text block Hello, got %+v", params[0].Content[0])
	}
}

func TestMessagesToParams_UserMessageWithContentPartsText(t *testing.T) {
	_, params, err := messagesToParams([]models.Message{{
		Role:         models.RoleUser,
		ContentParts: []models.ContentPart{models.TextPart{Text: "What is in this image?"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	if len(params[0].Content) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(params[0].Content))
	}
	if params[0].Content[0].GetText() == nil || *params[0].Content[0].GetText() != "What is in this image?" {
		t.Errorf("expected text part, got %+v", params[0].Content[0])
	}
}

func TestMessagesToParams_UserMessageWithImagePartBase64(t *testing.T) {
	imgBytes := []byte("fake-png-data")
	_, params, err := messagesToParams([]models.Message{{
		Role: models.RoleUser,
		ContentParts: []models.ContentPart{
			models.ImagePart{Bytes: imgBytes, MediaType: "image/png"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	if len(params[0].Content) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(params[0].Content))
	}
	if params[0].Content[0].OfImage == nil {
		t.Fatal("expected image content part")
	}
	// OfImage has Source with base64 data
	if params[0].Content[0].OfImage.Source.OfBase64 == nil {
		t.Fatal("expected base64 image source")
	}
	if params[0].Content[0].OfImage.Source.OfBase64.Data != base64.StdEncoding.EncodeToString(imgBytes) {
		t.Error("expected base64 image data to match")
	}
}

func TestMessagesToParams_UserMessageWithImagePartURL(t *testing.T) {
	url := "https://example.com/image.png"
	_, params, err := messagesToParams([]models.Message{{
		Role: models.RoleUser,
		ContentParts: []models.ContentPart{
			models.ImagePart{URL: url, MediaType: "image/png"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	if params[0].Content[0].OfImage == nil {
		t.Fatal("expected image content part")
	}
	if params[0].Content[0].OfImage.Source.OfURL == nil || params[0].Content[0].OfImage.Source.OfURL.URL != url {
		t.Errorf("expected image URL %q", url)
	}
}

func TestMessagesToParams_UserMessageWithAudioPartBase64(t *testing.T) {
	// Anthropic has no native audio input; we encode as text block with data URL reference.
	audioBytes := []byte("fake-wav-bytes")
	_, params, err := messagesToParams([]models.Message{{
		Role: models.RoleUser,
		ContentParts: []models.ContentPart{
			models.AudioPart{Bytes: audioBytes, MediaType: "audio/wav"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	// Should be a single text block containing [Audio input: data:...]
	if len(params[0].Content) != 1 {
		t.Fatalf("expected 1 content part (audio as text ref), got %d", len(params[0].Content))
	}
	txt := params[0].Content[0].GetText()
	if txt == nil {
		t.Fatal("expected text block for audio reference")
	}
	if *txt == "" || (*txt)[0] != '[' {
		t.Errorf("expected [Audio input: ...] text, got %q", *txt)
	}
}

func TestMessagesToParams_UserMessageWithTextAndImage(t *testing.T) {
	imgBytes := []byte("x")
	_, params, err := messagesToParams([]models.Message{{
		Role: models.RoleUser,
		ContentParts: []models.ContentPart{
			models.TextPart{Text: "What do you see?"},
			models.ImagePart{Bytes: imgBytes, MediaType: "image/jpeg"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	if len(params[0].Content) != 2 {
		t.Fatalf("expected 2 content parts, got %d", len(params[0].Content))
	}
	if params[0].Content[0].GetText() == nil || *params[0].Content[0].GetText() != "What do you see?" {
		t.Errorf("first part should be text")
	}
	if params[0].Content[1].OfImage == nil {
		t.Error("second part should be image")
	}
}

func TestMessagesToParams_SystemMessage(t *testing.T) {
	system, params, err := messagesToParams([]models.Message{models.NewTextMessage(models.RoleSystem, "You are helpful.")})
	if err != nil {
		t.Fatal(err)
	}
	if len(system) != 1 {
		t.Fatalf("expected 1 system block, got %d", len(system))
	}
	if system[0].Text != "You are helpful." {
		t.Errorf("expected system text, got %q", system[0].Text)
	}
	if len(params) != 0 {
		t.Fatalf("expected no message params, got %d", len(params))
	}
}

func TestMessagesToParams_AssistantMessage(t *testing.T) {
	_, params, err := messagesToParams([]models.Message{models.NewTextMessage(models.RoleAssistant, "Here is the answer.")})
	if err != nil {
		t.Fatal(err)
	}
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	if params[0].Role != anthropic.MessageParamRoleAssistant {
		t.Fatal("expected assistant message")
	}
	if len(params[0].Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(params[0].Content))
	}
	if params[0].Content[0].GetText() == nil || *params[0].Content[0].GetText() != "Here is the answer." {
		t.Errorf("expected content, got %+v", params[0].Content[0])
	}
}

func TestMessagesToParams_AssistantMessageWithToolCalls(t *testing.T) {
	_, params, err := messagesToParams([]models.Message{{
		Role:         models.RoleAssistant,
		ContentParts: []models.ContentPart{models.TextPart{Text: "Calling a tool."}},
		ToolCalls: []models.ToolCall{
			{ID: "call_1", Name: "get_weather", Arguments: `{"city":"NYC"}`},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	if len(params[0].Content) != 2 {
		t.Fatalf("expected 2 blocks (text + tool_use), got %d", len(params[0].Content))
	}
	if params[0].Content[1].OfToolUse == nil {
		t.Fatal("expected tool_use block")
	}
	if params[0].Content[1].OfToolUse.Name != "get_weather" {
		t.Errorf("expected tool name get_weather, got %q", params[0].Content[1].OfToolUse.Name)
	}
}

func TestMessagesToParams_ToolMessagesMergedIntoUser(t *testing.T) {
	_, params, err := messagesToParams([]models.Message{
		models.NewTextMessage(models.RoleUser, "Hi"),
		{Role: models.RoleAssistant, ContentParts: []models.ContentPart{models.TextPart{Text: "I'll call a tool."}}, ToolCalls: []models.ToolCall{{ID: "tc1", Name: "foo", Arguments: "{}"}}},
		{Role: models.RoleTool, ToolCallID: "tc1", ContentParts: []models.ContentPart{models.TextPart{Text: `{"result": "ok"}`}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(params) != 3 {
		t.Fatalf("expected 3 params (user, assistant, user with tool_result), got %d", len(params))
	}
	if params[2].Role != anthropic.MessageParamRoleUser {
		t.Fatal("expected third message to be user (tool results)")
	}
	if len(params[2].Content) != 1 {
		t.Fatalf("expected 1 tool_result block, got %d", len(params[2].Content))
	}
	if params[2].Content[0].OfToolResult == nil {
		t.Fatal("expected tool_result block")
	}
	if params[2].Content[0].OfToolResult.ToolUseID != "tc1" {
		t.Errorf("expected tool_use_id tc1, got %q", params[2].Content[0].OfToolResult.ToolUseID)
	}
	if len(params[2].Content[0].OfToolResult.Content) == 0 {
		t.Error("expected tool_result to have content")
	}
}

func TestMessagesToParams_MalformedToolCallArguments(t *testing.T) {
	_, _, err := messagesToParams([]models.Message{{
		Role: models.RoleAssistant,
		ToolCalls: []models.ToolCall{
			{ID: "call_1", Name: "broken_tool", Arguments: `{not valid json`},
		},
	}})
	if err == nil {
		t.Fatal("expected error for malformed JSON arguments, got nil")
	}
	if !strings.Contains(err.Error(), "broken_tool") {
		t.Errorf("expected error to mention tool name, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "{not valid json") {
		t.Errorf("expected error to include raw argument string, got %q", err.Error())
	}
}

func TestResponseToMessage_TextOnly(t *testing.T) {
	msg := anthropic.Message{
		Content: []anthropic.ContentBlockUnion{
			{Type: "text", Text: "Hello, world."},
		},
	}
	result := responseToMessage(msg)
	if result.Role != models.RoleAssistant {
		t.Errorf("expected role assistant, got %q", result.Role)
	}
	if result.TextContent() != "Hello, world." {
		t.Errorf("expected content %q, got %q", "Hello, world.", result.TextContent())
	}
	if len(result.ContentParts) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(result.ContentParts))
	}
	tp, ok := result.ContentParts[0].(models.TextPart)
	if !ok || tp.Text != "Hello, world." {
		t.Errorf("expected TextPart, got %+v", result.ContentParts[0])
	}
}

func TestResponseToMessage_WithToolCalls(t *testing.T) {
	msg := anthropic.Message{
		Content: []anthropic.ContentBlockUnion{
			{Type: "text", Text: "Calling get_weather."},
			{Type: "tool_use", ID: "call_abc", Name: "get_weather", Input: []byte(`{"city":"NYC"}`)},
		},
	}
	result := responseToMessage(msg)
	if result.TextContent() != "Calling get_weather." {
		t.Errorf("expected content, got %q", result.TextContent())
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.ToolCalls))
	}
	tc := result.ToolCalls[0]
	if tc.ID != "call_abc" || tc.Name != "get_weather" || tc.Arguments != `{"city":"NYC"}` {
		t.Errorf("expected tool call, got ID=%q Name=%q Args=%q", tc.ID, tc.Name, tc.Arguments)
	}
}

func TestStreamChunksToMessage_TextOnly(t *testing.T) {
	result := StreamChunksToMessage("Hello", nil)
	if result.Role != models.RoleAssistant {
		t.Errorf("expected role assistant, got %q", result.Role)
	}
	if result.TextContent() != "Hello" {
		t.Errorf("expected content Hello, got %q", result.TextContent())
	}
	if len(result.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(result.ToolCalls))
	}
}

func TestStreamChunksToMessage_WithToolCalls(t *testing.T) {
	toolCalls := []models.ToolCall{
		{ID: "call_1", Name: "run", Arguments: `{"cmd":"ls"}`},
	}
	result := StreamChunksToMessage("Done.", toolCalls)
	if result.TextContent() != "Done." {
		t.Errorf("expected content Done., got %q", result.TextContent())
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.ToolCalls))
	}
	if result.ToolCalls[0].Name != "run" {
		t.Errorf("expected tool name run, got %q", result.ToolCalls[0].Name)
	}
}
