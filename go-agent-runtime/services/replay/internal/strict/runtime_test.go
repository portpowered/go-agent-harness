package strict

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	publicreplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/recording"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestAttachReplayMediaPreservesProviderInterruptionBoundary(t *testing.T) {
	media := sharedaudio.NewSessionMediaAtRate(nil, 24000)
	defer func() {
		if err := media.Close(); err != nil {
			t.Errorf("close media: %v", err)
		}
	}()

	attachReplayMedia(context.Background(), &replayMediaTestSession{media: media})
	response := sharedaudio.PlaybackResponse{ResponseID: "resp-interrupted", ItemID: "item-interrupted"}
	media.StartInboundResponse(response)
	if err := media.PushInbound(make([]int16, 720)); err != nil {
		t.Fatal(err)
	}

	interruption, ok := media.InterruptInbound()
	if !ok || interruption.PlaybackResponse != response || interruption.AudioEndMS != 0 {
		t.Fatalf("replay interruption = %+v/%t, want response at zero cursor", interruption, ok)
	}
}

func TestStrictInputActionsPreserveAudioBoundariesAndRejectOverlap(t *testing.T) {
	capture := gwtesting.SessionCapture{Records: []gwtesting.CapturedSessionEvent{
		{Sequence: 1, Direction: gwtesting.DirectionClientToServer, Type: "input_audio_buffer.append", Payload: []byte(`{"type":"input_audio_buffer.append","audio":"AQI="}`)},
		{Sequence: 2, Direction: gwtesting.DirectionClientToServer, Type: "input_audio_buffer.append", Payload: []byte(`{"type":"input_audio_buffer.append","audio":"AwQ="}`)},
		{Sequence: 3, Direction: gwtesting.DirectionServerToClient, Type: responseDoneType, Payload: []byte(`{"type":"response.done"}`)},
		{Sequence: 4, Direction: gwtesting.DirectionClientToServer, Type: "conversation.item.create", Payload: []byte(`{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`)},
		{Sequence: 5, Direction: gwtesting.DirectionServerToClient, Type: responseDoneType, Payload: []byte(`{"type":"response.done"}`)},
	}}
	actions, err := deriveInputActions(capture)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 2 || len(actions[0].audio) != 2 || string(actions[0].audio[0]) != "\x01\x02" || string(actions[0].audio[1]) != "\x03\x04" {
		t.Fatalf("audio actions = %+v, want two coalesced PCM appends", actions)
	}
	if actions[0].responseEnds != 1 || actions[1].text != "hello" || actions[1].responseEnds != 1 {
		t.Fatalf("actions = %+v, want response boundaries and text preserved", actions)
	}

	overlapping := gwtesting.SessionCapture{Records: []gwtesting.CapturedSessionEvent{
		{Sequence: 1, Direction: gwtesting.DirectionClientToServer, Type: "conversation.item.create", Payload: []byte(`{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}]}}`)},
		{Sequence: 2, Direction: gwtesting.DirectionServerToClient, Type: "response.output_text.done", Payload: []byte(`{"type":"response.output_text.done"}`)},
		{Sequence: 3, Direction: gwtesting.DirectionClientToServer, Type: "conversation.item.create", Payload: []byte(`{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"second"}]}}`)},
		{Sequence: 4, Direction: gwtesting.DirectionServerToClient, Type: responseDoneType, Payload: []byte(`{"type":"response.done"}`)},
	}}
	if err := func() error {
		_, err := deriveInputActions(overlapping)
		return err
	}(); !errors.Is(err, publicreplay.ErrBundleMismatch) {
		t.Fatalf("overlapping input error = %v, want bundle mismatch", err)
	}
}

func TestStrictInputActionsRejectMalformedAndUnsupportedControls(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload []byte
		want    error
	}{
		{name: "malformed JSON", payload: []byte("{"), want: publicreplay.ErrBundleIncomplete},
		{name: "empty audio", payload: []byte(`{"type":"input_audio_buffer.append"}`), want: publicreplay.ErrBundleIncomplete},
		{name: "invalid audio", payload: []byte(`{"type":"input_audio_buffer.append","audio":"not-base64"}`), want: publicreplay.ErrBundleIncomplete},
		{name: "unsupported cancellation", payload: []byte(`{"type":"response.cancel"}`), want: publicreplay.ErrBundleMismatch},
	} {
		t.Run(test.name, func(t *testing.T) {
			builder := inputBuilder{audioAction: -1}
			if err := builder.consume(9, 0, test.payload); !errors.Is(err, test.want) {
				t.Fatalf("consume error = %v, want %v", err, test.want)
			}
		})
	}

	builder := inputBuilder{audioAction: -1}
	for _, payload := range []string{
		`{"type":"conversation.item.create","item":{"type":"function_call","role":"assistant"}}`,
		`{"type":"conversation.item.create","item":{"type":"message","role":"assistant","content":[{"type":"input_text","text":"ignored"}]}}`,
		`{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_image"}]}}`,
		`{"type":"session.update"}`,
	} {
		if err := builder.consume(1, 0, []byte(payload)); err != nil {
			t.Fatalf("unsupported input %s: %v", payload, err)
		}
	}
	if len(builder.actions) != 0 {
		t.Fatalf("unsupported inputs created actions: %+v", builder.actions)
	}
}

