package live

import (
	"errors"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"sort"
	"strings"
)

const continuationStatusFailed = "failed"

type liveToolContinuation struct {
	callID                string
	name                  string
	resultAccepted        bool
	continuationRequested bool
	toolOutputObserved    bool
	toolResponseComplete  bool
	outputObserved        bool
	pendingTerminal       bool
	status                string
	code                  string
	detail                string
}

func (h *handle) observeToolLifecycle(msg messages.StreamMessage) (error, bool) {
	if h == nil {
		return nil, false
	}
	if msg.Type == messages.StreamTypeToolCallStart || msg.Type == messages.StreamTypeToolCallDelta || msg.Type == messages.StreamTypeToolCallEnd {
		h.observeProviderToolCall(msg)
		if msg.Type != messages.StreamTypeToolCallStart && msg.Type != messages.StreamTypeToolCallDelta {
			h.markContinuationOutput()
		}
		return nil, false
	}
	if isContinuationOutputType(msg.Type) {
		if msg.Role == messages.RoleTool {
			h.observeToolResponseOutput(msg.ToolCallId)
			return nil, false
		}
		h.markContinuationOutput()
		return nil, false
	}
	if msg.Type == messages.StreamTypeMessageEnd {
		if msg.Role == messages.RoleTool {
			h.markToolResponseComplete()
			return h.finishDeferredToolContinuations()
		}
		return h.finishToolContinuations(msg)
	}
	return nil, false
}
func isContinuationOutputType(kind messages.StreamMessageType) bool {
	if kind == messages.StreamTypeRefusal {
		return true
	}
	name := string(kind)
	if !strings.HasSuffix(name, ".DELTA") && !strings.HasSuffix(name, ".END") {
		return false
	}
	return kind != messages.StreamTypeMessageEnd && kind != messages.StreamTypeToolCallEnd && kind != messages.StreamTypeToolCallDelta
}
func (h *handle) observeProviderToolCall(msg messages.StreamMessage) {
	callID, name := providerToolCallIdentity(msg)
	if callID == "" {
		return
	}
	h.toolMu.Lock()
	state := h.toolContinuations[callID]
	if state == nil {
		state = &liveToolContinuation{callID: callID}
		h.toolContinuations[callID] = state
	}
	if name != "" {
		state.name = name
	}
	h.toolMu.Unlock()
}
func (h *handle) observeToolResponseOutput(callID string) {
	callID = strings.TrimSpace(callID)
	if h == nil || callID == "" {
		return
	}
	h.toolMu.Lock()
	state := h.toolContinuations[callID]
	if state == nil {
		state = &liveToolContinuation{callID: callID}
		h.toolContinuations[callID] = state
	}
	state.toolOutputObserved = true
	h.toolMu.Unlock()
}
func providerToolCallIdentity(msg messages.StreamMessage) (string, string) {
	callID, name := msg.ToolCallId, ""
	switch value := msg.Value.(type) {
	case *messages.ToolCallStartValue:
		if value != nil {
			if value.ToolCallID != "" {
				callID = value.ToolCallID
			}
			name = value.Name
		}
	case *messages.ToolCallEndValue:
		if value != nil {
			if value.ToolCallID != "" {
				callID = value.ToolCallID
			}
			name = value.Name
		}
	}
	return strings.TrimSpace(callID), strings.TrimSpace(name)
}
func (h *handle) notePendingToolCallLocked(msg messages.StreamMessage) error {
	callID, _ := providerToolCallIdentity(msg)
	responseID := strings.TrimSpace(msg.ResponseID)
	if h.pendingToolCallResponses == nil {
		h.pendingToolCallResponses = make(map[string]string)
	}
	if callID != "" {
		if _, exists := h.pendingToolCallResponses[callID]; !exists && len(h.pendingToolCallResponses) >= maxPendingToolCallResponses {
			return errors.New("pending tool call response admission limit exceeded")
		}
		h.pendingToolCallResponses[callID] = responseID
	}
	h.pendingToolCalls = len(h.pendingToolCallResponses)
	return nil
}
func (h *handle) pendingToolCallsForResponse(responseID string) bool {
	responseID = strings.TrimSpace(responseID)
	if responseID == "" || h.pendingToolCallResponses == nil {
		return h.pendingToolCalls > 0
	}
	for _, callResponseID := range h.pendingToolCallResponses {
		if callResponseID == "" || callResponseID == responseID {
			return true
		}
	}
	return false
}
func (h *handle) retireCompletedToolCallsLocked() {
	h.toolMu.Lock()
	completed := make([]string, 0, len(h.toolContinuations))
	for callID, state := range h.toolContinuations {
		if state != nil && state.toolResponseComplete && strings.TrimSpace(callID) != "" {
			completed = append(completed, callID)
		}
	}
	h.toolMu.Unlock()
	for _, callID := range completed {
		delete(h.pendingToolCallResponses, callID)
	}
	h.pendingToolCalls = len(h.pendingToolCallResponses)
}
func (h *handle) beginToolResultAdmission(callID, name string, requestsContinuation bool) func() {
	callID = strings.TrimSpace(callID)
	if h == nil || callID == "" {
		return func() {}
	}
	name = strings.TrimSpace(name)
	h.toolMu.Lock()
	state := h.toolContinuations[callID]
	existed := state != nil
	if state == nil {
		state = &liveToolContinuation{callID: callID}
		h.toolContinuations[callID] = state
	}
	previousAccepted := state.resultAccepted
	previousName := state.name
	previousRequested := state.continuationRequested
	state.resultAccepted = true
	if name != "" {
		state.name = name
	}
	if requestsContinuation {
		state.continuationRequested = true
	}
	h.toolMu.Unlock()
	return func() {
		h.toolMu.Lock()
		if current := h.toolContinuations[callID]; current == state {
			current.resultAccepted = previousAccepted
			current.name = previousName
			current.continuationRequested = previousRequested
			if !existed && !current.toolOutputObserved && !current.toolResponseComplete && !current.outputObserved &&
				!current.pendingTerminal && current.status == "" && current.code == "" && current.detail == "" {
				delete(h.toolContinuations, callID)
			}
		}
		h.toolMu.Unlock()
	}
}
func (h *handle) unresolvedToolResultsError() error {
	if h == nil {
		return nil
	}
	h.toolMu.Lock()
	ids := make([]string, 0, len(h.toolContinuations))
	for callID, state := range h.toolContinuations {
		if state != nil && !state.resultAccepted && strings.TrimSpace(callID) != "" {
			ids = append(ids, callID)
		}
	}
	h.toolMu.Unlock()
	if len(ids) == 0 {
		return nil
	}
	for index, id := range ids {
		ids[index] = strings.TrimSpace(id)
	}
	sort.Strings(ids)
	return &session.LiveUnresolvedToolResultsError{CallIDs: ids}
}
func (h *handle) beginContinuationAdmission() func() {
	if h == nil {
		return func() {}
	}
	h.toolMu.Lock()
	changed := make([]*liveToolContinuation, 0, len(h.toolContinuations))
	for _, state := range h.toolContinuations {
		if state.resultAccepted && !state.continuationRequested {
			state.continuationRequested = true
			changed = append(changed, state)
		}
	}
	h.toolMu.Unlock()
	return func() {
		h.toolMu.Lock()
		for _, state := range changed {
			state.continuationRequested = false
		}
		h.toolMu.Unlock()
	}
}
func (h *handle) markContinuationOutput() {
	if h == nil {
		return
	}
	h.toolMu.Lock()
	for _, state := range h.toolContinuations {
		if state.resultAccepted && state.continuationRequested {
			state.outputObserved = true
		}
	}
	h.toolMu.Unlock()
}
func (h *handle) markToolResponseComplete() {
	if h == nil {
		return
	}
	h.toolMu.Lock()
	for _, state := range h.toolContinuations {
		if state.toolOutputObserved && !state.toolResponseComplete {
			state.toolResponseComplete = true
		}
	}
	h.toolMu.Unlock()
}
func (h *handle) finishToolContinuations(msg messages.StreamMessage) (error, bool) {
	if h == nil {
		return nil, false
	}
	status, code, detail := continuationStatus(msg)
	h.toolMu.Lock()
	image, tools, completed := h.collectContinuationFailures(status, code, detail, msg.Value)
	failure := continuationFailure(image, tools)
	if failure != nil && h.continuationErr == nil {
		h.continuationErr = failure
	}
	stored := h.continuationErr
	h.toolMu.Unlock()
	return stored, completed
}

