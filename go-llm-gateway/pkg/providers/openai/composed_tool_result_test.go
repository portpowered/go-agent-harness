package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

// This file composes the real agent loop (DuplexSession) with the real OpenAI
// Realtime provider session over a mock websocket conn and a scripted tool
// execution. It proves end-to-end provider-wire tool-result delivery without
// network access: a completed tool call must be observable as exactly one
// conversation.item.create carrying a function_call_output item whose call_id
// matches the originating call and whose output carries the serialized result,
// while plain user-text turns keep producing the unchanged
// conversation.item.create + response.create sequence. Audio-only tool
// continuations explicitly request their response because they have no user
// text event to trigger it.

type composedSessionInferencer struct{ session *realtimeSession }

func (c composedSessionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return c.session, nil
}

type composedToolExecutor struct {
	responses map[string]messages.ToolCallResponse
}

func (e composedToolExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	return e.responses[call.ID], nil
}

type wireFrame struct {
	Type  string         `json:"type"`
	Item  map[string]any `json:"item"`
	Delta string         `json:"delta"`
}

func parseWireFrames(t *testing.T, payloads [][]byte) []wireFrame {
	t.Helper()
	frames := make([]wireFrame, 0, len(payloads))
	for _, payload := range payloads {
		var frame wireFrame
		if err := json.Unmarshal(payload, &frame); err != nil {
			t.Fatalf("unmarshal client frame %q: %v", payload, err)
		}
		frames = append(frames, frame)
	}
	return frames
}

func findFunctionCallOutput(frames []wireFrame) []int {
	idx := make([]int, 0, 1)
	for i, frame := range frames {
		if frame.Type == "conversation.item.create" && frame.Item["type"] == "function_call_output" {
			idx = append(idx, i)
		}
	}
	return idx
}

