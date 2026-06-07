package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInteractionFixtureReplayer_ReplaysDeterministicNormalizedEvents(t *testing.T) {
	t.Parallel()

	fixture := completeInteractionFixture()
	replayer, err := NewInteractionFixtureReplayer(fixture)
	if err != nil {
		t.Fatalf("NewInteractionFixtureReplayer: %v", err)
	}

	first := collectReplayEvents(t, replayer)
	second := collectReplayEvents(t, replayer)

	if !reflect.DeepEqual(first, fixture.Events) {
		t.Fatalf("first replay mismatch:\n got: %#v\nwant: %#v", first, fixture.Events)
	}
	if !reflect.DeepEqual(second, fixture.Events) {
		t.Fatalf("second replay mismatch:\n got: %#v\nwant: %#v", second, fixture.Events)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("replay is not deterministic:\n first: %#v\nsecond: %#v", first, second)
	}
}

func TestDecodeInteractionFixture_ValidatesFirstInvalidEvent(t *testing.T) {
	t.Parallel()

	data := []byte(`{
		"version":"gateway.interaction.v1",
		"events":[
			{"interactionId":"interaction-1","sequence":1,"type":"interaction.start"},
			{"interactionId":"","sequence":2,"type":"text.delta"},
			{"interactionId":"interaction-1","sequence":4,"type":"interaction.end"}
		]
	}`)

	_, err := DecodeInteractionFixture("bad.interaction.json", data)
	requireInteractionFixtureError(t, err, "bad.interaction.json", "events[1].interactionId", "is required")
}

func TestDecodeInteractionFixture_ValidatesEventPayloads(t *testing.T) {
	t.Parallel()

	data := []byte(`{
		"version":"gateway.interaction.v1",
		"events":[
			{"interactionId":"interaction-1","sequence":1,"type":"interaction.start"},
			{"interactionId":"interaction-1","sequence":2,"type":"tool.call.request","toolCall":{"id":"call-1"}}
		]
	}`)

	_, err := DecodeInteractionFixture("missing-tool-name.interaction.json", data)
	requireInteractionFixtureError(t, err, "missing-tool-name.interaction.json", "events[1].toolCall.name", "is required")
}

func TestLoadInteractionFixture_LoadsValidatedFixtureFile(t *testing.T) {
	t.Parallel()

	fixture := completeInteractionFixture()
	path := writeInteractionFixture(t, fixture)

	loaded, err := LoadInteractionFixture(path)
	if err != nil {
		t.Fatalf("LoadInteractionFixture: %v", err)
	}
	want, err := cloneInteractionFixture(fixture)
	if err != nil {
		t.Fatalf("clone fixture: %v", err)
	}
	requireSameJSON(t, loaded, want)
}

func TestInteractionFixtureReplayer_ReplayReturnsClonedEvents(t *testing.T) {
	t.Parallel()

	fixture := completeInteractionFixture()
	replayer, err := NewInteractionFixtureReplayer(fixture)
	if err != nil {
		t.Fatalf("NewInteractionFixtureReplayer: %v", err)
	}

	first := collectReplayEvents(t, replayer)
	first[1].TextDelta.Content = "mutated"

	second := collectReplayEvents(t, replayer)
	if second[1].TextDelta.Content != "hello" {
		t.Fatalf("replay returned mutable shared event, got text delta %q", second[1].TextDelta.Content)
	}
}

