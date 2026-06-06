package openai

import (
	"encoding/base64"
	"testing"

	"github.com/portpowered/go-agent-loop/pkg/logging"
	"github.com/portpowered/go-llm-gateway/pkg/models"
)

var testLogger = logging.DummyLogger()

// ── User message transformation ──────────────────────────────────────────

func TestMessagesToParams_UserMessageTextOnly(t *testing.T) {
	// When ContentParts has one text part, user message is sent as content array with one text part.
	params := messagesToParams([]models.Message{models.NewTextMessage(models.RoleUser, "Hello")}, testLogger)
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	u := params[0]
	if u.Role != "user" {
		t.Fatalf("expected role user, got %q", u.Role)
	}
	if u.Content == nil || u.Content.parts == nil || len(u.Content.parts) != 1 {
		t.Fatalf("expected content array with 1 part, got %+v", u.Content)
	}
	if u.Content.parts[0].Type != "text" || u.Content.parts[0].Text != "Hello" {
		t.Errorf("expected text part Hello, got %+v", u.Content.parts[0])
	}
}

func TestMessagesToParams_UserMessageWithContentPartsText(t *testing.T) {
	// When ContentParts has only text, produces a content array with one text part.
	params := messagesToParams([]models.Message{{
		Role:         models.RoleUser,
		ContentParts: []models.ContentPart{models.TextPart{Text: "What is in this image?"}},
	}}, testLogger)
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	u := params[0]
	if u.Content == nil || u.Content.parts == nil {
		t.Fatal("expected content array")
	}
	parts := u.Content.parts
	if len(parts) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(parts))
	}
	if parts[0].Type != "text" || parts[0].Text != "What is in this image?" {
		t.Errorf("expected text part, got %+v", parts[0])
	}
}

func TestMessagesToParams_UserMessageWithImagePartBase64(t *testing.T) {
	// Image bytes are encoded as base64 data URL per OpenAI vision guide.
	imgBytes := []byte("fake-png-data")
	params := messagesToParams([]models.Message{{
		Role: models.RoleUser,
		ContentParts: []models.ContentPart{
			models.ImagePart{Bytes: imgBytes, MediaType: "image/png"},
		},
	}}, testLogger)
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	parts := params[0].Content.parts
	if len(parts) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(parts))
	}
	if parts[0].Type != "image_url" || parts[0].ImageURL == nil {
		t.Fatal("expected image_url content part")
	}
	expectedURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imgBytes)
	if parts[0].ImageURL.URL != expectedURL {
		t.Errorf("expected image_url %q, got %q", expectedURL, parts[0].ImageURL.URL)
	}
}

func TestMessagesToParams_UserMessageWithImagePartURL(t *testing.T) {
	// When ImagePart has URL and no bytes, URL is passed through.
	url := "https://example.com/image.png"
	params := messagesToParams([]models.Message{{
		Role: models.RoleUser,
		ContentParts: []models.ContentPart{
			models.ImagePart{URL: url, MediaType: "image/png"},
		},
	}}, testLogger)
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	parts := params[0].Content.parts
	if len(parts) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(parts))
	}
	if parts[0].Type != "image_url" || parts[0].ImageURL == nil {
		t.Fatal("expected image_url content part")
	}
	if parts[0].ImageURL.URL != url {
		t.Errorf("expected image_url url %q, got %q", url, parts[0].ImageURL.URL)
	}
}

func TestMessagesToParams_UserMessageWithAudioPartBase64(t *testing.T) {
	// Audio bytes are base64-encoded as input_audio with format derived from MediaType.
	audioBytes := []byte("fake-wav-bytes")
	params := messagesToParams([]models.Message{{
		Role: models.RoleUser,
		ContentParts: []models.ContentPart{
			models.AudioPart{Bytes: audioBytes, MediaType: "audio/wav"},
		},
	}}, testLogger)
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	parts := params[0].Content.parts
	if len(parts) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(parts))
	}
	if parts[0].Type != "input_audio" || parts[0].InputAudio == nil {
		t.Fatal("expected input_audio content part")
	}
	if parts[0].InputAudio.Data != base64.StdEncoding.EncodeToString(audioBytes) {
		t.Errorf("expected base64 audio data")
	}
	if parts[0].InputAudio.Format != "wav" {
		t.Errorf("expected format wav, got %q", parts[0].InputAudio.Format)
	}
}

