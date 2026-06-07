package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
)

const InteractionFixtureVersion = "gateway.interaction.v1"

// InteractionFixture is a credential-free replay envelope for normalized
// interaction events.
type InteractionFixture struct {
	Version string             `json:"version"`
	Request InteractionRequest `json:"request,omitempty"`
	Events  []InteractionEvent `json:"events"`
}

// InteractionFixtureValidationError describes the first invalid fixture field.
type InteractionFixtureValidationError struct {
	File      string
	FieldPath string
	Reason    string
}

func (e InteractionFixtureValidationError) Error() string {
	if e.File != "" {
		return fmt.Sprintf("%s: %s: %s", e.File, e.FieldPath, e.Reason)
	}
	return fmt.Sprintf("%s: %s", e.FieldPath, e.Reason)
}

// LoadInteractionFixture loads and validates a normalized interaction fixture.
func LoadInteractionFixture(path string) (InteractionFixture, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return InteractionFixture{}, fmt.Errorf("read interaction fixture: %w", err)
	}
	return DecodeInteractionFixture(path, data)
}

// DecodeInteractionFixture decodes and validates a normalized interaction fixture.
func DecodeInteractionFixture(file string, data []byte) (InteractionFixture, error) {
	var fixture InteractionFixture
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&fixture); err != nil {
		return InteractionFixture{}, InteractionFixtureValidationError{
			File:      file,
			FieldPath: "$",
			Reason:    fmt.Sprintf("must be valid JSON: %v", err),
		}
	}
	if err := ValidateInteractionFixture(file, fixture); err != nil {
		return InteractionFixture{}, err
	}
	return fixture, nil
}

// ValidateInteractionFixture validates fixture structure and event payloads.
func ValidateInteractionFixture(file string, fixture InteractionFixture) error {
	if fixture.Version == "" {
		return interactionFixtureError(file, "version", "is required")
	}
	if fixture.Version != InteractionFixtureVersion {
		return interactionFixtureError(file, "version", fmt.Sprintf("must be %q", InteractionFixtureVersion))
	}
	if len(fixture.Events) == 0 {
		return interactionFixtureError(file, "events", "must contain at least one event")
	}

	var interactionID string
	for i, event := range fixture.Events {
		field := fmt.Sprintf("events[%d]", i)
		if event.InteractionID == "" {
			return interactionFixtureError(file, field+".interactionId", "is required")
		}
		if interactionID == "" {
			interactionID = event.InteractionID
		} else if event.InteractionID != interactionID {
			return interactionFixtureError(file, field+".interactionId", fmt.Sprintf("must match %q", interactionID))
		}

		wantSequence := int64(i + 1)
		if event.Sequence != wantSequence {
			return interactionFixtureError(file, field+".sequence", fmt.Sprintf("must be %d", wantSequence))
		}
		if event.Type == "" {
			return interactionFixtureError(file, field+".type", "is required")
		}
		if err := validateInteractionEventPayload(file, field, event); err != nil {
			return err
		}
	}
	return nil
}