func completeInteractionFixture() InteractionFixture {
	return InteractionFixture{
		Version: InteractionFixtureVersion,
		Request: InteractionRequest{
			InteractionID: "interaction-1",
			Provider:      "fixture",
			Model:         "test-model",
			Messages: []InteractionMessage{
				{
					Role:         InteractionRoleUser,
					ContentParts: []InteractionContent{{Type: InteractionContentText, Text: "hello"}},
				},
			},
		},
		Events: []InteractionEvent{
			{
				InteractionID: "interaction-1",
				Sequence:      1,
				Type:          InteractionEventStart,
				Provider:      "fixture",
				Model:         "test-model",
			},
			{
				InteractionID: "interaction-1",
				Sequence:      2,
				Type:          InteractionEventTextDelta,
				Provider:      "fixture",
				Model:         "test-model",
				Correlation:   InteractionCorrelation{MessageID: "message-1"},
				TextDelta:     &TextDeltaEvent{Content: "hello"},
			},
			{
				InteractionID: "interaction-1",
				Sequence:      3,
				Type:          InteractionEventToolCallRequest,
				Provider:      "fixture",
				Model:         "test-model",
				Correlation:   InteractionCorrelation{MessageID: "message-1", ToolCallID: "call-1"},
				ToolCall:      &InteractionToolCall{ID: "call-1", Name: "lookup", Arguments: json.RawMessage(`{"query":"hello"}`)},
			},
			{
				InteractionID: "interaction-1",
				Sequence:      4,
				Type:          InteractionEventToolResultAccepted,
				Provider:      "fixture",
				Model:         "test-model",
				Correlation:   InteractionCorrelation{ToolCallID: "call-1"},
				ToolResult:    &InteractionToolResult{ToolCallID: "call-1", Name: "lookup", Payload: json.RawMessage(`{"answer":"world"}`)},
			},
			{
				InteractionID: "interaction-1",
				Sequence:      5,
				Type:          InteractionEventFinalMessage,
				Provider:      "fixture",
				Model:         "test-model",
				Correlation:   InteractionCorrelation{MessageID: "message-2"},
				FinalMessage: &InteractionMessage{
					Role:         InteractionRoleAssistant,
					ContentParts: []InteractionContent{{Type: InteractionContentText, Text: "world"}},
				},
			},
			{
				InteractionID: "interaction-1",
				Sequence:      6,
				Type:          InteractionEventUsage,
				Provider:      "fixture",
				Model:         "test-model",
				Usage:         &InteractionUsage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5},
			},
			{
				InteractionID: "interaction-1",
				Sequence:      7,
				Type:          InteractionEventError,
				Provider:      "fixture",
				Model:         "test-model",
				Error:         &InteractionError{Code: "fixture_error", Message: "synthetic failure"},
			},
			{
				InteractionID: "interaction-1",
				Sequence:      8,
				Type:          InteractionEventCancellation,
				Provider:      "fixture",
				Model:         "test-model",
				Cancellation:  &InteractionCancellation{Reason: "fixture_cancelled", Message: "synthetic cancellation"},
			},
			{
				InteractionID: "interaction-1",
				Sequence:      9,
				Type:          InteractionEventEnd,
				Provider:      "fixture",
				Model:         "test-model",
			},
		},
	}
}

func collectReplayEvents(t *testing.T, replayer *InteractionFixtureReplayer) []InteractionEvent {
	t.Helper()

	var events []InteractionEvent
	for event := range replayer.Replay(context.Background()) {
		events = append(events, event)
	}
	return events
}

func writeInteractionFixture(t *testing.T, fixture InteractionFixture) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "fixture.interaction.json")
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func requireInteractionFixtureError(t *testing.T, err error, file, fieldPath, reason string) {
	t.Helper()

	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	var validationErr InteractionFixtureValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("expected InteractionFixtureValidationError, got %T: %v", err, err)
	}
	if validationErr.File != file || validationErr.FieldPath != fieldPath || !strings.Contains(validationErr.Reason, reason) {
		t.Fatalf("unexpected validation error:\n got: %#v\nwant file=%q field=%q reason containing %q", validationErr, file, fieldPath, reason)
	}
}

func requireSameJSON(t *testing.T, got, want any) {
	t.Helper()

	gotData, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}
	wantData, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	if string(gotData) != string(wantData) {
		t.Fatalf("JSON mismatch:\n got: %s\nwant: %s", gotData, wantData)
	}
}
