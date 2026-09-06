package participants

import (
	"context"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"testing"
)

func TestModelRunner_ForwardSessionEventReportsProviderBoundaryOutcomes(t *testing.T) {
	ctx := context.Background()

	t.Run("ordinary rejection remains best effort", func(t *testing.T) {
		runner := NewSessionModelRunner(nil, 8, nil)
		session := newRejectingStreamSession()

		failure, deferred, accepted := runner.forwardSessionEvent(ctx, session, messages.StreamMessage{
			Type: messages.StreamTypeTextDelta,
		})
		if failure.Type != "" || deferred || accepted {
			t.Fatalf("ordinary rejection outcome = (%#v, %v, %v), want zero/false/false", failure, deferred, accepted)
		}
		if _, ok := runner.DeltaOutbox.Read(); ok {
			t.Fatal("ordinary rejection emitted an error delta")
		}
	})

	t.Run("result rejection is deferred", func(t *testing.T) {
		runner := NewSessionModelRunner(nil, 8, nil)
		session := newRejectingStreamSession()

		failure, deferred, accepted := runner.forwardSessionEvent(ctx, session, messages.StreamMessage{
			Type:  messages.StreamTypeToolCallEnd,
			Value: messages.NewToolCallEndValue("call-result", "date", "today"),
		})
		if failure.Type != messages.StreamTypeError || !deferred || accepted {
			t.Fatalf("result rejection outcome = (%#v, %v, %v), want ERROR/true/false", failure, deferred, accepted)
		}
		value, ok := failure.Value.(*messages.ErrorValue)
		if !ok {
			t.Fatalf("failure value = %T, want *messages.ErrorValue", failure.Value)
		}
		if value.Classification != "unresolved_tool_result" || !contains(value.Message, "call-result") {
			t.Fatalf("failure = %+v, want unresolved call-result", value)
		}

		runner.flushPendingSessionSendErrors(ctx, []messages.StreamMessage{failure})
		forwarded, ok := runner.DeltaOutbox.Read()
		if !ok || forwarded.Type != messages.StreamTypeError {
			t.Fatalf("flushed failure = %#v, ok=%v; want ERROR", forwarded, ok)
		}
	})

	t.Run("continuation rejection is emitted immediately", func(t *testing.T) {
		runner := NewSessionModelRunner(nil, 8, nil)
		session := newRejectingStreamSession()

		failure, deferred, accepted := runner.forwardSessionEvent(ctx, session, messages.StreamMessage{
			Type: messages.StreamTypeResponseCreate,
		})
		if failure.Type != "" || deferred || accepted {
			t.Fatalf("continuation rejection return = (%#v, %v, %v), want zero/false/false", failure, deferred, accepted)
		}
		forwarded, ok := runner.DeltaOutbox.Read()
		if !ok || forwarded.Type != messages.StreamTypeError {
			t.Fatalf("continuation failure = %#v, ok=%v; want ERROR", forwarded, ok)
		}
		value, ok := forwarded.Value.(*messages.ErrorValue)
		if !ok {
			t.Fatalf("continuation failure value = %T, want *messages.ErrorValue", forwarded.Value)
		}
		if value.Classification != "unresolved_tool_continuation" || !contains(value.Message, "not requested") {
			t.Fatalf("continuation failure = %+v, want unresolved continuation", value)
		}
	})

	t.Run("session update rejection is emitted immediately", func(t *testing.T) {
		runner := NewSessionModelRunner(nil, 8, nil)
		session := newRejectingStreamSession()

		failure, deferred, accepted := runner.forwardSessionEvent(ctx, session, messages.StreamMessage{
			Type: messages.StreamTypeSessionUpdate,
			Value: messages.NewSessionUpdateValue(&messages.SessionUpdateConfig{
				Tools: []messages.ToolDefinition{{Name: "current_page_tool"}},
			}),
		})
		if failure.Type != "" || deferred || accepted {
			t.Fatalf("session update rejection return = (%#v, %v, %v), want zero/false/false", failure, deferred, accepted)
		}
		forwarded, ok := runner.DeltaOutbox.Read()
		if !ok || forwarded.Type != messages.StreamTypeError {
			t.Fatalf("session update failure = %#v, ok=%v; want ERROR", forwarded, ok)
		}
		value, ok := forwarded.Value.(*messages.ErrorValue)
		if !ok || value.Classification != "unresolved_session_update" || !contains(value.Message, "tool definition update") {
			t.Fatalf("session update failure = %#v, want unresolved session update", forwarded.Value)
		}
	})
}