func TestMessagesToParams_UserMessageWithAudioPartMp3(t *testing.T) {
	// MediaType audio/mpeg maps to format "mp3".
	audioBytes := []byte("fake-mp3")
	params := messagesToParams([]models.Message{{
		Role: models.RoleUser,
		ContentParts: []models.ContentPart{
			models.AudioPart{Bytes: audioBytes, MediaType: "audio/mpeg"},
		},
	}}, testLogger)
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	parts := params[0].Content.parts
	if len(parts) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(parts))
	}
	if parts[0].Type != "input_audio" || parts[0].InputAudio == nil {
		t.Fatal("expected input_audio content part")
	}
	if parts[0].InputAudio.Format != "mp3" {
		t.Errorf("expected format mp3, got %q", parts[0].InputAudio.Format)
	}
}

func TestMessagesToParams_UserMessageWithTextAndImage(t *testing.T) {
	// Multimodal: text part then image part (order preserved).
	imgBytes := []byte("x")
	params := messagesToParams([]models.Message{{
		Role: models.RoleUser,
		ContentParts: []models.ContentPart{
			models.TextPart{Text: "What do you see?"},
			models.ImagePart{Bytes: imgBytes, MediaType: "image/jpeg"},
		},
	}}, testLogger)
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	parts := params[0].Content.parts
	if len(parts) != 2 {
		t.Fatalf("expected 2 content parts, got %d", len(parts))
	}
	if parts[0].Type != "text" || parts[0].Text != "What do you see?" {
		t.Errorf("first part should be text: %+v", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL == nil {
		t.Errorf("second part should be image_url: %+v", parts[1])
	}
	expectedURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(imgBytes)
	if parts[1].ImageURL.URL != expectedURL {
		t.Errorf("expected image url %q, got %q", expectedURL, parts[1].ImageURL.URL)
	}
}

func TestMessagesToParams_UserMessageWithVideoPartURL(t *testing.T) {
	// Video parts are sent as video_url content parts.
	videoURL := "https://example.com/video.mp4"
	params := messagesToParams([]models.Message{{
		Role: models.RoleUser,
		ContentParts: []models.ContentPart{
			models.VideoPart{URL: videoURL, MediaType: "video/mp4"},
		},
	}}, testLogger)
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	parts := params[0].Content.parts
	if len(parts) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(parts))
	}
	if parts[0].Type != "video_url" || parts[0].VideoURL == nil {
		t.Fatalf("expected video_url content part, got %+v", parts[0])
	}
	if parts[0].VideoURL.URL != videoURL {
		t.Errorf("expected video url %q, got %q", videoURL, parts[0].VideoURL.URL)
	}
}

func TestMessagesToParams_AudioPartEmptyBytesSkipped(t *testing.T) {
	// Audio part with no bytes is skipped.
	params := messagesToParams([]models.Message{{
		Role: models.RoleUser,
		ContentParts: []models.ContentPart{
			models.TextPart{Text: "Hello"},
			models.AudioPart{MediaType: "audio/wav"}, // no Bytes
		},
	}}, testLogger)
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	parts := params[0].Content.parts
	if len(parts) != 1 {
		t.Fatalf("expected 1 content part (audio with no bytes skipped), got %d", len(parts))
	}
	if parts[0].Type != "text" {
		t.Error("expected only text part")
	}
}

func TestAudioFormatFromMediaType(t *testing.T) {
	tests := []struct {
		mediaType string
		want      string
	}{
		{"audio/wav", "wav"},
		{"audio/mpeg", "mp3"},
		{"audio/mp3", "mp3"},
		{"", "wav"},
		{"audio/unknown", "wav"},
	}
	for _, tt := range tests {
		got := audioFormatFromMediaType(tt.mediaType)
		if got != tt.want {
			t.Errorf("audioFormatFromMediaType(%q) = %q, want %q", tt.mediaType, got, tt.want)
		}
	}
}

// ── Assistant message transformation ─────────────────────────────────────

