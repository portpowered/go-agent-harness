package services

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// The conversation log turns the raw both-side frame transcript of a session
// recording into an ordered, machine-readable per-turn summary: what the user
// put in (text and committed audio) and what the model answered (full response
// text plus recorded audio). It is written as session-log.jsonl inside the
// recording directory so a completed conversation has durable evidence that
// does not depend on any live observer.

// sessionConversationTurnInput describes one user turn's observed input.
type sessionConversationTurnInput struct {
	Text          string   `json:"text"`
	AudioBytes    uint64   `json:"audio_bytes"`
	Committed     bool     `json:"committed"`
	AudioSegments []string `json:"audio_segments,omitempty"`
}

// sessionConversationTurnResponse describes the assistant reply for one turn.
type sessionConversationTurnResponse struct {
	Text          string   `json:"text"`
	Complete      bool     `json:"complete"`
	AudioBytes    uint64   `json:"audio_bytes"`
	AudioSegments []string `json:"audio_segments,omitempty"`
}

// sessionConversationLogEntry is one JSONL line of session-log.jsonl. Field
// order is pinned by declaration so logs diff cleanly across runs.
type sessionConversationLogEntry struct {
	TurnIndex int                             `json:"turn_index"`
	Input     sessionConversationTurnInput    `json:"input"`
	Response  sessionConversationTurnResponse `json:"response"`
}

// sessionConversationTurn accumulates stream observations for the turn in
// progress. It is owned by the recording finalizer's mutex domain.
type sessionConversationTurn struct {
	inputText        string
	inputAudioBytes  uint64
	inputCommitted   bool
	inputSegments    []string
	responseDeltas   strings.Builder
	responseFullText string
	outputAudioBytes uint64
	outputSegments   []string
	complete         bool
}

// observed reports whether any conversational content was recorded for the
// turn; empty bracket turns (for example a bare session handshake) are not
// logged at all.
func (t *sessionConversationTurn) observed() bool {
	return t.inputText != "" || t.inputAudioBytes > 0 || t.responseDeltas.Len() > 0 || t.responseFullText != "" || t.outputAudioBytes > 0
}

// sessionConversationCollector reduces observed stream messages into ordered
// turn entries. A turn closes when the assistant MESSAGE.END crosses the
// recorder; anything observed afterwards starts the next turn.
type sessionConversationCollector struct {
	closed  []sessionConversationTurn
	current sessionConversationTurn
}

func (c *sessionConversationCollector) closeTurn() {
	if c.current.observed() {
		c.current.complete = true
		c.closed = append(c.closed, c.current)
	}
	c.current = sessionConversationTurn{}
}

// observe folds one stream message into the collector. inputIndex/outputIndex
// are the recording segment indices just appended for audio-bearing messages,
// or -1 when the message carried no recorded audio segment.
func (c *sessionConversationCollector) observe(msg messages.StreamMessage, outbound bool, inputIndex, outputIndex int) {
	turn := &c.current
	switch msg.Type {
	case messages.StreamTypeAudioDelta:
		audio, ok := msg.Value.(*messages.AudioDeltaValue)
		if !ok || audio == nil {
			return
		}
		if outbound {
			turn.inputAudioBytes += uint64(len(audio.Content))
			if inputIndex >= 0 {
				turn.inputSegments = append(turn.inputSegments, fmt.Sprintf("audio/in-%03d.pcm", inputIndex))
			}
			return
		}
		turn.outputAudioBytes += uint64(len(audio.Content))
		if outputIndex >= 0 {
			turn.outputSegments = append(turn.outputSegments, fmt.Sprintf("audio/out-%03d.pcm", outputIndex))
		}
	case messages.StreamTypeTextDelta:
		text, ok := msg.Value.(*messages.TextDeltaValue)
		if !ok || text == nil {
			return
		}
		if outbound {
			turn.inputText += text.Content
			return
		}
		turn.responseDeltas.WriteString(text.Content)
	case messages.StreamTypeTranscriptDelta:
		if outbound {
			return
		}
		if transcript, ok := msg.Value.(*messages.TranscriptDeltaValue); ok && transcript != nil {
			turn.responseDeltas.WriteString(transcript.Text)
		}
	case messages.StreamTypeTranscriptEnd:
		if outbound {
			return
		}
		if transcript, ok := msg.Value.(*messages.TranscriptEndValue); ok && transcript != nil && transcript.FullText != "" {
			turn.responseFullText = transcript.FullText
		}
	case messages.StreamTypeMessageEnd:
		if outbound {
			// End-of-turn control plane: commit plus response.create on the
			// realtime wire. Its value is intentionally empty, so the type is
			// the carrier of meaning here. The user side of this turn is now
			// complete.
			turn.inputCommitted = true
			return
		}
		c.closeTurn()
	}
}

// entries snapshots the ordered log: every completed turn in order plus, when
// the session ended mid-turn, the trailing partial turn marked incomplete.
func (c *sessionConversationCollector) entries() []sessionConversationLogEntry {
	all := make([]sessionConversationTurn, 0, len(c.closed)+1)
	all = append(all, c.closed...)
	if c.current.observed() {
		all = append(all, c.current)
	}
	log := make([]sessionConversationLogEntry, 0, len(all))
	for index, turn := range all {
		text := turn.responseFullText
		if text == "" {
			text = turn.responseDeltas.String()
		}
		log = append(log, sessionConversationLogEntry{
			TurnIndex: index + 1,
			Input: sessionConversationTurnInput{
				Text:          turn.inputText,
				AudioBytes:    turn.inputAudioBytes,
				Committed:     turn.inputCommitted,
				AudioSegments: turn.inputSegments,
			},
			Response: sessionConversationTurnResponse{
				Text:          text,
				Complete:      turn.complete,
				AudioBytes:    turn.outputAudioBytes,
				AudioSegments: turn.outputSegments,
			},
		})
	}
	return log
}

// sessionConversationLogJSON renders the collector snapshot as JSONL bytes.
// A nil return means no conversational content was observed, in which case no
// session-log.jsonl artifact is emitted.
func sessionConversationLogJSON(collector *sessionConversationCollector) ([]byte, error) {
	entries := collector.entries()
	if len(entries) == 0 {
		return nil, nil
	}
	var builder strings.Builder
	for _, entry := range entries {
		line, err := json.Marshal(entry)
		if err != nil {
			return nil, fmt.Errorf("encode session log entry %d: %w", entry.TurnIndex, err)
		}
		builder.Write(line)
		builder.WriteByte('\n')
	}
	return []byte(builder.String()), nil
}