func TestModelRunner_ResponseCancelStateTracksAdmissionOutcome(t *testing.T) {
	for _, test := range []struct {
		name          string
		outcome       messages.SessionSendOutcome
		wantSent      bool
		wantForwarded int
	}{
		{
			name:          "accepted cancel",
			outcome:       messages.SessionSendOutcome{Status: messages.SessionSendSucceeded},
			wantSent:      true,
			wantForwarded: 1,
		},
		{
			name:          "rejected cancel",
			outcome:       messages.SessionSendOutcome{Status: messages.SessionSendBufferFull},
			wantSent:      false,
			wantForwarded: 0,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := &outcomeRecordingSession{
				recordingSession: newRecordingSession(),
				outcomes: map[messages.StreamMessageType]messages.SessionSendOutcome{
					messages.StreamTypeResponseCancel: test.outcome,
				},
			}
			runner := NewSessionModelRunner(nil, 8, nil)
			state := &sessionRunState{
				responseInFlight:  true,
				currentResponseID: "response-1",
			}
			state.ensureMaps()

			runner.forwardQueuedSessionEvent(context.Background(), session, state, messages.StreamMessage{
				Type: messages.StreamTypeResponseCancel,
			})

			if state.responseCancelSent != test.wantSent {
				t.Fatalf("responseCancelSent = %t, want %t", state.responseCancelSent, test.wantSent)
			}
			if got := len(session.sentMessages()); got != test.wantForwarded {
				t.Fatalf("provider cancel messages = %d, want %d", got, test.wantForwarded)
			}
		})
	}
}

func TestModelRunner_DrainSessionAudioForwardsQueuedFrames(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)
	runner.UserAudioInbox <- []byte{4, 5, 6}

	responseInFlight := false
	responseCancelSent := false
	runner.drainSessionAudio(context.Background(), session, &responseInFlight, &responseCancelSent)

	sent := session.sentMessages()
	if len(sent) != 1 || sent[0].Type != messages.StreamTypeAudioDelta {
		t.Fatalf("drained sends = %#v, want one AUDIO.DELTA", sent)
	}
	value, ok := sent[0].Value.(*messages.AudioDeltaValue)
	if !ok || string(value.Content) != string([]byte{4, 5, 6}) {
		t.Fatalf("drained audio = %#v, want original frame", sent[0].Value)
	}

	close(runner.UserAudioInbox)
	runner.drainSessionAudio(context.Background(), session, &responseInFlight, &responseCancelSent)
}

func TestModelRunner_SendLatestUserTextPicksNewestUserText(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)
	ctx := context.Background()

	runner.sendLatestUserText(ctx, session, messages.InferenceRequest{
		Messages: []messages.Message{
			messages.NewTextMessage(messages.RoleUser, "first"),
			messages.NewTextMessage(messages.RoleAssistant, "reply"),
			messages.NewTextMessage(messages.RoleUser, "second"),
		},
	})

	sent := session.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(sent))
	}
	if sent[0].Type != messages.StreamTypeTextDelta {
		t.Fatalf("sent type = %s, want %s", sent[0].Type, messages.StreamTypeTextDelta)
	}
	value, ok := sent[0].Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("sent value = %T, want *messages.TextDeltaValue", sent[0].Value)
	}
	if value.Content != "second" {
		t.Fatalf("text = %q, want %q", value.Content, "second")
	}
}

func TestModelRunner_SendLatestUserTextSkipsTextlessAndNonUserMessages(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)
	ctx := context.Background()

	runner.sendLatestUserText(ctx, session, messages.InferenceRequest{
		Messages: []messages.Message{
			messages.NewTextMessage(messages.RoleAssistant, "only assistant"),
		},
	})
	if got := len(session.sentMessages()); got != 0 {
		t.Fatalf("assistant-only request sent %d messages, want 0", got)
	}

	runner.sendLatestUserText(ctx, session, messages.InferenceRequest{
		Messages: []messages.Message{
			messages.NewTextMessage(messages.RoleUser, "real"),
			{Role: messages.RoleUser},
		},
	})
	// The newest user message has no text part, so the search stops before
	// reaching the older non-empty one: nothing is sent.
	sent := session.sentMessages()
	if len(sent) != 0 {
		t.Fatalf("textless user message must stop the search without sending; got %d sends", len(sent))
	}

	runner.sendLatestUserText(ctx, session, messages.InferenceRequest{
		Messages: []messages.Message{
			messages.NewTextMessage(messages.RoleAssistant, "noise"),
			messages.NewTextMessage(messages.RoleUser, "real"),
		},
	})
	if sent = session.sentMessages(); len(sent) != 1 {
		t.Fatalf("sent %d messages, want 1 for older user text after non-user skip", len(sent))
	}
}

