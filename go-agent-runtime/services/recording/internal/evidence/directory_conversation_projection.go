package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

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

func (c evidenceConversation) json() ([]byte, error) {
	if !c.turn.observed() && len(c.closed) == 0 {
		return nil, nil
	}
	// A fixed-capacity buffer prevents append growth from doubling a large
	// summary during finalization. The normalized bundle writer makes one
	// bounded copy; both copies are bounded by this explicit limit.
	data := make([]byte, 0, int(directorySummaryMaxBytes))
	for index, turn := range c.closed {
		var err error
		data, err = c.appendJSONTurn(data, index, turn)
		if err != nil {
			return data, err
		}
	}
	if c.turn.observed() {
		return c.appendJSONTurn(data, len(c.closed), c.turn)
	}
	return data, nil
}

func (c evidenceConversation) appendJSONTurn(data []byte, index int, turn evidenceTurn) ([]byte, error) {
	entry := c.logEntry(index, c.withResponseAudio(turn))
	line, err := json.Marshal(entry)
	if err != nil {
		return data, fmt.Errorf("encode session log entry %d: %w", index+1, err)
	}
	if len(data) > int(directorySummaryMaxBytes)-len(line)-1 {
		return data, summaryBudgetError(false)
	}
	data = append(data, line...)
	return append(data, '\n'), nil
}

func (c evidenceConversation) logEntry(index int, turn evidenceTurn) evidenceLogEntry {
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
	return entry
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

func (t evidenceText) observed() bool {
	return t.Len() > 0
}

func (t evidenceTurn) observed() bool {
	return len(t.responseIDs) > 0 || t.inputText.observed() || t.responseText.observed() || t.inputAudio > 0 || t.outputAudio > 0 || len(t.toolEvents) > 0
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
	if !t.observed() {
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

// The conversation log is a convenience projection. Raw transcript, PCM and
// provider artifacts remain authoritative and are deliberately not charged to
// this budget. The byte limit is a conservative upper bound for the retained
// projection and its JSON representation: strings are charged at the maximum
// expansion used by encoding/json, while fixed charges cover headers, map
// entries and slice capacity. This keeps finalization bounded without making
// the public recording contract depend on the caller's input lifetime.
const (
	directorySummaryMaxBytes int64 = 2 << 20
	directorySummaryMaxItems       = 4096

	// encoding/json can emit a six-byte \\u00xx escape for one input byte. The
	// fixed charge also covers a retained string header and JSON punctuation.
	summaryJSONEscapeMultiplier int64 = 6
	summaryStringFixedBytes     int64 = 32
	summarySliceEntryBytes      int64 = 64
	summaryMapEntryBytes        int64 = 128
	summaryTurnFixedBytes       int64 = 768
	summaryToolEventFixedBytes  int64 = 256

	// A final JSON line is built before it is appended to the bounded output.
	// Keep a fixed per-turn allowance for field names, numeric fields and
	// encoding scratch in addition to the retained value charges.
	summaryJSONTurnFixedBytes int64 = 512
)

type summaryBudgetErrorKind uint8

const (
	errConversationSummaryBudget summaryBudgetErrorKind = iota + 1
	errConversationSummaryItems
)

func (kind summaryBudgetErrorKind) Error() string {
	switch kind {
	case errConversationSummaryBudget:
		return "recording conversation summary budget exceeded"
	case errConversationSummaryItems:
		return "recording conversation summary item limit exceeded"
	default:
		return "recording conversation summary limit exceeded"
	}
}

func (kind summaryBudgetErrorKind) Is(target error) bool {
	other, ok := target.(summaryBudgetErrorKind)
	return ok && kind == other
}

type summaryBudget struct {
	bytes int64
	items int
}

func (b *summaryBudget) reserve(bytes int64, items int) bool {
	if b == nil || bytes < 0 || items < 0 {
		return false
	}
	if bytes > directorySummaryMaxBytes-b.bytes || items > directorySummaryMaxItems-b.items {
		return false
	}
	b.bytes += bytes
	b.items += items
	return true
}

func (b *summaryBudget) release(bytes int64, items int) {
	if b == nil {
		return
	}
	b.bytes -= bytes
	b.items -= items
	if b.bytes < 0 {
		b.bytes = 0
	}
	if b.items < 0 {
		b.items = 0
	}
}

func summaryStringBytes(value string) int64 {
	length := int64(len(value))
	if length > (directorySummaryMaxBytes-summaryStringFixedBytes)/summaryJSONEscapeMultiplier {
		return directorySummaryMaxBytes + 1
	}
	return summaryStringFixedBytes + length*summaryJSONEscapeMultiplier
}

func summaryBudgetError(items bool) error {
	if items {
		return errors.Join(errConversationSummaryBudget, errConversationSummaryItems, io.ErrShortBuffer)
	}
	return errors.Join(errConversationSummaryBudget, io.ErrShortBuffer)
}

func summaryCost(values ...string) int64 {
	cost := int64(0)
	for _, value := range values {
		valueCost := summaryStringBytes(value)
		if valueCost > directorySummaryMaxBytes-cost {
			return directorySummaryMaxBytes + 1
		}
		cost += valueCost
	}
	return cost
}
