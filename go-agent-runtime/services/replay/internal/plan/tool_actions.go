package plan

import (
	"encoding/json"
	"fmt"

	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

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

func (tools *toolActions) consume(record gatewaytesting.CapturedSessionEvent) (bool, error) {
	if tools.continuing && record.Type == "response.create" {
		if err := replayPayloadType(record, "response.create"); err != nil {
			return false, err
		}
		tools.continuing = false
		return true, nil
	}
	if record.Type != replayCreateItem {
		return false, nil
	}
	var event struct {
		Type string `json:"type"`
		Item struct {
			Type    string `json:"type"`
			CallID  string `json:"call_id"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
			} `json:"content"`
		} `json:"item"`
	}
	if err := json.Unmarshal(replayRecordPayload(record), &event); err != nil {
		return false, fmt.Errorf("decode client item at sequence %d: %w", record.Sequence, err)
	}
	if event.Type != record.Type {
		return false, fmt.Errorf("client item payload type mismatch at sequence %d", record.Sequence)
	}
	if event.Item.Type == "function_call_output" {
		if !tools.pending[event.Item.CallID] {
			return false, fmt.Errorf("orphan tool output at sequence %d", record.Sequence)
		}
		delete(tools.pending, event.Item.CallID)
		tools.continuing = true
		return true, nil
	}
	if !tools.continuing || event.Item.Type != "message" || event.Item.Role != "user" || len(event.Item.Content) == 0 {
		return false, nil
	}
	for _, content := range event.Item.Content {
		if content.Type != "input_image" {
			return false, nil
		}
	}
	return true, nil
}
