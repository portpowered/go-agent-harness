package plan

import (
	"encoding/json"
	"fmt"

	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const replayResponseCreate = "response.create"

// Tool outputs and staged images are produced by the live runtime in response
// to captured provider calls. The replay driver must not emit them a second
// time. The raw replay transport still verifies their exact outbound payloads.
type toolActions struct {
	pending    map[string]bool
	continuing bool
}

func replayDriverActions(records []gatewaytesting.CapturedSessionEvent) ([]gatewaytesting.CapturedSessionEvent, error) {
	tools := toolActions{pending: make(map[string]bool)}
	actions := make([]gatewaytesting.CapturedSessionEvent, 0, len(records))
	for _, record := range records {
		if record.Direction == gatewaytesting.DirectionServerToClient {
			if err := tools.observeCall(record); err != nil {
				return nil, err
			}
			continue
		}
		if record.Direction != gatewaytesting.DirectionClientToServer || record.Type == replaySessionUpdate {
			continue
		}
		runtimeOwned, err := tools.consume(record)
		if err != nil {
			return nil, err
		}
		if !runtimeOwned {
			actions = append(actions, record)
		}
	}
	return actions, nil
}

func (tools *toolActions) observeCall(record gatewaytesting.CapturedSessionEvent) error {
	switch record.Type {
	case "response.function_call_arguments.done", "response.output_item.added", "response.output_item.done":
	default:
		return nil
	}
	var event struct {
		CallID string `json:"call_id"`
		Item   struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
		} `json:"item"`
	}
	if err := json.Unmarshal(replayRecordPayload(record), &event); err != nil {
		return fmt.Errorf("decode provider tool call at sequence %d: %w", record.Sequence, err)
	}
	id := event.CallID
	if id == "" && event.Item.Type == "function_call" {
		id = event.Item.CallID
	}
	if id != "" {
		tools.pending[id] = true
	}
	return nil
}

type replayClientItem struct {
	Type string `json:"type"`
	Item struct {
		Type    string                `json:"type"`
		CallID  string                `json:"call_id"`
		Role    string                `json:"role"`
		Content []replayClientContent `json:"content"`
	} `json:"item"`
}

type replayClientContent struct {
	Type string `json:"type"`
}

func (tools *toolActions) consume(record gatewaytesting.CapturedSessionEvent) (bool, error) {
	switch record.Type {
	case replayResponseCreate:
		return tools.consumeResponseCreate(record)
	case replayCreateItem:
		return tools.consumeCreateItem(record)
	case replayTruncateItem:
		// Realtime interruption owns this wire action: the provider's VAD event
		// causes the live media boundary to truncate the currently playing item.
		// The strict replay transport still validates the complete payload when
		// the runtime emits it; the self-driving plan must only avoid scheduling
		// it as a second caller action.
		if err := replayPayloadType(record, replayTruncateItem); err != nil {
			return false, err
		}
		return true, nil
	default:
		return false, nil
	}
}

func (tools *toolActions) consumeResponseCreate(record gatewaytesting.CapturedSessionEvent) (bool, error) {
	if tools.continuing {
		if err := replayPayloadType(record, replayResponseCreate); err != nil {
			return false, err
		}
		tools.continuing = false
		return true, nil
	}
	if len(tools.pending) > 0 {
		// A response.create before every pending tool result has been
		// accepted cannot be reproduced by the live runtime. Keep the
		// capture available for caller-driven replay so the strict wire
		// replayer can report the actual outbound mismatch instead of
		// turning it into an audio-plan boundary error.
		return false, fmt.Errorf("%w: %s at sequence %d precedes pending tool results", errSelfDrivingPlanUnavailable, replayResponseCreate, record.Sequence)
	}
	return false, nil
}

func (tools *toolActions) consumeCreateItem(record gatewaytesting.CapturedSessionEvent) (bool, error) {
	var event replayClientItem
	if err := json.Unmarshal(replayRecordPayload(record), &event); err != nil {
		return false, fmt.Errorf("decode client item at sequence %d: %w", record.Sequence, err)
	}
	if event.Type != record.Type {
		return false, fmt.Errorf("client item payload type mismatch at sequence %d", record.Sequence)
	}
	if event.Item.Type == "function_call_output" {
		return tools.consumeFunctionCallOutput(record, event.Item.CallID)
	}
	if !tools.continuing || event.Item.Type != "message" || event.Item.Role != "user" || len(event.Item.Content) == 0 {
		return false, nil
	}
	return replayClientItemContainsOnlyImages(event.Item.Content), nil
}

func (tools *toolActions) consumeFunctionCallOutput(record gatewaytesting.CapturedSessionEvent, callID string) (bool, error) {
	if !tools.pending[callID] {
		// The raw replay transport owns exact outbound validation. A
		// malformed, duplicate, or mismatched result must therefore
		// remain a caller-driven capture rather than being rejected while
		// deriving the optional self-driving plan.
		return false, fmt.Errorf("%w: orphan tool output at sequence %d", errSelfDrivingPlanUnavailable, record.Sequence)
	}
	delete(tools.pending, callID)
	tools.continuing = true
	return true, nil
}

func replayClientItemContainsOnlyImages(content []replayClientContent) bool {
	for _, item := range content {
		if item.Type != "input_image" {
			return false
		}
	}
	return true
}