func TestModelRunner_SendLatestUserTextPreservesExplicitEmptyTextPart(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)

	runner.sendLatestUserText(context.Background(), session, messages.InferenceRequest{
		Messages: []messages.Message{messages.NewTextMessage(messages.RoleUser, "")},
	})

	sent := session.sentMessages()
	if len(sent) != 1 || sent[0].Type != messages.StreamTypeTextDelta {
		t.Fatalf("explicit empty text sends = %#v, want one TEXT.DELTA", sent)
	}
	value, ok := sent[0].Value.(*messages.TextDeltaValue)
	if !ok || value.Content != "" {
		t.Fatalf("explicit empty text value = %#v, want empty TextDeltaValue", sent[0].Value)
	}
}

func TestModelRunner_SendLatestUserTextRequestsResponseForAudioOnlyToolResult(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)

	runner.sendLatestUserText(context.Background(), session, messages.InferenceRequest{
		Messages: []messages.Message{
			{Role: messages.RoleUser},
			{
				Role:      messages.RoleAssistant,
				ToolCalls: []messages.ToolCall{{ID: "call-read-file", Name: "read_file"}},
			},
			{
				Role:         messages.RoleTool,
				ToolCallID:   "call-read-file",
				ContentParts: []messages.ContentPart{messages.TextPart{Text: "file not found"}},
			},
		},
	})

	sent := session.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("sent %d messages, want one explicit response request", len(sent))
	}
	if sent[0].Type != messages.StreamTypeResponseCreate {
		t.Fatalf("sent type = %s, want %s", sent[0].Type, messages.StreamTypeResponseCreate)
	}
	if _, ok := sent[0].Value.(*messages.ResponseCreateValue); !ok {
		t.Fatalf("sent value = %T, want *messages.ResponseCreateValue", sent[0].Value)
	}
}

func TestModelRunner_SendLatestSessionToolResultsUsesCompleteMessagePath(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)
	ctx := context.Background()
	imageBytes := []byte{0x89, 'P', 'N', 'G'}

	runner.sendLatestUserText(ctx, session, messages.InferenceRequest{
		Messages: []messages.Message{
			messages.NewTextMessage(messages.RoleUser, "inspect the image"),
			{
				Role:         messages.RoleAssistant,
				ToolCalls:    []messages.ToolCall{{ID: "call-read-image", Name: "read_image"}},
				ContentParts: []messages.ContentPart{messages.TextPart{Text: "Reading the image."}},
			},
			{
				Role:       messages.RoleTool,
				ToolCallID: "call-read-image",
				ContentParts: []messages.ContentPart{
					messages.TextPart{Text: "image attached"},
					messages.ImagePart{Bytes: imageBytes, MediaType: "image/png"},
				},
			},
		},
	})

	if got := len(session.sentMessages()); got != 0 {
		t.Fatalf("tool-result request sent %d streaming messages, want complete-message path", got)
	}
	sent := session.completeMessages()
	if len(sent) != 1 {
		t.Fatalf("complete-message sends = %d, want 1", len(sent))
	}
	if sent[0].Role != messages.RoleTool || sent[0].ToolCallID != "call-read-image" {
		t.Fatalf("complete message identity = %#v, want tool call-read-image", sent[0])
	}
	if len(sent[0].ContentParts) != 2 {
		t.Fatalf("complete message parts = %d, want text and image", len(sent[0].ContentParts))
	}
	gotImage, ok := sent[0].ContentParts[1].(messages.ImagePart)
	if !ok || gotImage.MediaType != "image/png" || string(gotImage.Bytes) != string(imageBytes) {
		t.Fatalf("complete image part = %#v, want original PNG bytes", sent[0].ContentParts[1])
	}
}