func TestMessagesToParams_AssistantMessageTextOnly(t *testing.T) {
	params := messagesToParams([]models.Message{models.NewTextMessage(models.RoleAssistant, "Here is the answer.")}, testLogger)
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	a := params[0]
	if a.Role != "assistant" {
		t.Fatalf("expected role assistant, got %q", a.Role)
	}
	if a.Content == nil || a.Content.parts == nil || len(a.Content.parts) != 1 {
		t.Fatalf("expected content array with 1 part, got %+v", a.Content)
	}
	if a.Content.parts[0].Type != "text" || a.Content.parts[0].Text != "Here is the answer." {
		t.Errorf("expected Content string, got %+v", a.Content)
	}
	if len(a.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(a.ToolCalls))
	}
}

func TestMessagesToParams_AssistantMessageWithContentPartsText(t *testing.T) {
	params := messagesToParams([]models.Message{{
		Role:         models.RoleAssistant,
		ContentParts: []models.ContentPart{models.TextPart{Text: "Part one."}, models.TextPart{Text: " Part two."}},
	}}, testLogger)
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	a := params[0]
	parts := a.Content.parts
	if len(parts) != 2 {
		t.Fatalf("expected 2 content parts, got %d", len(parts))
	}
	if parts[0].Type != "text" || parts[0].Text != "Part one." {
		t.Errorf("first part: got %+v", parts[0])
	}
	if parts[1].Type != "text" || parts[1].Text != " Part two." {
		t.Errorf("second part: got %+v", parts[1])
	}
}

func TestMessagesToParams_AssistantMessageWithContentPartsTextAndImage(t *testing.T) {
	// Assistant messages with image content now send image_url parts (not serialized as text).
	// Compatible APIs (e.g. OpenRouter) support image_url in assistant messages.
	params := messagesToParams([]models.Message{{
		Role: models.RoleAssistant,
		ContentParts: []models.ContentPart{
			models.TextPart{Text: "Only this text."},
			models.ImagePart{Bytes: []byte("x"), MediaType: "image/png"},
		},
	}}, testLogger)
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	a := params[0]
	parts := a.Content.parts
	if len(parts) != 2 {
		t.Fatalf("expected 2 content parts (text + image_url), got %d", len(parts))
	}
	if parts[0].Type != "text" || parts[0].Text != "Only this text." {
		t.Errorf("expected first part text: %+v", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL == nil {
		t.Errorf("expected second part image_url, got %+v", parts[1])
	}
	expectedURL := "data:image/png;base64,eA=="
	if parts[1].ImageURL.URL != expectedURL {
		t.Errorf("expected image URL %q, got %q", expectedURL, parts[1].ImageURL.URL)
	}
}

func TestMessagesToParams_AssistantMessageWithContentPartsTextAndAudio(t *testing.T) {
	// Assistant messages with audio content now send input_audio parts (not serialized as text).
	// Compatible APIs (e.g. OpenRouter) support input_audio in assistant messages.
	params := messagesToParams([]models.Message{{
		Role: models.RoleAssistant,
		ContentParts: []models.ContentPart{
			models.TextPart{Text: "Summary."},
			models.AudioPart{Bytes: []byte("wav"), MediaType: "audio/wav"},
		},
	}}, testLogger)
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	a := params[0]
	parts := a.Content.parts
	if len(parts) != 2 {
		t.Fatalf("expected 2 content parts (text + input_audio), got %d", len(parts))
	}
	if parts[0].Type != "text" || parts[0].Text != "Summary." {
		t.Errorf("expected first part text: %+v", parts[0])
	}
	if parts[1].Type != "input_audio" || parts[1].InputAudio == nil {
		t.Errorf("expected second part input_audio, got %+v", parts[1])
	}
	expectedData := base64.StdEncoding.EncodeToString([]byte("wav"))
	if parts[1].InputAudio.Data != expectedData {
		t.Errorf("expected audio data %q, got %q", expectedData, parts[1].InputAudio.Data)
	}
	if parts[1].InputAudio.Format != "wav" {
		t.Errorf("expected format wav, got %q", parts[1].InputAudio.Format)
	}
}

func TestMessagesToParams_AssistantMessageWithToolCallsAndContentParts(t *testing.T) {
	params := messagesToParams([]models.Message{{
		Role:         models.RoleAssistant,
		ContentParts: []models.ContentPart{models.TextPart{Text: "Calling a tool."}},
		ToolCalls: []models.ToolCall{
			{ID: "call_1", Name: "get_weather", Arguments: `{"city":"NYC"}`},
		},
	}}, testLogger)
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	a := params[0]
	if len(a.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(a.ToolCalls))
	}
	if a.ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("expected tool name get_weather, got %q", a.ToolCalls[0].Function.Name)
	}
	parts := a.Content.parts
	if len(parts) != 1 || parts[0].Type != "text" || parts[0].Text != "Calling a tool." {
		t.Errorf("expected content part: %+v", a.Content)
	}
}

