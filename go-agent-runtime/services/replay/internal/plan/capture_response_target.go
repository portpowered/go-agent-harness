package plan

import (
	"encoding/json"
	"fmt"
	"strings"

	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// replayResponseIdentityUnavailableError marks a capture whose response
// boundary omits identity; its zero-state value is not mutable package state.
type replayResponseIdentityUnavailableError struct{}

func (replayResponseIdentityUnavailableError) Error() string {
	return "replay response identity unavailable"
}

// replayCaptureResponseTarget derives finite completion from correlated
// provider response identities. A cancelled interruption does not satisfy the
// target when the capture contains a distinct replacement response.
func replayCaptureResponseTarget(path string, records []gatewaytesting.CapturedSessionEvent) (int, bool, error) {
	state := newReplayResponseTargetState()
	for _, record := range records {
		if record.Direction != gatewaytesting.DirectionServerToClient {
			continue
		}
		if err := state.observe(path, record); err != nil {
			return 0, false, err
		}
	}
	return state.result()
}

type replayResponseTargetState struct {
	createdResponses  map[string]struct{}
	toolResponses     map[string]struct{}
	terminalResponses map[string]struct{}
	currentResponseID string
	cancelledID       string
	replacement       bool
	sawTerminal       bool
	target            int
}

func newReplayResponseTargetState() *replayResponseTargetState {
	return &replayResponseTargetState{
		createdResponses:  make(map[string]struct{}),
		toolResponses:     make(map[string]struct{}),
		terminalResponses: make(map[string]struct{}),
	}
}

func (s *replayResponseTargetState) observe(path string, record gatewaytesting.CapturedSessionEvent) error {
	switch record.Type {
	case "response.created":
		return s.observeCreated(path, record)
	case "response.output_item.added", "response.output_item.done":
		return replayObserveToolResponse(path, record, s.currentResponseID, s.toolResponses)
	case "response.done":
		return s.observeDone(path, record)
	default:
		return nil
	}
}

func (s *replayResponseTargetState) observeCreated(path string, record gatewaytesting.CapturedSessionEvent) error {
	response, err := replayResponseRecord(path, record, false)
	if err != nil {
		return err
	}
	s.createdResponses[response.ID] = struct{}{}
	s.currentResponseID = response.ID
	if s.cancelledID != "" && response.ID != s.cancelledID {
		s.replacement = true
		s.cancelledID = ""
	}
	return nil
}

func (s *replayResponseTargetState) observeDone(path string, record gatewaytesting.CapturedSessionEvent) error {
	response, err := replayResponseRecord(path, record, true)
	if err != nil {
		return err
	}
	if err := s.validateDone(path, record, response); err != nil {
		return err
	}
	s.terminalResponses[response.ID] = struct{}{}
	s.sawTerminal = true
	if replayResponseWasCancelled(response.Status, response.StatusDetails) {
		return s.observeCancellation(path, record, response)
	}
	if err := s.validateReplacement(path, record, response); err != nil {
		return err
	}
	if _, tool := s.toolResponses[response.ID]; !tool {
		s.target++
		if s.target > maxReplayResponseTarget {
			return fmt.Errorf("live replay plan %s: response terminal target exceeds bounded limit %d at sequence %d", path, maxReplayResponseTarget, record.Sequence)
		}
	}
	s.currentResponseID = ""
	return nil
}

func (s *replayResponseTargetState) validateDone(path string, record gatewaytesting.CapturedSessionEvent, response replayResponseBoundary) error {
	if _, duplicate := s.terminalResponses[response.ID]; duplicate {
		return fmt.Errorf("live replay plan %s: duplicate response.done for response %q at sequence %d", path, response.ID, record.Sequence)
	}
	if _, created := s.createdResponses[response.ID]; !created {
		return fmt.Errorf("live replay plan %s: response.done at sequence %d has no preceding response.created for response %q", path, record.Sequence, response.ID)
	}
	return nil
}

func (s *replayResponseTargetState) observeCancellation(path string, record gatewaytesting.CapturedSessionEvent, response replayResponseBoundary) error {
	if s.cancelledID != "" {
		return fmt.Errorf("live replay plan %s: multiple cancelled response boundaries before a replacement at sequence %d", path, record.Sequence)
	}
	s.cancelledID = response.ID
	s.currentResponseID = ""
	return nil
}

func (s *replayResponseTargetState) validateReplacement(path string, record gatewaytesting.CapturedSessionEvent, response replayResponseBoundary) error {
	if s.cancelledID != "" && !s.replacement {
		return fmt.Errorf("live replay plan %s: response %q completed after cancelled response %q without a distinct response.created boundary at sequence %d", path, response.ID, s.cancelledID, record.Sequence)
	}
	return nil
}

func (s *replayResponseTargetState) result() (int, bool, error) {
	if s.target == 0 && (s.sawTerminal || s.replacement) {
		s.target = 1
	}
	return s.target, s.replacement, nil
}

type replayResponseBoundary struct {
	ID            string
	Status        string
	StatusDetails string
}

func replayResponseRecord(path string, record gatewaytesting.CapturedSessionEvent, requireStatus bool) (replayResponseBoundary, error) {
	var event struct {
		Type     string `json:"type"`
		Response *struct {
			ID            string `json:"id"`
			Status        string `json:"status"`
			StatusDetails *struct {
				Type string `json:"type"`
			} `json:"status_details"`
		} `json:"response"`
	}
	if err := json.Unmarshal(replayRecordPayload(record), &event); err != nil {
		return replayResponseBoundary{}, fmt.Errorf("live replay plan %s: decode %s at sequence %d: %w", path, record.Type, record.Sequence, err)
	}
	if event.Type != record.Type {
		return replayResponseBoundary{}, fmt.Errorf("live replay plan %s: %s at sequence %d has payload type %q", path, record.Type, record.Sequence, event.Type)
	}
	if event.Response == nil {
		return replayResponseBoundary{}, fmt.Errorf("%w: live replay plan %s: %s at sequence %d is missing its response object", replayResponseIdentityUnavailableError{}, path, record.Type, record.Sequence)
	}
	responseID := strings.TrimSpace(event.Response.ID)
	if responseID == "" {
		return replayResponseBoundary{}, fmt.Errorf("%w: live replay plan %s: %s at sequence %d is missing a response id", replayResponseIdentityUnavailableError{}, path, record.Type, record.Sequence)
	}
	statusDetails := ""
	if event.Response.StatusDetails != nil {
		statusDetails = strings.TrimSpace(event.Response.StatusDetails.Type)
	}
	if requireStatus && strings.TrimSpace(event.Response.Status) == "" && statusDetails == "" {
		return replayResponseBoundary{}, fmt.Errorf("live replay plan %s: response.done at sequence %d is missing status and status_details.type", path, record.Sequence)
	}
	return replayResponseBoundary{ID: responseID, Status: event.Response.Status, StatusDetails: statusDetails}, nil
}

func replayObserveToolResponse(path string, record gatewaytesting.CapturedSessionEvent, currentResponseID string, toolResponses map[string]struct{}) error {
	var event struct {
		Type       string `json:"type"`
		ResponseID string `json:"response_id"`
		Item       *struct {
			Type string `json:"type"`
		} `json:"item"`
	}
	if err := json.Unmarshal(replayRecordPayload(record), &event); err != nil {
		return fmt.Errorf("live replay plan %s: decode %s at sequence %d: %w", path, record.Type, record.Sequence, err)
	}
	if event.Type != record.Type {
		return fmt.Errorf("live replay plan %s: %s at sequence %d has payload type %q", path, record.Type, record.Sequence, event.Type)
	}
	if event.Item == nil {
		return fmt.Errorf("live replay plan %s: %s at sequence %d is missing its item object", path, record.Type, record.Sequence)
	}
	if event.Item.Type != "function_call" {
		return nil
	}
	responseID := strings.TrimSpace(event.ResponseID)
	if responseID == "" {
		responseID = currentResponseID
	}
	if responseID == "" {
		return fmt.Errorf("live replay plan %s: function_call at sequence %d has no response id", path, record.Sequence)
	}
	toolResponses[responseID] = struct{}{}
	return nil
}