type continuationFailures struct {
	ids      []string
	statuses map[string]string
	codes    map[string]string
	details  map[string]string
}

func continuationStatus(msg messages.StreamMessage) (string, string, string) {
	value, ok := msg.Value.(*messages.MessageEndValue)
	if !ok || value == nil {
		return "", "", ""
	}
	detail := strings.TrimSpace(value.StatusDetails)
	if detail == "" {
		detail = strings.TrimSpace(value.ProviderErrorMessage)
	}
	return strings.TrimSpace(value.Status), strings.TrimSpace(value.ProviderErrorCode), detail
}
func (h *handle) collectContinuationFailures(status, code, detail string, raw any) (continuationFailures, continuationFailures, bool) {
	image := newContinuationFailures()
	tools := newContinuationFailures()
	completed := false
	value, ok := raw.(*messages.MessageEndValue)
	if !ok {
		value = nil
	}
	for callID, state := range h.toolContinuations {
		if !state.resultAccepted || !state.continuationRequested {
			continue
		}
		if !state.toolResponseComplete {
			if providerContinuationFailed(value) {
				state.pendingTerminal = true
				state.status, state.code, state.detail = status, code, detail
			}
			continue
		}
		state.status, state.code, state.detail = status, code, detail
		if continuationFailed(value, state.outputObserved) {
			target := &tools
			if strings.EqualFold(strings.TrimSpace(state.name), "read_image") {
				target = &image
			}
			target.add(callID, status, code, detail)
			continue
		}
		completed = true
		delete(h.toolContinuations, callID)
	}
	return image, tools, completed
}
func (h *handle) finishDeferredToolContinuations() (error, bool) {
	image := newContinuationFailures()
	tools := newContinuationFailures()
	completed := false
	h.toolMu.Lock()
	for callID, state := range h.toolContinuations {
		if !state.resultAccepted || !state.continuationRequested || !state.toolResponseComplete || !state.pendingTerminal {
			continue
		}
		state.pendingTerminal = false
		if continuationFailed(&messages.MessageEndValue{Status: state.status}, state.outputObserved) {
			target := &tools
			if strings.EqualFold(strings.TrimSpace(state.name), "read_image") {
				target = &image
			}
			target.add(callID, state.status, state.code, state.detail)
			continue
		}
		completed = true
		delete(h.toolContinuations, callID)
	}
	failure := continuationFailure(image, tools)
	if failure != nil && h.continuationErr == nil {
		h.continuationErr = failure
	}
	stored := h.continuationErr
	h.toolMu.Unlock()
	return stored, completed
}
func newContinuationFailures() continuationFailures {
	return continuationFailures{
		ids: make([]string, 0), statuses: make(map[string]string),
		codes: make(map[string]string), details: make(map[string]string),
	}
}
func (f *continuationFailures) add(callID, status, code, detail string) {
	if f == nil {
		return
	}
	f.ids = append(f.ids, callID)
	f.statuses[callID], f.codes[callID], f.details[callID] = status, code, detail
}
func continuationFailure(image, tools continuationFailures) error {
	var failure error
	if len(image.ids) > 0 {
		sort.Strings(image.ids)
		failure = &session.LiveImageContinuationError{
			CallIDs: image.ids, ProviderStatuses: image.statuses,
			ProviderCodes: image.codes, ProviderDetails: image.details,
		}
	}
	if len(tools.ids) == 0 {
		return failure
	}
	sort.Strings(tools.ids)
	toolFailure := &session.LiveToolContinuationError{
		CallIDs: tools.ids, ProviderStatuses: tools.statuses,
		ProviderCodes: tools.codes, ProviderDetails: tools.details,
	}
	if failure == nil {
		return toolFailure
	}
	return errors.Join(failure, toolFailure)
}
func continuationFailed(value *messages.MessageEndValue, outputObserved bool) bool {
	if !outputObserved {
		return true
	}
	status := ""
	if value != nil {
		status = strings.ToLower(strings.TrimSpace(value.Status))
	}
	return status == continuationStatusFailed || status == "cancelled" || status == "canceled" || status == "incomplete" || status == "error"
}