func waitForFunctionCallOutput(t *testing.T, conn *mockWebSocketConn, deadline time.Time) []wireFrame {
	t.Helper()
	for {
		frames := parseWireFrames(t, conn.getClientMessages())
		if len(findFunctionCallOutput(frames)) > 0 {
			return frames
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for function_call_output wire frame; got %v", frames)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForFrameCount(t *testing.T, conn *mockWebSocketConn, n int, deadline time.Time) []wireFrame {
	t.Helper()
	for {
		payloads := conn.getClientMessages()
		if len(payloads) >= n {
			return parseWireFrames(t, payloads)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d client frames, got %d", n, len(payloads))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func addServerEvent(conn *mockWebSocketConn, eventType string, fields map[string]any) {
	conn.addServerEvent(eventType, fields)
}

func TestComposed_LoopDeliversToolResultOnOpenAIRealtimeWire(t *testing.T) {
	const (
		callID    = "call_composed_weather"
		toolName  = "lookup_weather"
		toolArgs  = `{"city":"San Francisco"}`
		toolOut   = `{"forecast":"sunny","temp_c":21}`
		userReply = "thanks, that is all"
	)

	conn := newMockWebSocketConn()
	dialer := &mockWebSocketDialer{conn: conn}
	provider := New(
		WithAPIKey("test-key"),
		WithRealtimeBaseURL("wss://mock.openai.test/v1/realtime"),
		WithWebSocketDialer(dialer),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	session, err := provider.ConnectSession(ctx, models.SessionConfig{Model: "gpt-realtime"})
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	al, err := agentloop.New(
		agentloop.WithMode(engine.DuplexSession),
		agentloop.WithSessionInferencer(composedSessionInferencer{session: session.(*realtimeSession)}),
		agentloop.WithToolExecutor(composedToolExecutor{responses: map[string]messages.ToolCallResponse{
			callID: {ToolCallID: callID, Name: toolName, Content: toolOut},
		}}),
		agentloop.WithTools([]messages.ToolDefinition{{Name: toolName, Description: "weather lookup"}}),
	)
	if err != nil {
		t.Fatalf("agentloop.New: %v", err)
	}

	go func() { _ = al.Run(ctx) }()

	// Turn 1: audio-only user input (no text message enters history).
	if err := al.SendAudioInput(ctx, []byte{1, 2, 3, 4}); err != nil {
		t.Fatalf("SendAudioInput: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	frames := waitForFrameCount(t, conn, 2, deadline) // session.update + input_audio_buffer.append
	if frames[1].Type != "input_audio_buffer.append" {
		t.Fatalf("frame[1] = %s, want input_audio_buffer.append", frames[1].Type)
	}

	// Scripted model turn requesting one tool call.
	addServerEvent(conn, "response.created", nil)
	addServerEvent(conn, "response.output_item.added", map[string]any{
		"item": map[string]any{"type": "function_call", "id": "item_1", "call_id": callID, "name": toolName, "arguments": ""},
	})
	addServerEvent(conn, "response.function_call_arguments.done", map[string]any{
		"call_id": callID, "name": toolName, "arguments": toolArgs,
	})
	addServerEvent(conn, "response.done", nil)

	frames = waitForFunctionCallOutput(t, conn, deadline)
	fcoIdx := findFunctionCallOutput(frames)
	if len(fcoIdx) != 1 {
		t.Fatalf("observed %d function_call_output frames before reply turn, want exactly 1", len(fcoIdx))
	}
	fco := frames[fcoIdx[0]].Item
	if got, _ := fco["call_id"].(string); got != callID {
		t.Errorf("function_call_output call_id = %q, want %q", got, callID)
	}
	if got, _ := fco["output"].(string); got != toolOut {
		t.Errorf("function_call_output output = %q, want serialized result %q", got, toolOut)
	}

	// Turn 2: plain user-text turn must keep the unchanged pairing.
	if err := al.SendSessionEvent(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue(userReply),
	}); err != nil {
		t.Fatalf("SendSessionEvent: %v", err)
	}
	frames = waitForFrameCount(t, conn, len(parseWireFrames(t, conn.getClientMessages()))+2, deadline)

	// Full deterministic client-to-server event sequence.
	gotTypes := make([]string, 0, len(frames))
	for _, frame := range frames {
		gotTypes = append(gotTypes, frame.Type)
	}
	wantTypes := []string{
		"session.update",
		"input_audio_buffer.append",
		"conversation.item.create", // function_call_output
		"response.create",          // audio-only tool continuation
		"conversation.item.create", // user text turn
		"response.create",
	}
	if len(gotTypes) != len(wantTypes) {
		t.Fatalf("client event sequence = %v, want %v", gotTypes, wantTypes)
	}
	for i := range wantTypes {
		if gotTypes[i] != wantTypes[i] {
			t.Fatalf("client event sequence = %v, want %v (mismatch at %d)", gotTypes, wantTypes, i)
		}
	}

	// Exactly one function_call_output across the whole run.
	if idx := findFunctionCallOutput(frames); len(idx) != 1 {
		t.Fatalf("total function_call_output frames = %d, want exactly 1", len(idx))
	}

	// The user-text item keeps its byte-compatible shape.
	textItem := frames[4].Item
	if got, _ := textItem["type"].(string); got != "message" {
		t.Errorf("text turn item.type = %q, want message", got)
	}
	if got, _ := textItem["role"].(string); got != "user" {
		t.Errorf("text turn item.role = %q, want user", got)
	}
	content, ok := textItem["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("text turn item.content = %#v, want one part", textItem["content"])
	}
	part, _ := content[0].(map[string]any)
	if part["type"] != "input_text" || part["text"] != userReply {
		t.Errorf("text turn content part = %#v, want input_text %q", part, userReply)
	}

	// The audio-only tool continuation and the later plain-text turn each
	// request exactly one response.
	responseCreates := 0
	for _, frame := range frames {
		if frame.Type == "response.create" {
			responseCreates++
		}
	}
	if responseCreates != 2 {
		t.Fatalf("response.create count = %d, want exactly 2 (tool continuation and plain-text turn)", responseCreates)
	}
}

func TestComposed_LoopDeliversMixedToolBatchExactlyOnceOnOpenAIRealtimeWire(t *testing.T) {
	const (
		textCallID  = "call_composed_text"
		imageCallID = "call_composed_image"
		textTool    = "lookup_weather"
		imageTool   = "read_image"
		textOutput  = "text sibling result"
	)
	imageBytes := []byte("committed image bytes")

	conn := newMockWebSocketConn()
	dialer := &mockWebSocketDialer{conn: conn}
	provider := New(
		WithAPIKey("test-key"),
		WithRealtimeBaseURL("wss://mock.openai.test/v1/realtime"),
		WithWebSocketDialer(dialer),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	session, err := provider.ConnectSession(ctx, models.SessionConfig{Model: "gpt-realtime"})
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	al, err := agentloop.New(
		agentloop.WithMode(engine.DuplexSession),
		agentloop.WithSessionInferencer(composedSessionInferencer{session: session.(*realtimeSession)}),
		agentloop.WithToolExecutor(composedToolExecutor{responses: map[string]messages.ToolCallResponse{
			textCallID:  {ToolCallID: textCallID, Content: textOutput},
			imageCallID: {ToolCallID: imageCallID, ContentParts: []messages.ContentPart{messages.ImagePart{Bytes: imageBytes, MediaType: "image/png"}}},
		}}),
		agentloop.WithTools([]messages.ToolDefinition{
			{Name: textTool, Description: "text lookup"},
			{Name: imageTool, Description: "image lookup"},
		}),
	)
	if err != nil {
		t.Fatalf("agentloop.New: %v", err)
	}

	go func() { _ = al.Run(ctx) }()
	if err := al.SendAudioInput(ctx, []byte{1, 2, 3, 4}); err != nil {
		t.Fatalf("SendAudioInput: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	waitForFrameCount(t, conn, 2, deadline) // session.update + input_audio_buffer.append

	// One provider response requests two tools in a single batch: the first
	// result is text-only and the second carries the image content.
	addServerEvent(conn, "response.created", nil)
	addServerEvent(conn, "response.output_item.added", map[string]any{
		"item": map[string]any{"type": "function_call", "id": "item_text", "call_id": textCallID, "name": textTool, "arguments": ""},
	})
	addServerEvent(conn, "response.function_call_arguments.done", map[string]any{
		"call_id": textCallID, "name": textTool, "arguments": `{"city":"Seattle"}`,
	})
	addServerEvent(conn, "response.output_item.added", map[string]any{
		"item": map[string]any{"type": "function_call", "id": "item_image", "call_id": imageCallID, "name": imageTool, "arguments": ""},
	})
	addServerEvent(conn, "response.function_call_arguments.done", map[string]any{
		"call_id": imageCallID, "name": imageTool, "arguments": `{"path":"fixture.png"}`,
	})
	addServerEvent(conn, "response.done", nil)

	frames := waitForFrameCount(t, conn, 6, deadline)
	wantTypes := []string{
		"session.update",
		"input_audio_buffer.append",
		"conversation.item.create", // text function_call_output
		"conversation.item.create", // image function_call_output
		"conversation.item.create", // image input message
		"response.create",
	}
	gotTypes := make([]string, 0, len(frames))
	for _, frame := range frames {
		gotTypes = append(gotTypes, frame.Type)
	}
	if len(gotTypes) != len(wantTypes) {
		t.Fatalf("client event sequence = %v, want %v", gotTypes, wantTypes)
	}
	for i := range wantTypes {
		if gotTypes[i] != wantTypes[i] {
			t.Fatalf("client event sequence = %v, want %v (mismatch at %d)", gotTypes, wantTypes, i)
		}
	}

	outputs := map[string]string{}
	imageItems := 0
	for _, frame := range frames {
		if frame.Type != "conversation.item.create" {
			continue
		}
		switch frame.Item["type"] {
		case "function_call_output":
			callID, _ := frame.Item["call_id"].(string)
			output, _ := frame.Item["output"].(string)
			outputs[callID] = output
		case "message":
			content, ok := frame.Item["content"].([]any)
			if !ok {
				continue
			}
			for _, rawPart := range content {
				part, _ := rawPart.(map[string]any)
				if part["type"] != "input_image" {
					continue
				}
				imageItems++
				wantURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imageBytes)
				if got, _ := part["image_url"].(string); got != wantURL {
					t.Fatalf("image URL = %q, want original image bytes", got)
				}
			}
		}
	}
	if len(outputs) != 2 {
		t.Fatalf("function_call_output call IDs = %#v, want one result for each call", outputs)
	}
	if outputs[textCallID] != textOutput {
		t.Fatalf("text result = %q, want %q", outputs[textCallID], textOutput)
	}
	if _, ok := outputs[imageCallID]; !ok {
		t.Fatalf("image result missing from function_call_output map: %#v", outputs)
	}
	if imageItems != 1 {
		t.Fatalf("image input items = %d, want exactly 1", imageItems)
	}
	responseCreates := 0
	for _, frame := range frames {
		if frame.Type == "response.create" {
			responseCreates++
		}
	}
	if responseCreates != 1 {
		t.Fatalf("response.create count = %d, want exactly 1 for the mixed batch", responseCreates)
	}
}
