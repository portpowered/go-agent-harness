package evidence

import (
	"encoding/json"
	"fmt"
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

type evidenceLogEntry struct {
	TurnIndex int `json:"turn_index"`
	Input     struct {
		Text             string   `json:"text"`
		AudioOffsetBytes uint64   `json:"audio_offset_bytes"`
		AudioBytes       uint64   `json:"audio_bytes"`
		Committed        bool     `json:"committed"`
		AudioSegments    []string `json:"audio_segments,omitempty"`
	} `json:"input"`
	Response struct {
		Text             string   `json:"text"`
		Complete         bool     `json:"complete"`
		AudioOffsetBytes uint64   `json:"audio_offset_bytes"`
		AudioBytes       uint64   `json:"audio_bytes"`
		AudioSegments    []string `json:"audio_segments,omitempty"`
	} `json:"response"`
	ToolEvents []evidenceToolEvent `json:"tool_events,omitempty"`
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
	if c.toolResultEventByID == nil {
		c.toolResultEventByID = make(map[string]int)
	}
	index, exists := c.toolResultEventByID[callID]
	if !exists {
		if !c.ensureTurn() {
			return
		}
		c.nextToolSequence++
		event := evidenceToolEvent{
			Sequence: c.nextToolSequence, Type: "tool_result", ToolCallID: callID,
			ToolName: c.toolNames[callID], Status: "completed",
		}
		entryCost := summaryMapEntryBytes + summaryCost(callID) + toolEventCost(event)
		if !c.reserve(entryCost, 2) {
			return
		}
		event.retainedBytes = toolEventCost(event)
		c.turn.toolEvents = append(c.turn.toolEvents, event)
		index = len(c.turn.toolEvents) - 1
		c.toolResultEventByID[callID] = index
	}
	event := &c.turn.toolEvents[index]
	if event.ToolName == "" {
		if name := c.toolNames[callID]; name != "" {
			if !c.updateToolEvent(event, name, event.Arguments, event.Content) {
				return
			}
		}
	}
	if value, ok := msg.Value.(*messages.TextDeltaValue); ok && value != nil && value.Content != "" {
		if !c.updateToolEvent(event, event.ToolName, event.Arguments, event.Content+value.Content) {
			return
		}
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

func (c *evidenceConversation) observeText(msg messages.StreamMessage, outbound bool) {
	if c == nil || c.summaryFull {
		return
	}
	switch value := msg.Value.(type) {
	case *messages.TextDeltaValue:
		if value != nil && msg.Type == messages.StreamTypeTextDelta {
			c.appendText(outbound, value.Content)
		}
	case *messages.TranscriptDeltaValue:
		if value != nil && !outbound && msg.Type == messages.StreamTypeTranscriptDelta {
			c.appendTranscript(msg.Role == messages.RoleUser, value.ItemID, value.Text, false)
		}
	case *messages.TranscriptEndValue:
		if value != nil && !outbound && msg.Type == messages.StreamTypeTranscriptEnd {
			c.appendTranscript(msg.Role == messages.RoleUser, value.ItemID, value.FullText, true)
		}
	}
}

func (c *evidenceConversation) appendText(input bool, text string) {
	if c == nil || c.summaryFull || text == "" || !c.ensureTurn() {
		return
	}
	target := &c.turn.responseText
	if input {
		target = &c.turn.inputText
	}
	needBytes, needItems, ok := target.append(text)
	if !ok {
		c.failBudget(needBytes, needItems)
	}
}

func (c *evidenceConversation) appendTranscript(input bool, itemID, text string, complete bool) {
	if c == nil || c.summaryFull || !c.ensureTurn() {
		return
	}
	target := &c.turn.responseText
	if input {
		target = &c.turn.inputText
	}
	needBytes, needItems, ok := target.observeTranscript(itemID, text, complete)
	if !ok {
		c.failBudget(needBytes, needItems)
	}
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

func (t evidenceTurn) observed() bool {
	return len(t.responseIDs) > 0 || t.inputText.Len() > 0 || t.responseText.Len() > 0 || t.inputAudio > 0 || t.outputAudio > 0 || len(t.toolEvents) > 0
}

func (c evidenceConversation) json() ([]byte, error) {
	turns := len(c.closed)
	if c.turn.observed() {
		turns++
	}
	if turns == 0 {
		return nil, nil
	}
	// A fixed-capacity buffer prevents append growth from doubling a large
	// summary during finalization. The normalized bundle writer makes one
	// bounded copy; both copies are bounded by this explicit limit.
	data := make([]byte, 0, int(directorySummaryMaxBytes))
	writeTurn := func(index int, turn evidenceTurn) error {
		entry := evidenceLogEntry{TurnIndex: index + 1, ToolEvents: turn.toolEvents}
		entry.Input.Text = turn.inputText.String()
		entry.Input.AudioBytes = turn.inputAudio
		entry.Input.AudioOffsetBytes = turn.inputOffset
		entry.Input.Committed = turn.committed
		entry.Input.AudioSegments = append([]string(nil), turn.inputSegments...)
		entry.Response.Text = turn.responseText.String()
		entry.Response.Complete = turn.complete
		entry.Response.AudioBytes = turn.outputAudio
		entry.Response.AudioOffsetBytes = turn.outputOffset
		entry.Response.AudioSegments = append([]string(nil), turn.outputSegments...)
		for _, id := range turn.responseIDs {
			audio := c.responseAudio[id]
			if audio.bytes == 0 {
				continue
			}
			if entry.Response.AudioBytes == 0 || audio.offset < entry.Response.AudioOffsetBytes {
				entry.Response.AudioOffsetBytes = audio.offset
			}
			entry.Response.AudioBytes += audio.bytes
			if !containsString(entry.Response.AudioSegments, audio.segment) {
				entry.Response.AudioSegments = append(entry.Response.AudioSegments, audio.segment)
			}
		}
		line, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("encode session log entry %d: %w", index+1, err)
		}
		if len(data) > int(directorySummaryMaxBytes)-len(line)-1 {
			return summaryBudgetError(false)
		}
		data = append(data, line...)
		data = append(data, '\n')
		return nil
	}
	for index, turn := range c.closed {
		if err := writeTurn(index, turn); err != nil {
			return data, err
		}
	}
	if c.turn.observed() {
		if err := writeTurn(len(c.closed), c.turn); err != nil {
			return data, err
		}
	}
	return data, nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// Full transcripts replace deltas for the same item; they are snapshots, not
// another fragment. Distinct items remain distinct within a turn projection.
// The typed transcript is authoritative for asynchronous cross-turn
// attribution.
type evidenceText struct {
	budget        *summaryBudget
	fragments     []string
	transcripts   []evidenceTranscript
	bytes         int64
	items         int
	retainedBytes int64
}

type evidenceTranscript struct {
	itemID        string
	text          evidenceText
	retainedBytes int64
}

func (t *evidenceText) append(text string) (int64, int, bool) {
	if t == nil || text == "" {
		return 0, 0, true
	}
	if t.budget == nil {
		t.budget = &summaryBudget{}
	}
	needBytes := summarySliceEntryBytes + summaryStringBytes(text)
	if !t.budget.reserve(needBytes, 1) {
		return needBytes, 1, false
	}
	t.fragments = append(t.fragments, text)
	t.bytes += int64(len(text))
	t.items++
	t.retainedBytes += needBytes
	return needBytes, 1, true
}

func (t *evidenceText) observeTranscript(itemID, text string, complete bool) (int64, int, bool) {
	if t == nil {
		return 0, 0, false
	}
	if t.budget == nil {
		t.budget = &summaryBudget{}
	}
	for index := range t.transcripts {
		if t.transcripts[index].itemID != itemID {
			continue
		}
		if complete {
			return t.transcripts[index].text.replace(text)
		}
		return t.transcripts[index].text.append(text)
	}
	entryBytes := summarySliceEntryBytes + summaryCost(itemID)
	if !t.budget.reserve(entryBytes, 1) {
		return entryBytes, 1, false
	}
	transcript := evidenceTranscript{itemID: itemID, text: evidenceText{budget: t.budget}, retainedBytes: entryBytes}
	textBytes, textItems, ok := transcript.text.append(text)
	if !ok {
		t.budget.release(entryBytes, 1)
		return entryBytes + textBytes, 1 + textItems, false
	}
	t.transcripts = append(t.transcripts, transcript)
	return 0, 0, true
}

func (t *evidenceText) replace(text string) (int64, int, bool) {
	if t == nil {
		return 0, 0, false
	}
	if t.budget == nil {
		t.budget = &summaryBudget{}
	}
	newBytes, newItems := int64(0), 0
	if text != "" {
		newBytes = summarySliceEntryBytes + summaryStringBytes(text)
		newItems = 1
	}
	byteDelta, itemDelta := newBytes-t.retainedBytes, newItems-t.items
	if byteDelta > 0 || itemDelta > 0 {
		if !t.budget.reserve(maxInt64(byteDelta, 0), maxInt(itemDelta, 0)) {
			return maxInt64(byteDelta, 0), maxInt(itemDelta, 0), false
		}
	}
	if byteDelta < 0 || itemDelta < 0 {
		t.budget.release(maxInt64(-byteDelta, 0), maxInt(-itemDelta, 0))
	}
	t.fragments = nil
	if text != "" {
		t.fragments = []string{text}
	}
	t.bytes = int64(len(text))
	t.items = newItems
	t.retainedBytes = newBytes
	return 0, 0, true
}

func (t evidenceText) String() string {
	if t.bytes == 0 && len(t.transcripts) == 0 {
		return ""
	}
	var value strings.Builder
	value.Grow(int(t.bytes))
	for _, fragment := range t.fragments {
		value.WriteString(fragment)
	}
	for _, item := range t.transcripts {
		value.WriteString(item.text.String())
	}
	return value.String()
}

func (t evidenceText) Len() int {
	size := int(t.bytes)
	for _, item := range t.transcripts {
		size += item.text.Len()
	}
	return size
}

func maxInt64(value, floor int64) int64 {
	if value < floor {
		return floor
	}
	return value
}

func maxInt(value, floor int) int {
	if value < floor {
		return floor
	}
	return value
}