func validateInteractionEventPayload(file, field string, event InteractionEvent) error {
	switch event.Type {
	case InteractionEventStart, InteractionEventEnd:
		return nil
	case InteractionEventTextDelta:
		if event.TextDelta == nil {
			return interactionFixtureError(file, field+".textDelta", "is required for text.delta events")
		}
	case InteractionEventFinalMessage:
		if event.FinalMessage == nil {
			return interactionFixtureError(file, field+".finalMessage", "is required for message.final events")
		}
		if event.FinalMessage.Role == "" {
			return interactionFixtureError(file, field+".finalMessage.role", "is required")
		}
	case InteractionEventToolCallRequest:
		if event.ToolCall == nil {
			return interactionFixtureError(file, field+".toolCall", "is required for tool.call.request events")
		}
		if event.ToolCall.ID == "" {
			return interactionFixtureError(file, field+".toolCall.id", "is required")
		}
		if event.ToolCall.Name == "" {
			return interactionFixtureError(file, field+".toolCall.name", "is required")
		}
	case InteractionEventToolResultAccepted:
		if event.ToolResult == nil {
			return interactionFixtureError(file, field+".toolResult", "is required for tool.result.accepted events")
		}
		if event.ToolResult.ToolCallID == "" {
			return interactionFixtureError(file, field+".toolResult.toolCallId", "is required")
		}
	case InteractionEventUsage:
		if event.Usage == nil {
			return interactionFixtureError(file, field+".usage", "is required for usage events")
		}
	case InteractionEventError:
		if event.Error == nil {
			return interactionFixtureError(file, field+".error", "is required for error events")
		}
		if event.Error.Code == "" {
			return interactionFixtureError(file, field+".error.code", "is required")
		}
		if event.Error.Message == "" {
			return interactionFixtureError(file, field+".error.message", "is required")
		}
	case InteractionEventCancellation:
		if event.Cancellation == nil {
			return interactionFixtureError(file, field+".cancellation", "is required for cancellation events")
		}
	default:
		return interactionFixtureError(file, field+".type", fmt.Sprintf("unsupported event type %q", event.Type))
	}
	return nil
}

func interactionFixtureError(file, fieldPath, reason string) InteractionFixtureValidationError {
	return InteractionFixtureValidationError{
		File:      file,
		FieldPath: fieldPath,
		Reason:    reason,
	}
}

// InteractionFixtureReplayer emits the validated event sequence from an
// InteractionFixture without calling provider code.
type InteractionFixtureReplayer struct {
	fixture InteractionFixture
}

// NewInteractionFixtureReplayer creates a deterministic replayer from a decoded fixture.
func NewInteractionFixtureReplayer(fixture InteractionFixture) (*InteractionFixtureReplayer, error) {
	if err := ValidateInteractionFixture("", fixture); err != nil {
		return nil, err
	}
	cloned, err := cloneInteractionFixture(fixture)
	if err != nil {
		return nil, fmt.Errorf("clone interaction fixture: %w", err)
	}
	return &InteractionFixtureReplayer{fixture: cloned}, nil
}

// NewInteractionFixtureReplayerFromFile loads a fixture file and creates a deterministic replayer.
func NewInteractionFixtureReplayerFromFile(path string) (*InteractionFixtureReplayer, error) {
	fixture, err := LoadInteractionFixture(path)
	if err != nil {
		return nil, err
	}
	return NewInteractionFixtureReplayer(fixture)
}

// Fixture returns a cloned copy of the validated fixture envelope.
func (r *InteractionFixtureReplayer) Fixture() InteractionFixture {
	cloned, err := cloneInteractionFixture(r.fixture)
	if err != nil {
		return InteractionFixture{}
	}
	return cloned
}

// Replay streams a fresh clone of the fixture events in fixture order.
func (r *InteractionFixtureReplayer) Replay(ctx context.Context) <-chan InteractionEvent {
	out := make(chan InteractionEvent)
	events, err := cloneInteractionEvents(r.fixture.Events)
	if err != nil {
		close(out)
		return out
	}

	go func() {
		defer close(out)
		for _, event := range events {
			select {
			case <-ctx.Done():
				return
			case out <- event:
			}
		}
	}()
	return out
}

func cloneInteractionFixture(fixture InteractionFixture) (InteractionFixture, error) {
	var cloned InteractionFixture
	data, err := json.Marshal(fixture)
	if err != nil {
		return InteractionFixture{}, err
	}
	if err := json.Unmarshal(data, &cloned); err != nil {
		return InteractionFixture{}, err
	}
	return cloned, nil
}

func cloneInteractionEvents(events []InteractionEvent) ([]InteractionEvent, error) {
	var cloned []InteractionEvent
	data, err := json.Marshal(events)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &cloned); err != nil {
		return nil, err
	}
	return cloned, nil
}