// ── Tool message transformation ───────────────────────────────────────────

func TestMessagesToParams_ToolMessageTextOnly(t *testing.T) {
	params := messagesToParams([]models.Message{{
		Role:         models.RoleTool,
		ContentParts: []models.ContentPart{models.TextPart{Text: `{"result": "ok"}`}},
		ToolCallID:   "call_abc",
	}}, testLogger)
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	tr := params[0]
	if tr.Role != "tool" {
		t.Fatalf("expected role tool, got %q", tr.Role)
	}
	if tr.ToolCallID != "call_abc" {
		t.Errorf("expected tool_call_id call_abc, got %q", tr.ToolCallID)
	}
	if tr.Content == nil || tr.Content.parts == nil || len(tr.Content.parts) != 1 {
		t.Fatalf("expected content array with 1 part, got %+v", tr.Content)
	}
	if tr.Content.parts[0].Type != "text" || tr.Content.parts[0].Text != `{"result": "ok"}` {
		t.Errorf("expected content string, got %+v", tr.Content)
	}
}

func TestMessagesToParams_ToolMessageWithContentPartsText(t *testing.T) {
	params := messagesToParams([]models.Message{{
		Role:         models.RoleTool,
		ContentParts: []models.ContentPart{models.TextPart{Text: "Tool result text."}},
		ToolCallID:   "call_xyz",
	}}, testLogger)
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	tr := params[0]
	parts := tr.Content.parts
	if len(parts) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(parts))
	}
	if parts[0].Type != "text" || parts[0].Text != "Tool result text." {
		t.Errorf("expected text content, got %+v", parts[0])
	}
}

