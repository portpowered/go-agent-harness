package evidence

import (
	"encoding/json"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"strings"
	"time"
)

// evidenceConversation is intentionally small and transport-neutral. The
// detailed provider transcript remains in the two raw JSONL artifacts; this
// index preserves the useful turn summary used by CLI and room tooling.
type evidenceConversation struct {
	inputBytes  uint64
	outputBytes uint64
	closed      []evidenceTurn
	turn        evidenceTurn
}

type evidenceTurn struct {
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
}

type evidenceToolEvent struct {
	Sequence   uint64 `json:"sequence"`
	Type       string `json:"type"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	Arguments  string `json:"arguments,omitempty"`
	Status     string `json:"status,omitempty"`
	Content    string `json:"content,omitempty"`
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

// observe builds a convenience projection. The typed transcript retains every
// admitted message, including types that have no conversation summary field.
func (c *evidenceConversation) observe(msg messages.StreamMessage, outbound bool, sequence uint64) {
	if msg.Role == messages.RoleTool {
		if value, ok := msg.Value.(*messages.TextDeltaValue); ok && value != nil {
			c.turn.toolEvents = append(c.turn.toolEvents, evidenceToolEvent{Sequence: sequence, Type: "tool_result", ToolCallID: msg.ToolCallId, Content: value.Content})
		}
		return
	}
	c.observeText(msg, outbound)
	if !outbound && (msg.Type == messages.StreamTypeToolCallStart || msg.Type == messages.StreamTypeToolCallDelta || msg.Type == messages.StreamTypeToolCallEnd) {
		c.turn.toolMessage = true
	}
	if msg.Type == messages.StreamTypeMessageEnd {
		c.endMessage(outbound)
	}
	if msg.Type == messages.StreamTypeToolCallEnd {
		if value, ok := msg.Value.(*messages.ToolCallEndValue); ok && value != nil {
			c.turn.toolEvents = append(c.turn.toolEvents, evidenceToolEvent{Sequence: sequence, Type: "tool_call", ToolCallID: value.ToolCallID, ToolName: value.Name, Arguments: value.Arguments})
		}
	}
}

func (c *evidenceConversation) observeText(msg messages.StreamMessage, outbound bool) {
	switch value := msg.Value.(type) {
	case *messages.TextDeltaValue:
		if value != nil && msg.Type == messages.StreamTypeTextDelta {
			c.appendText(outbound, value.Content)
		}
	case *messages.TranscriptDeltaValue:
		if value != nil && !outbound && msg.Type == messages.StreamTypeTranscriptDelta {
			c.transcriptText(msg.Role == messages.RoleUser).observeTranscript(value.ItemID, value.Text, false)
		}
	case *messages.TranscriptEndValue:
		if value != nil && !outbound && msg.Type == messages.StreamTypeTranscriptEnd {
			c.transcriptText(msg.Role == messages.RoleUser).observeTranscript(value.ItemID, value.FullText, true)
		}
	}
}

func (c *evidenceConversation) appendText(input bool, text string) {
	if input {
		c.turn.inputText.WriteString(text)
	} else {
		c.turn.responseText.WriteString(text)
	}
}

func (c *evidenceConversation) endMessage(outbound bool) {
	if outbound {
		c.turn.committed = true
		return
	}
	if c.turn.toolMessage {
		c.turn.toolMessage = false
		return
	}
	c.turn.complete = true
	if c.turn.observed() {
		c.closed = append(c.closed, c.turn)
	}
	c.turn = evidenceTurn{}
}

func (c *evidenceConversation) observeAudio(input bool, index, bytes int, _ time.Time) {
	if c == nil {
		return
	}
	if input {
		if c.turn.inputAudio == 0 {
			c.turn.inputOffset = c.inputBytes
		}
		c.inputBytes += uint64(bytes)
		c.turn.inputAudio += uint64(bytes)
		if len(c.turn.inputSegments) == 0 {
			c.turn.inputSegments = []string{fmt.Sprintf("audio/in-%03d.pcm", index)}
		}
		return
	}
	if c.turn.outputAudio == 0 {
		c.turn.outputOffset = c.outputBytes
	}
	c.outputBytes += uint64(bytes)
	c.turn.outputAudio += uint64(bytes)
	if len(c.turn.outputSegments) == 0 {
		c.turn.outputSegments = []string{fmt.Sprintf("audio/out-%03d.pcm", index)}
	}
}

func (t evidenceTurn) observed() bool {
	return t.inputText.Len() > 0 || t.responseText.Len() > 0 || t.inputAudio > 0 || t.outputAudio > 0 || len(t.toolEvents) > 0
}

func (c evidenceConversation) json() ([]byte, error) {
	turns := append([]evidenceTurn(nil), c.closed...)
	if c.turn.observed() {
		turns = append(turns, c.turn)
	}
	if len(turns) == 0 {
		return nil, nil
	}
	var data []byte
	for index, turn := range turns {
		entry := evidenceLogEntry{TurnIndex: index + 1, ToolEvents: append([]evidenceToolEvent(nil), turn.toolEvents...)}
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
		line, err := json.Marshal(entry)
		if err != nil {
			return nil, fmt.Errorf("encode session log entry %d: %w", index+1, err)
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	return data, nil
}

// Full transcripts replace deltas for the same item; they are snapshots, not
// another fragment. Distinct items remain distinct within a turn projection.
// The typed transcript is authoritative for asynchronous cross-turn attribution.
type evidenceText struct {
	strings.Builder
	transcripts []evidenceTranscript
}

type evidenceTranscript struct{ itemID, text string }

func (c *evidenceConversation) transcriptText(input bool) *evidenceText {
	if input {
		return &c.turn.inputText
	}
	return &c.turn.responseText
}

func (t *evidenceText) observeTranscript(itemID, text string, complete bool) {
	for index := range t.transcripts {
		if t.transcripts[index].itemID == itemID {
			if complete {
				t.transcripts[index].text = text
			} else {
				t.transcripts[index].text += text
			}
			return
		}
	}
	t.transcripts = append(t.transcripts, evidenceTranscript{itemID: itemID, text: text})
}

func (t evidenceText) String() string {
	var value strings.Builder
	value.WriteString(t.Builder.String())
	for _, item := range t.transcripts {
		value.WriteString(item.text)
	}
	return value.String()
}

func (t evidenceText) Len() int {
	size := t.Builder.Len()
	for _, item := range t.transcripts {
		size += len(item.text)
	}
	return size
}
