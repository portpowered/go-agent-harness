package evidence

import (
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// evidenceConversation is intentionally small and transport-neutral. The
// detailed provider transcript remains in the two raw JSONL artifacts; this
// index preserves the useful turn summary used by CLI and room tooling.
//
// The projection is bounded independently of the admission queue. Once its
// budget is exhausted, the raw recorder continues to drain and the accepted
// prefix is published as a partial session log.
type evidenceConversation struct {
	responseAudio       map[string]evidenceResponseAudio
	closed              []evidenceTurn
	turn                evidenceTurn
	toolNames           map[string]string
	toolResultEventByID map[string]int
	nextToolSequence    uint64

	budget      *summaryBudget
	summaryErr  error
	summaryFull bool
}

type evidenceTurn struct {
	responseIDs    []string
	inputText      evidenceText
	responseText   evidenceText
	inputAudio     uint64
	inputOffset    uint64
	outputOffset   uint64
	outputAudio    uint64
	inputSegments  []string
	outputSegments []string
	committed      bool
	complete       bool
	toolMessage    bool
	toolEvents     []evidenceToolEvent
	reserved       bool
}

type evidenceToolEvent struct {
	Sequence   uint64 `json:"sequence"`
	Type       string `json:"type"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	Arguments  string `json:"arguments,omitempty"`
	Status     string `json:"status,omitempty"`
	Content    string `json:"content,omitempty"`

	// retainedBytes includes the fixed event and slice charges. It is not part
	// of the public JSON shape and lets a result delta be re-accounted without
	// retaining a second copy of the complete event.
	retainedBytes int64
}

func newEvidenceConversation() evidenceConversation {
	conversation := evidenceConversation{budget: &summaryBudget{}}
	conversation.bindTurn()
	return conversation
}

func (c *evidenceConversation) ensureBudget() {
	if c == nil {
		return
	}
	if c.budget == nil {
		c.budget = &summaryBudget{}
	}
	c.bindTurn()
}

func (c *evidenceConversation) bindTurn() {
	if c == nil || c.budget == nil {
		return
	}
	c.turn.inputText.budget = c.budget
	c.turn.responseText.budget = c.budget
}

func (c *evidenceConversation) failBudget(bytes int64, items int) {
	if c == nil || c.summaryFull {
		return
	}
	c.summaryFull = true
	// Prefer the item identity when both dimensions are exhausted. The error
	// is fixed text and never includes caller payloads or credentials.
	itemLimit := items > directorySummaryMaxItems-c.budget.items
	byteLimit := bytes > directorySummaryMaxBytes-c.budget.bytes
	c.summaryErr = summaryBudgetError(itemLimit && !byteLimit)
}

func (c *evidenceConversation) reserve(bytes int64, items int) bool {
	if c == nil || c.summaryFull {
		return false
	}
	c.ensureBudget()
	if c.budget.reserve(bytes, items) {
		return true
	}
	c.failBudget(bytes, items)
	return false
}

func (c *evidenceConversation) projectionError() error {
	if c == nil {
		return nil
	}
	return c.summaryErr
}

func (c *evidenceConversation) ensureTurn() bool {
	if c == nil || c.summaryFull {
		return false
	}
	c.ensureBudget()
	if c.turn.reserved {
		return true
	}
	if !c.reserve(summaryTurnFixedBytes+summaryJSONTurnFixedBytes, 1) {
		return false
	}
	c.turn.reserved = true
	return true
}

// observe builds a convenience projection. The typed transcript retains every
// admitted message, including types that have no conversation summary field.
func (c *evidenceConversation) observe(msg messages.StreamMessage, outbound bool, _ uint64) {
	if c == nil || c.summaryFull {
		return
	}
	c.ensureBudget()
	if msg.Role == messages.RoleTool {
		c.observeToolResult(msg, 0)
		return
	}
	if !outbound && msg.Role != messages.RoleUser {
		c.trackResponse(msg.ResponseID)
	}
	c.observeText(msg, outbound)
	c.observeToolCall(msg, outbound)
	if msg.Type == messages.StreamTypeMessageEnd {
		c.endMessage(outbound)
	}
}

func (c *evidenceConversation) observeToolCall(msg messages.StreamMessage, outbound bool) {
	if c == nil || c.summaryFull || outbound || (msg.Type != messages.StreamTypeToolCallStart && msg.Type != messages.StreamTypeToolCallDelta && msg.Type != messages.StreamTypeToolCallEnd) {
		return
	}
	c.turn.toolMessage = true
	if msg.Type == messages.StreamTypeToolCallEnd {
		c.observeToolCallEnd(msg)
	}
}

func (c *evidenceConversation) observeToolCallEnd(msg messages.StreamMessage) {
	value, ok := msg.Value.(*messages.ToolCallEndValue)
	if !ok || value == nil || c == nil || c.summaryFull || !c.ensureTurn() {
		return
	}
	callID := strings.TrimSpace(value.ToolCallID)
	if callID == "" {
		callID = strings.TrimSpace(msg.ToolCallId)
	}
	if c.toolNames == nil {
		c.toolNames = make(map[string]string)
	}
	if oldName, exists := c.toolNames[callID]; exists {
		if oldName != value.Name {
			oldCost := summaryMapEntryBytes + summaryCost(callID, oldName)
			newCost := summaryMapEntryBytes + summaryCost(callID, value.Name)
			if !c.replaceRetained(oldCost, newCost) {
				return
			}
			c.toolNames[callID] = value.Name
		}
	} else {
		entryCost := summaryMapEntryBytes + summaryCost(callID, value.Name)
		if !c.reserve(entryCost, 1) {
			return
		}
		c.toolNames[callID] = value.Name
	}
	c.nextToolSequence++
	event := evidenceToolEvent{Sequence: c.nextToolSequence, Type: "tool_call", ToolCallID: callID, ToolName: value.Name, Arguments: value.Arguments}
	c.appendToolEvent(event)
}

func (c *evidenceConversation) observeToolResult(msg messages.StreamMessage, _ uint64) {
	if c == nil || c.summaryFull {
		return
	}
	callID := strings.TrimSpace(msg.ToolCallId)
	if callID == "" {
		return
	}
	c.ensureToolResultIndex()
	index, exists := c.toolResultEventByID[callID]
	if !exists {
		var ok bool
		index, ok = c.startToolResult(callID)
		if !ok {
			return
		}
	}
	c.updateToolResult(&c.turn.toolEvents[index], callID, msg)
}

func (c *evidenceConversation) ensureToolResultIndex() {
	if c.toolResultEventByID == nil {
		c.toolResultEventByID = make(map[string]int)
	}
}

func (c *evidenceConversation) startToolResult(callID string) (int, bool) {
	if !c.ensureTurn() {
		return 0, false
	}
	c.nextToolSequence++
	event := evidenceToolEvent{
		Sequence: c.nextToolSequence, Type: "tool_result", ToolCallID: callID,
		ToolName: c.toolNames[callID], Status: "completed",
	}
	entryCost := summaryMapEntryBytes + summaryCost(callID) + toolEventCost(event)
	if !c.reserve(entryCost, 2) {
		return 0, false
	}
	event.retainedBytes = toolEventCost(event)
	c.turn.toolEvents = append(c.turn.toolEvents, event)
	index := len(c.turn.toolEvents) - 1
	c.toolResultEventByID[callID] = index
	return index, true
}

func (c *evidenceConversation) updateToolResult(event *evidenceToolEvent, callID string, msg messages.StreamMessage) {
	if event.ToolName == "" {
		if name := c.toolNames[callID]; name != "" {
			c.updateToolEvent(event, name, event.Arguments, event.Content)
		}
	}
	if value, ok := msg.Value.(*messages.TextDeltaValue); ok && value != nil && value.Content != "" {
		c.updateToolEvent(event, event.ToolName, event.Arguments, event.Content+value.Content)
	}
	if event.Status == "" {
		c.updateToolEvent(event, event.ToolName, event.Arguments, event.Content)
	}
}

func (c *evidenceConversation) appendToolEvent(event evidenceToolEvent) bool {
	eventCost := toolEventCost(event)
	if !c.reserve(eventCost, 1) {
		return false
	}
	event.retainedBytes = eventCost
	c.turn.toolEvents = append(c.turn.toolEvents, event)
	return true
}

func toolEventCost(event evidenceToolEvent) int64 {
	return summaryToolEventFixedBytes + summarySliceEntryBytes + summaryCost(event.Type, event.ToolCallID, event.ToolName, event.Arguments, event.Status, event.Content)
}

func (c *evidenceConversation) updateToolEvent(event *evidenceToolEvent, name, arguments, content string) bool {
	if event == nil || c == nil || c.summaryFull {
		return false
	}
	updated := *event
	updated.ToolName = name
	updated.Arguments = arguments
	updated.Content = content
	if updated.Status == "" {
		updated.Status = "completed"
	}
	newCost := toolEventCost(updated)
	if !c.replaceRetained(event.retainedBytes, newCost) {
		return false
	}
	event.ToolName = name
	event.Arguments = arguments
	event.Content = content
	event.Status = updated.Status
	event.retainedBytes = newCost
	return true
}

func (c *evidenceConversation) replaceRetained(oldBytes, newBytes int64) bool {
	if c == nil || c.summaryFull {
		return false
	}
	if newBytes > oldBytes && !c.reserve(newBytes-oldBytes, 0) {
		return false
	}
	if newBytes < oldBytes {
		c.budget.release(oldBytes-newBytes, 0)
	}
	return true
}

func (c *evidenceConversation) endMessage(outbound bool) {
	if c == nil || c.summaryFull {
		return
	}
	if outbound {
		c.turn.committed = true
		return
	}
	if c.turn.toolMessage {
		c.turn.toolMessage = false
		return
	}
	if !c.turn.observed() {
		return
	}
	c.turn.complete = true
	if !c.reserve(summarySliceEntryBytes, 1) {
		return
	}
	c.closed = append(c.closed, c.turn)
	c.resetToolResultIndex()
	c.turn = evidenceTurn{}
	c.bindTurn()
}

func (c *evidenceConversation) resetToolResultIndex() {
	if c == nil || c.toolResultEventByID == nil {
		return
	}
	for callID := range c.toolResultEventByID {
		c.budget.release(summaryMapEntryBytes+summaryCost(callID), 1)
	}
	c.toolResultEventByID = nil
}

func (c *evidenceConversation) observeAudio(input bool, bytes int, offset uint64, segment string) {
	if c == nil || c.summaryFull || bytes <= 0 || !c.ensureTurn() {
		return
	}
	if input {
		if c.turn.inputAudio == 0 {
			c.turn.inputOffset = offset
		}
		c.turn.inputAudio += uint64(bytes)
		if len(c.turn.inputSegments) == 0 {
			need := summarySliceEntryBytes + summaryCost(segment)
			if !c.reserve(need, 1) {
				return
			}
			c.turn.inputSegments = []string{segment}
		}
		return
	}
	if c.turn.outputAudio == 0 {
		c.turn.outputOffset = offset
	}
	c.turn.outputAudio += uint64(bytes)
	if len(c.turn.outputSegments) == 0 {
		need := summarySliceEntryBytes + summaryCost(segment)
		if !c.reserve(need, 1) {
			return
		}
		c.turn.outputSegments = []string{segment}
	}
}