func TestMessagesToParams_ToolMessageWithContentPartsTextAndImage(t *testing.T) {
	// Tool messages with image content send image_url parts and change role to "user"
	// because the OpenAI API does not support image_url in tool-role messages.
	params := messagesToParams([]models.Message{{
		Role: models.RoleTool,
		ContentParts: []models.ContentPart{
			models.TextPart{Text: "Only text."},
			models.ImagePart{Bytes: []byte("x"), MediaType: "image/png"},
		},
		ToolCallID: "call_1",
	}}, testLogger)
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	tr := params[0]
	if tr.Role != "user" {
		t.Fatalf("expected role changed to user for tool message with image, got %q", tr.Role)
	}
	parts := tr.Content.parts
	if len(parts) != 2 {
		t.Fatalf("expected 2 content parts (text + image_url), got %d", len(parts))
	}
	if parts[0].Type != "text" || parts[0].Text != "Only text." {
		t.Errorf("expected first part text: %+v", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL == nil {
		t.Errorf("expected second part image_url, got %+v", parts[1])
	}
	expectedURL := "data:image/png;base64,eA=="
	if parts[1].ImageURL.URL != expectedURL {
		t.Errorf("expected image URL %q, got %q", expectedURL, parts[1].ImageURL.URL)
	}
}

func TestMessagesToParams_ToolMessageImageAtSecondPosition(t *testing.T) {
	// Verifies that the image workaround triggers even when the image is not the first content part.
	params := messagesToParams([]models.Message{{
		Role: models.RoleTool,
		ContentParts: []models.ContentPart{
			models.TextPart{Text: "Here is the screenshot:"},
			models.ImagePart{Bytes: []byte("img"), MediaType: "image/jpeg"},
		},
		ToolCallID: "call_img2",
	}}, testLogger)
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	tr := params[0]
	if tr.Role != "user" {
		t.Fatalf("expected role user when image is at second position, got %q", tr.Role)
	}
	if tr.ToolCallID != "call_img2" {
		t.Errorf("expected tool_call_id preserved, got %q", tr.ToolCallID)
	}
}

func TestMessagesToParams_ToolMessageImageAtThirdPosition(t *testing.T) {
	// Verifies workaround triggers when image is buried after multiple text parts.
	params := messagesToParams([]models.Message{{
		Role: models.RoleTool,
		ContentParts: []models.ContentPart{
			models.TextPart{Text: "Part one."},
			models.TextPart{Text: "Part two."},
			models.ImagePart{Bytes: []byte("pic"), MediaType: "image/png"},
		},
		ToolCallID: "call_img3",
	}}, testLogger)
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	tr := params[0]
	if tr.Role != "user" {
		t.Fatalf("expected role user when image at third position, got %q", tr.Role)
	}
}

func TestMessagesToParams_ToolMessageImageOnlyAtFirstPosition(t *testing.T) {
	// Image as the only content part (first position) also triggers workaround.
	params := messagesToParams([]models.Message{{
		Role: models.RoleTool,
		ContentParts: []models.ContentPart{
			models.ImagePart{Bytes: []byte("solo"), MediaType: "image/png"},
		},
		ToolCallID: "call_solo",
	}}, testLogger)
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	if params[0].Role != "user" {
		t.Fatalf("expected role user for image-only tool message, got %q", params[0].Role)
	}
}

func TestMessagesToParams_ToolMessageNoImageKeepsToolRole(t *testing.T) {
	// Tool messages without images keep the "tool" role.
	params := messagesToParams([]models.Message{{
		Role: models.RoleTool,
		ContentParts: []models.ContentPart{
			models.TextPart{Text: "result"},
			models.AudioPart{Bytes: []byte("wav"), MediaType: "audio/wav"},
		},
		ToolCallID: "call_noimg",
	}}, testLogger)
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	if params[0].Role != "tool" {
		t.Fatalf("expected role tool when no image present, got %q", params[0].Role)
	}
}

func TestMessagesToParams_ToolMessageWarnsOnRoleChange(t *testing.T) {
	// Verifies that the warning logger is called when role is changed.
	var warned bool
	warnLogger := &captureWarnLogger{onWarn: func(msg string) { warned = true }}
	messagesToParams([]models.Message{{
		Role: models.RoleTool,
		ContentParts: []models.ContentPart{
			models.TextPart{Text: "text"},
			models.ImagePart{Bytes: []byte("x"), MediaType: "image/png"},
		},
		ToolCallID: "call_warn",
	}}, warnLogger)
	if !warned {
		t.Error("expected Warn to be called when tool message role is changed to user")
	}
}

// ── Response → gateway message (non-stream) ───────────────────────────────

func TestResponseToMessage_TextOnly(t *testing.T) {
	msg := responseMsg{
		Role:    "assistant",
		Content: "Hello, world.",
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
		t.Errorf("expected TextPart with same text, got %+v", result.ContentParts[0])
	}
	if len(result.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(result.ToolCalls))
	}
}

func TestResponseToMessage_WithToolCalls(t *testing.T) {
	msg := responseMsg{
		Role:    "assistant",
		Content: "Calling get_weather.",
		ToolCalls: []toolCall{{
			ID:   "call_abc",
			Type: "function",
			Function: toolCallFunc{
				Name:      "get_weather",
				Arguments: `{"city":"NYC"}`,
			},
		}},
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
		t.Errorf("expected tool call get_weather, got ID=%q Name=%q Args=%q", tc.ID, tc.Name, tc.Arguments)
	}
}

func TestResponseToMessage_WithAudio(t *testing.T) {
	audioBytes := []byte("fake-wav-data")
	b64 := base64.StdEncoding.EncodeToString(audioBytes)
	msg := responseMsg{
		Role:    "assistant",
		Content: "Here is the audio.",
		Audio: &audioResp{
			ID:         "audio_1",
			Data:       b64,
			Transcript: "Here is the audio.",
		},
	}
	result := responseToMessage(msg)
	if result.TextContent() != "Here is the audio." {
		t.Errorf("expected content, got %q", result.TextContent())
	}
	if len(result.ContentParts) != 2 {
		t.Fatalf("expected 2 content parts (text + audio), got %d", len(result.ContentParts))
	}
	tp, ok := result.ContentParts[0].(models.TextPart)
	if !ok || tp.Text != "Here is the audio." {
		t.Errorf("expected first part text, got %+v", result.ContentParts[0])
	}
	ap, ok := result.ContentParts[1].(models.AudioPart)
	if !ok {
		t.Fatalf("expected second part AudioPart, got %T", result.ContentParts[1])
	}
	if string(ap.Bytes) != string(audioBytes) {
		t.Errorf("expected decoded audio bytes, got %q", ap.Bytes)
	}
}

func TestResponseToMessage_WithRefusal(t *testing.T) {
	msg := responseMsg{
		Role:    "assistant",
		Refusal: "I cannot assist with that.",
	}
	result := responseToMessage(msg)
	if result.Role != models.RoleAssistant {
		t.Errorf("expected role assistant, got %q", result.Role)
	}
	if result.Refusal != "I cannot assist with that." {
		t.Errorf("expected refusal %q, got %q", "I cannot assist with that.", result.Refusal)
	}
	if len(result.ContentParts) != 0 {
		t.Errorf("expected no content parts when only refusal, got %d", len(result.ContentParts))
	}
}

func TestResponseToMessage_WithRefusalAndContent(t *testing.T) {
	msg := responseMsg{
		Role:    "assistant",
		Content: "Partial response.",
		Refusal: "I cannot continue with that request.",
	}
	result := responseToMessage(msg)
	if result.Refusal != "I cannot continue with that request." {
		t.Errorf("expected refusal populated alongside content, got %q", result.Refusal)
	}
	if result.TextContent() != "Partial response." {
		t.Errorf("expected content preserved, got %q", result.TextContent())
	}
}

func TestResponseToMessage_EmptyContentNoContentParts(t *testing.T) {
	msg := responseMsg{
		Role:    "assistant",
		Content: "",
	}
	result := responseToMessage(msg)
	if result.TextContent() != "" {
		t.Errorf("expected empty content, got %q", result.TextContent())
	}
	if len(result.ContentParts) != 0 {
		t.Errorf("expected no content parts, got %d", len(result.ContentParts))
	}
}

func TestResponseToMessage_InvalidBase64AudioSkipped(t *testing.T) {
	msg := responseMsg{
		Role:    "assistant",
		Content: "Text only.",
		Audio: &audioResp{
			ID:   "a1",
			Data: "not-valid-base64!!!",
		},
	}
	result := responseToMessage(msg)
	// Text part should still be present; audio decode fails so no AudioPart
	if len(result.ContentParts) != 1 {
		t.Fatalf("expected 1 content part (audio decode failed), got %d", len(result.ContentParts))
	}
	if _, ok := result.ContentParts[0].(models.TextPart); !ok {
		t.Errorf("expected only TextPart")
	}
}

// ── Stream chunks → gateway message ──────────────────────────────────────

func TestStreamChunksToMessage_TextOnly(t *testing.T) {
	result := StreamChunksToMessage("Hello", nil)
	if result.Role != models.RoleAssistant {
		t.Errorf("expected role assistant, got %q", result.Role)
	}
	if result.TextContent() != "Hello" {
		t.Errorf("expected content Hello, got %q", result.TextContent())
	}
	if len(result.ContentParts) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(result.ContentParts))
	}
	tp, ok := result.ContentParts[0].(models.TextPart)
	if !ok || tp.Text != "Hello" {
		t.Errorf("expected TextPart, got %+v", result.ContentParts[0])
	}
	if len(result.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(result.ToolCalls))
	}
}

func TestStreamChunksToMessage_EmptyContent(t *testing.T) {
	result := StreamChunksToMessage("", nil)
	if result.TextContent() != "" {
		t.Errorf("expected empty content, got %q", result.TextContent())
	}
	if len(result.ContentParts) != 0 {
		t.Errorf("expected no content parts when content empty, got %d", len(result.ContentParts))
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

// ── Test helpers ─────────────────────────────────────────────────────────

// captureWarnLogger captures Warn calls for testing.
type captureWarnLogger struct {
	logging.Logger
	onWarn func(msg string)
}

func (l *captureWarnLogger) Debug(msg string, fields ...logging.Field) {}
func (l *captureWarnLogger) Info(msg string, fields ...logging.Field)  {}
func (l *captureWarnLogger) Warn(msg string, fields ...logging.Field) {
	if l.onWarn != nil {
		l.onWarn(msg)
	}
}
func (l *captureWarnLogger) Error(msg string, fields ...logging.Field) {}
func (l *captureWarnLogger) Fatal(msg string, fields ...logging.Field) {}
func (l *captureWarnLogger) Panic(msg string, fields ...logging.Field) {}