func TestModelRunner_SendLatestSessionToolResultsPreservesBatchOrder(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)
	ctx := context.Background()
	first := messages.Message{Role: messages.RoleTool, ToolCallID: "call-1", ContentParts: []messages.ContentPart{
		messages.TextPart{Text: "one"},
		messages.ImagePart{Bytes: []byte("first image"), MediaType: "image/png"},
	}}
	second := messages.Message{Role: messages.RoleTool, ToolCallID: "call-2", ContentParts: []messages.ContentPart{
		messages.TextPart{Text: "two"},
		messages.ImagePart{Bytes: []byte("second image"), MediaType: "image/png"},
	}}

	runner.sendLatestUserText(ctx, session, messages.InferenceRequest{Messages: []messages.Message{first, second}})

	deferred := session.completeMessagesWithoutResponse()
	if len(deferred) != 1 || deferred[0].ToolCallID != "call-1" {
		t.Fatalf("deferred tool results = %#v, want call-1", deferred)
	}
	sent := session.completeMessages()
	if len(sent) != 1 || sent[0].ToolCallID != "call-2" {
		t.Fatalf("final tool results = %#v, want call-2", sent)
	}
}

func TestModelRunner_SendLatestSessionToolResultsMixedBatchUsesOneCompleteResponse(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)
	ctx := context.Background()
	imageBytes := []byte("image bytes")
	history := []messages.Message{
		messages.NewTextMessage(messages.RoleUser, "inspect both results"),
		{
			Role: messages.RoleAssistant,
			ToolCalls: []messages.ToolCall{
				{ID: "call-text", Name: "text_tool"},
				{ID: "call-image", Name: "read_image"},
			},
		},
		{
			Role:         messages.RoleTool,
			ToolCallID:   "call-text",
			ContentParts: []messages.ContentPart{messages.TextPart{Text: "text result"}},
		},
		{
			Role:       messages.RoleTool,
			ToolCallID: "call-image",
			ContentParts: []messages.ContentPart{
				messages.TextPart{Text: "image result"},
				messages.ImagePart{Bytes: imageBytes, MediaType: "image/png"},
			},
		},
	}

	runner.sendLatestUserText(ctx, session, messages.InferenceRequest{Messages: history})

	if sent := session.sentMessages(); len(sent) != 0 {
		t.Fatalf("mixed rich batch sent %d flat messages, want complete-message path only", len(sent))
	}
	deferred := session.completeMessagesWithoutResponse()
	if len(deferred) != 1 || deferred[0].ToolCallID != "call-text" {
		t.Fatalf("deferred tool results = %#v, want one call-text result", deferred)
	}
	complete := session.completeMessages()
	if len(complete) != 1 || complete[0].ToolCallID != "call-image" {
		t.Fatalf("response-requesting tool results = %#v, want one call-image result", complete)
	}
	imageParts := 0
	for _, part := range complete[0].ContentParts {
		if image, ok := part.(messages.ImagePart); ok {
			imageParts++
			if image.MediaType != "image/png" || string(image.Bytes) != string(imageBytes) {
				t.Fatalf("image part = %#v, want original PNG content", image)
			}
		}
	}
	if imageParts != 1 {
		t.Fatalf("image parts in final result = %d, want 1", imageParts)
	}
}