func TestStrictRecordedToolsRejectInvalidIdentityAndFailureShapes(t *testing.T) {
	for _, call := range []messages.ToolCall{
		{},
		{ID: "call", Arguments: `{}`},
		{ID: "call", Name: "lookup", Arguments: "not-json"},
	} {
		executor := newRecordedToolExecutor()
		if err := executor.addCall(call); !errors.Is(err, publicreplay.ErrBundleIncomplete) {
			t.Fatalf("invalid call %+v error = %v, want incomplete", call, err)
		}
	}

	executor := newRecordedToolExecutor()
	valid := messages.ToolCall{ID: "call-1", Name: "lookup", Arguments: `{}`}
	if err := executor.addCall(valid); err != nil {
		t.Fatal(err)
	}
	if err := executor.addCall(valid); !errors.Is(err, publicreplay.ErrBundleMismatch) {
		t.Fatalf("duplicate call error = %v, want mismatch", err)
	}
	for _, result := range []decodedToolResult{
		{callID: "unknown", name: "lookup"},
		{callID: valid.ID, name: "other"},
		{callID: valid.ID, name: valid.Name, response: messages.ToolCallResponse{ToolCallID: "other"}},
	} {
		if err := executor.addResult(result); !errors.Is(err, publicreplay.ErrBundleMismatch) {
			t.Fatalf("invalid result %+v error = %v, want mismatch", result, err)
		}
	}
	if err := executor.addResult(decodedToolResult{callID: valid.ID, name: valid.Name, response: messages.ToolCallResponse{Content: "ok"}}); err != nil {
		t.Fatal(err)
	}
	if err := executor.validateShape(); err != nil {
		t.Fatal(err)
	}
	response, err := executor.Execute(context.Background(), valid)
	if err != nil || response.ToolCallID != valid.ID || response.Name != valid.Name {
		t.Fatalf("normalized response = %+v, error = %v", response, err)
	}
	if _, err := executor.Execute(context.Background(), valid); !errors.Is(err, publicreplay.ErrToolMismatch) {
		t.Fatalf("duplicate execution error = %v, want mismatch", err)
	}

	failed := newRecordedToolExecutor()
	failedCall := messages.ToolCall{ID: "failed", Name: "lookup", Arguments: `{}`}
	if err := failed.addCall(failedCall); err != nil {
		t.Fatal(err)
	}
	if err := failed.addResult(decodedToolResult{callID: failedCall.ID, name: failedCall.Name, failed: true}); err != nil {
		t.Fatal(err)
	}
	if err := failed.validateShape(); err != nil {
		t.Fatal(err)
	}
	if _, err := failed.Execute(context.Background(), failedCall); !errors.Is(err, publicreplay.ErrToolFailure) {
		t.Fatalf("failed execution error = %v, want tool failure", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := executor.Execute(ctx, valid); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled execution error = %v, want context canceled", err)
	}
	var nilExecutor *recordedToolExecutor
	if got := nilExecutor.ExpectedToolCalls(); got != -1 {
		t.Fatalf("nil expected tool count = %d, want -1", got)
	}
}

func TestStrictToolDecodersPreserveWireIdentity(t *testing.T) {
	call, err := decodeToolCall([]byte(`{"id":"call-1","name":"lookup","arguments":"{}"}`))
	if err != nil || call.ID != "call-1" || call.Name != "lookup" || call.Arguments != "{}" {
		t.Fatalf("decoded lower-case call = %+v, error = %v", call, err)
	}
	if _, err := decodeToolCall([]byte("{")); err == nil {
		t.Fatal("malformed tool call unexpectedly decoded")
	}
	result, err := decodeToolResult([]byte(`{"call_id":"call-1","name":"lookup","response":{"tool_call_id":"call-1","name":"lookup","content":"ok"}}`))
	if err != nil || result.response.ToolCallID != "call-1" || result.response.Name != "lookup" || result.response.Content != "ok" {
		t.Fatalf("decoded tool result = %+v, error = %v", result, err)
	}
	if _, err := decodeToolResult([]byte(`{"name":"lookup","response":{}}`)); err == nil {
		t.Fatal("empty call_id unexpectedly decoded")
	}
	if _, err := decodeToolResult([]byte(`{"call_id":"call-1","response":{"ToolCallID":"a","tool_call_id":"b"}}`)); !errors.Is(err, publicreplay.ErrBundleMismatch) {
		t.Fatalf("disagreeing nested IDs error = %v, want mismatch", err)
	}
	if _, err := decodeToolResult([]byte(`{"call_id":"call-1","response":{"tool_call_id":"other"}}`)); !errors.Is(err, publicreplay.ErrBundleMismatch) {
		t.Fatalf("mismatched nested ID error = %v, want mismatch", err)
	}
	if !sameJSON(`{"b":2,"a":1}`, `{"a":1,"b":2}`) || sameJSON("not-json", `{}`) {
		t.Fatal("sameJSON did not preserve canonical JSON comparison")
	}
	if jsonEqual(func() {}, map[string]any{}) {
		t.Fatal("non-JSON value compared equal")
	}
}

func TestStrictEvidenceRejectsMalformedWireAndSkipsPostTerminalFailure(t *testing.T) {
	for _, test := range []struct {
		name  string
		event recording.Event
	}{
		{name: "empty payload", event: recording.Event{RuntimeKind: providerWireSend, Clean: true}},
		{name: "unclean send", event: recording.Event{RuntimeKind: providerWireSend, Payload: wireEnvelope(t, `{"type":"session.update"}`), Clean: false}},
		{name: "malformed envelope", event: recording.Event{RuntimeKind: providerWireSend, Payload: []byte("{"), Clean: true}},
		{name: "invalid message type", event: recording.Event{RuntimeKind: providerWireReceive, Payload: []byte(`{"message_type":0,"payload":{"type":"session.created"}}`), Clean: true}},
		{name: "failed receive before terminal", event: recording.Event{RuntimeKind: providerWireReceive, Payload: []byte(`{"message_type":0}`), Clean: false}},
		{name: "empty wire payload", event: recording.Event{RuntimeKind: providerWireSend, Payload: []byte(`{"message_type":1}`), Clean: true}},
		{name: "missing payload type", event: recording.Event{RuntimeKind: providerWireSend, Payload: wireEnvelope(t, `{}`), Clean: true}},
		{name: "negative timestamp", event: recording.Event{RuntimeKind: providerWireSend, Payload: wireEnvelope(t, `{"type":"session.update"}`), Clean: true, ElapsedNS: -1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			builder := newEvidenceBuilder()
			if err := builder.consumeWire(test.event); !errors.Is(err, publicreplay.ErrBundleIncomplete) {
				t.Fatalf("consumeWire error = %v, want incomplete", err)
			}
		})
	}

	binary := newEvidenceBuilder()
	if err := binary.consumeWire(recording.Event{RuntimeKind: providerWireSend, Payload: []byte(`{"message_type":2,"binary_payload":"AQI="}`), Clean: true}); err != nil {
		t.Fatalf("binary wire = %v", err)
	}
	if len(binary.wires) != 1 || binary.wires[0].Type != "binary" {
		t.Fatalf("binary wire = %+v", binary.wires)
	}

	skipped := newEvidenceBuilder()
	skipped.sawResponseDone = true
	if err := skipped.consumeWire(recording.Event{RuntimeKind: providerWireReceive, Payload: []byte(`{"message_type":0}`), Clean: false}); err != nil {
		t.Fatalf("post-terminal failed receive = %v, want ignored", err)
	}

	modelMismatch := newEvidenceBuilder()
	modelMismatch.createdModel = "first"
	if err := modelMismatch.observeWireMetadata(recording.Event{RuntimeKind: providerWireReceive}, "session.created", []byte(`{"session":{"model":"second"}}`)); !errors.Is(err, publicreplay.ErrBundleMismatch) {
		t.Fatalf("created model mismatch = %v, want mismatch", err)
	}
}

type replayMediaTestSession struct {
	media   *sharedaudio.SessionMedia
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
}

func (s *replayMediaTestSession) Send(context.Context, messages.StreamMessage) bool { return true }

func (s *replayMediaTestSession) RTCMedia() sharedaudio.MediaEndpoints {
	if s.media == nil {
		return sharedaudio.MediaEndpoints{}
	}
	return s.media.Endpoints()
}

func (s *replayMediaTestSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	if s.receive == nil {
		s.receive = messages.NewTypedBuffer[messages.StreamMessage](1)
	}
	return s.receive
}

func (s *replayMediaTestSession) Done() <-chan struct{} {
	if s.done == nil {
		s.done = make(chan struct{})
	}
	return s.done
}

func (s *replayMediaTestSession) Close() error {
	if s.media != nil {
		return s.media.Close()
	}
	return nil
}