func TestModelRunner_SendLatestSessionToolResultsFallsBackForStreamOnlySession(t *testing.T) {
	session := newStreamOnlyRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)
	ctx := context.Background()
	history := []messages.Message{
		messages.NewTextMessage(messages.RoleUser, "inspect both results"),
		{
			Role: messages.RoleAssistant,
			ToolCalls: []messages.ToolCall{
				{ID: "call-text", Name: "text_tool"},
				{ID: "call-image", Name: "read_image"},
			},
		},
		{
			Role:         messages.RoleTool,
			ToolCallID:   "call-text",
			ContentParts: []messages.ContentPart{messages.TextPart{Text: "text result"}},
		},
		{
			Role:       messages.RoleTool,
			ToolCallID: "call-image",
			ContentParts: []messages.ContentPart{
				messages.ImagePart{Bytes: []byte("image bytes"), MediaType: "image/png"},
			},
		},
	}

	runner.sendLatestUserText(ctx, session, messages.InferenceRequest{Messages: history})

	sent := session.sentMessages()
	if len(sent) != 3 {
		t.Fatalf("stream-only sends = %d, want two tool results and one response trigger", len(sent))
	}
	for index, wantID := range []string{"call-text", "call-image"} {
		if sent[index].Type != messages.StreamTypeToolCallEnd {
			t.Fatalf("sent[%d] type = %s, want TOOLCALL.END", index, sent[index].Type)
		}
		value, ok := sent[index].Value.(*messages.ToolCallEndValue)
		if !ok {
			t.Fatalf("sent[%d] value = %T, want *ToolCallEndValue", index, sent[index].Value)
		}
		if value.ToolCallID != wantID {
			t.Fatalf("sent[%d] call ID = %q, want %q", index, value.ToolCallID, wantID)
		}
	}
	first, _ := sent[0].Value.(*messages.ToolCallEndValue)
	if first.Arguments != "text result" || first.Name != "text_tool" {
		t.Fatalf("text fallback = %#v, want correlated text result", first)
	}
	second, _ := sent[1].Value.(*messages.ToolCallEndValue)
	if second.Arguments != "" || second.Name != "read_image" {
		t.Fatalf("image fallback = %#v, want correlated empty flat output", second)
	}
	if sent[2].Type != messages.StreamTypeResponseCreate {
		t.Fatalf("response trigger type = %s, want RESPONSE.CREATE", sent[2].Type)
	}
	if _, ok := sent[2].Value.(*messages.ResponseCreateValue); !ok {
		t.Fatalf("response trigger value = %T, want *ResponseCreateValue", sent[2].Value)
	}
}

func TestModelRunner_SendLatestUserTextWaitsForQueuedToolBoundary(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)
	ctx := context.Background()
	history := []messages.Message{
		messages.NewTextMessage(messages.RoleUser, "run the tool"),
		{
			Role:      messages.RoleAssistant,
			ToolCalls: []messages.ToolCall{{ID: "call-queued", Name: "queued_tool"}},
		},
		{
			Role:         messages.RoleTool,
			ToolCallID:   "call-queued",
			ContentParts: []messages.ContentPart{messages.TextPart{Text: "tool output"}},
		},
	}

	if err := runner.EnqueueSessionEvent(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeToolCallEnd,
		Value: messages.NewToolCallEndValue("call-queued", "queued_tool", "tool output"),
	}); err != nil {
		t.Fatalf("queue tool result: %v", err)
	}
	if err := runner.EnqueueSessionEvent(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeResponseCreate,
		Value: messages.NewResponseCreateValue(),
	}); err != nil {
		t.Fatalf("queue continuation: %v", err)
	}

	// The coordinator's inference request can reach the session runner while
	// these events are still in the ordered session ingress. It must wait for the queued
	// boundary instead of requesting a bare response itself.
	runner.sendLatestUserText(ctx, session, messages.InferenceRequest{Messages: history})
	if sent := session.sentMessages(); len(sent) != 0 {
		t.Fatalf("queued tool boundary was overtaken by %d direct sends: %#v", len(sent), sent)
	}

	state := newSessionResponseState()
	for i := 0; i < 2; i++ {
		select {
		case input := <-runner.sessionInputInbox:
			if input.kind != sessionInputEvent {
				t.Fatalf("queued session input kind = %d, want event", input.kind)
			}
			runner.forwardQueuedSessionEvent(ctx, session, state, input.event)
		default:
			t.Fatal("queued tool boundary was not available")
		}
	}
	if runner.hasPendingSessionToolEvents() {
		t.Fatal("tool boundary remains pending after both events were forwarded")
	}

	sent := session.sentMessages()
	if len(sent) != 2 || sent[0].Type != messages.StreamTypeToolCallEnd || sent[1].Type != messages.StreamTypeResponseCreate {
		t.Fatalf("forwarded tool boundary = %#v, want TOOLCALL.END then RESPONSE.CREATE", sent)
	}
}

func readNextSessionMessageEnd(t *testing.T, deltas *messages.TypedBuffer[messages.StreamMessage]) *messages.MessageEndValue {
	t.Helper()
	for {
		delta, ok := deltas.Read()
		if !ok {
			t.Fatal("next response ended without MESSAGE.END")
		}
		if delta.Type != messages.StreamTypeMessageEnd {
			continue
		}
		value, ok := delta.Value.(*messages.MessageEndValue)
		if !ok || value == nil {
			t.Fatalf("next MESSAGE.END value = %T, want non-nil *MessageEndValue", delta.Value)
		}
		return value
	}
}
