package services

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const (
	sessionToolEventTypeCall   = "tool_call"
	sessionToolEventTypeResult = "tool_result"
	sessionToolEventStatusDone = "completed"
	sessionToolEventStatusFail = "failed"

	// Tool arguments and flat results are kept as strings so protocol-native
	// envelopes (including webmcp.tool-result.v1) remain one JSON string value
	// in session-log.jsonl. The bound prevents a provider or tool from making a
	// session log unshareably large; the recording bundle still applies its
	// normal credential redaction pass afterwards.
	maxSessionToolEventValueBytes = 64 * 1024
)

// sessionToolLifecycleObserver is the shared execution-boundary hook used by
// a directory recording. It intentionally observes tool calls where the
// composed executor runs, rather than provider debug frames, so each observed
// invocation has exactly one corresponding result.
type sessionToolLifecycleObserver interface {
	observeToolCall(messages.ToolCall)
	observeToolResult(messages.ToolCall, messages.ToolCallResponse, bool)
}

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

// sessionConversationTurnTiming carries real wall-clock measurements for one
// turn. The recording tick clock is deterministic by design (bundle artifacts
// must stay byte-comparable across equivalent runs and are hash-covered by
// the manifest), so true latency lives in a separate run-specific diagnostic
// file, never inside hashed bundle artifacts.
type sessionConversationTurnTiming struct {
	TurnIndex            int    `json:"turn_index"`
	CommittedAt          string `json:"committed_at"`
	FirstResponseAudioMS int64  `json:"first_response_audio_ms,omitempty"`
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
	TurnIndex  int                             `json:"turn_index"`
	Input      sessionConversationTurnInput    `json:"input"`
	Response   sessionConversationTurnResponse `json:"response"`
	ToolEvents []sessionConversationToolEvent  `json:"tool_events,omitempty"`
}

// sessionConversationToolEvent is the durable, per-session ordered lifecycle
// record. Calls and results use type-specific JSON fields so an empty result
// still carries an explicit content value while call entries never advertise a
// result status.
type sessionConversationToolEvent struct {
	Sequence   uint64
	Type       string
	ToolCallID string
	ToolName   string
	Arguments  string
	Status     string
	Content    string
	Image      *sessionConversationImageEvidence
}

// sessionConversationImageEvidence correlates a successful capture's typed
// projection with the immutable file emitted into the recording bundle.
// Browser identity is retained only as selected-target metadata; pixels stay
// in the artifact file and never get copied into session-log.jsonl.
type sessionConversationImageEvidence struct {
	Path            string `json:"path"`
	Source          string `json:"source"`
	BrowserID       string `json:"browser_id,omitempty"`
	TargetID        string `json:"target_id,omitempty"`
	MIMEType        string `json:"mime_type"`
	ByteLength      int    `json:"byte_length"`
	Width           int    `json:"width"`
	Height          int    `json:"height"`
	SHA256          string `json:"sha256"`
	TypedProjection string `json:"typed_projection"`
}

func (e sessionConversationToolEvent) MarshalJSON() ([]byte, error) {
	switch e.Type {
	case sessionToolEventTypeCall:
		return json.Marshal(struct {
			Sequence   uint64 `json:"sequence"`
			Type       string `json:"type"`
			ToolCallID string `json:"tool_call_id"`
			ToolName   string `json:"tool_name"`
			Arguments  string `json:"arguments"`
		}{
			Sequence: e.Sequence, Type: e.Type, ToolCallID: e.ToolCallID,
			ToolName: e.ToolName, Arguments: e.Arguments,
		})
	case sessionToolEventTypeResult:
		return json.Marshal(struct {
			Sequence   uint64                            `json:"sequence"`
			Type       string                            `json:"type"`
			ToolCallID string                            `json:"tool_call_id"`
			ToolName   string                            `json:"tool_name"`
			Status     string                            `json:"status"`
			Content    string                            `json:"content"`
			Image      *sessionConversationImageEvidence `json:"image,omitempty"`
		}{
			Sequence: e.Sequence, Type: e.Type, ToolCallID: e.ToolCallID,
			ToolName: e.ToolName, Status: e.Status, Content: e.Content, Image: e.Image,
		})
	default:
		return json.Marshal(struct {
			Sequence   uint64                            `json:"sequence"`
			Type       string                            `json:"type"`
			ToolCallID string                            `json:"tool_call_id"`
			ToolName   string                            `json:"tool_name"`
			Arguments  string                            `json:"arguments,omitempty"`
			Status     string                            `json:"status,omitempty"`
			Content    string                            `json:"content,omitempty"`
			Image      *sessionConversationImageEvidence `json:"image,omitempty"`
		}{
			Sequence: e.Sequence, Type: e.Type, ToolCallID: e.ToolCallID,
			ToolName: e.ToolName, Arguments: e.Arguments, Status: e.Status,
			Content: e.Content, Image: e.Image,
		})
	}
}

// sessionConversationTurn accumulates stream observations for the turn in
// progress. It is owned by the recording finalizer's mutex domain.
type sessionConversationTurn struct {
	inputText                string
	inputTranscript          strings.Builder
	inputFullText            string
	inputTranscriptCompleted bool
	inputAudioBytes          uint64
	inputCommitted           bool
	// inputOrdinal is this turn's zero-based position in server commit order,
	// assigned at the outbound end-of-turn boundary (inputOrdinalSet guards
	// the zero value). Item-correlated input transcription is attributed
	// through this ordinal, never arrival order.
	inputOrdinal    int
	inputOrdinalSet bool
	// committedAt / firstResponseAudioAt are real wall-clock observations
	// (the recording clock is deliberately deterministic and carries no
	// duration information), captured so per-turn response latency is
	// computable from the bundle alone.
	committedAt          time.Time
	firstResponseAudioAt time.Time
	inputSegments        []string
	responseDeltas       strings.Builder
	responseFullText     string
	outputAudioBytes     uint64
	outputSegments       []string
	complete             bool
	toolEvents           []sessionConversationToolEvent
	toolCallMessage      bool
}

// observed reports whether any conversational content was recorded for the
// turn; empty bracket turns (for example a bare session handshake) are not
// logged at all.
func (t *sessionConversationTurn) observed() bool {
	return t.inputText != "" || strings.TrimSpace(t.inputTranscript.String()) != "" || strings.TrimSpace(t.inputFullText) != "" || t.inputAudioBytes > 0 || t.responseDeltas.Len() > 0 || t.responseFullText != "" || t.outputAudioBytes > 0 || len(t.toolEvents) > 0
}

// sessionConversationCollector reduces observed stream messages into ordered
// turn entries. A turn closes when the assistant MESSAGE.END crosses the
// recorder; anything observed afterwards starts the next turn.
type sessionConversationCollector struct {
	closed           []sessionConversationTurn
	current          sessionConversationTurn
	nextToolSequence uint64

	// Item-correlated input transcription. Input ASR streams asynchronously
	// and interleaves across turns (a turn's transcript routinely finishes
	// after the next turn has begun), so text is accumulated per committed
	// input ordinal on the collector — NOT on the turn absorbing whatever is
	// current at arrival time — and joined to turns at snapshot time.
	itemOrdinals    map[string]int
	nextItemOrdinal int
	inputTexts      map[int]*sessionInputTranscriptAccum
	committedInputs int

	// now is the wall clock for per-turn latency observations; tests inject.
	now func() time.Time
}

// sessionInputTranscriptAccum holds one committed input's ASR text.
type sessionInputTranscriptAccum struct {
	deltas    strings.Builder
	fullText  string
	completed bool
}

// wallNow returns the injected wall clock's reading, or the zero time when no
// clock is configured (timing is then omitted from the log entirely, which
// keeps byte-exact log expectations stable for consumers that opt out).
func (c *sessionConversationCollector) wallNow() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Time{}
}

// inputOrdinalForItem resolves (registering on first sight) the committed
// ordinal owning a provider item id. INPUT_ITEM.ADDED events register ids in
// authoritative server commit order; a transcription event for an id that
// arrives first registers it in first-sight order as a fallback.
func (c *sessionConversationCollector) inputOrdinalForItem(itemID string) int {
	if c.itemOrdinals == nil {
		c.itemOrdinals = make(map[string]int)
	}
	ordinal, ok := c.itemOrdinals[itemID]
	if !ok {
		ordinal = c.nextItemOrdinal
		c.itemOrdinals[itemID] = ordinal
		c.nextItemOrdinal++
	}
	return ordinal
}

func (c *sessionConversationCollector) inputAccum(ordinal int) *sessionInputTranscriptAccum {
	if c.inputTexts == nil {
		c.inputTexts = make(map[int]*sessionInputTranscriptAccum)
	}
	accum, ok := c.inputTexts[ordinal]
	if !ok {
		accum = &sessionInputTranscriptAccum{}
		c.inputTexts[ordinal] = accum
	}
	return accum
}

func (c *sessionConversationCollector) observeToolCall(call messages.ToolCall) {
	if c == nil {
		return
	}
	c.nextToolSequence++
	c.current.toolEvents = append(c.current.toolEvents, sessionConversationToolEvent{
		Sequence:   c.nextToolSequence,
		Type:       sessionToolEventTypeCall,
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Arguments:  boundSessionToolEventValue(call.Arguments),
	})
}

func (c *sessionConversationCollector) observeToolResult(call messages.ToolCall, response messages.ToolCallResponse, failed bool, image ...*sessionConversationImageEvidence) {
	if c == nil {
		return
	}
	c.nextToolSequence++
	status := sessionToolEventStatusDone
	if failed {
		status = sessionToolEventStatusFail
	}
	var imageEvidence *sessionConversationImageEvidence
	if len(image) > 0 {
		imageEvidence = image[0]
	}
	c.current.toolEvents = append(c.current.toolEvents, sessionConversationToolEvent{
		Sequence:   c.nextToolSequence,
		Type:       sessionToolEventTypeResult,
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Status:     status,
		Content:    boundSessionToolEventValue(response.Content),
		Image:      imageEvidence,
	})
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
	case messages.StreamTypeInputItemAdded:
		if added, ok := msg.Value.(*messages.InputItemAddedValue); ok && added != nil && added.ItemID != "" {
			c.inputOrdinalForItem(added.ItemID)
		}
		return
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
		if turn.inputCommitted && turn.firstResponseAudioAt.IsZero() && len(audio.Content) > 0 {
			turn.firstResponseAudioAt = c.wallNow()
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
			if msg.Role == messages.RoleUser {
				if transcript.ItemID != "" {
					accum := c.inputAccum(c.inputOrdinalForItem(transcript.ItemID))
					if !accum.completed {
						accum.deltas.WriteString(transcript.Text)
					}
					return
				}
				turn.inputTranscript.WriteString(transcript.Text)
			} else {
				turn.responseDeltas.WriteString(transcript.Text)
			}
		}
	case messages.StreamTypeTranscriptEnd:
		if outbound {
			return
		}
		if transcript, ok := msg.Value.(*messages.TranscriptEndValue); ok && transcript != nil {
			if msg.Role == messages.RoleUser {
				if transcript.ItemID != "" {
					accum := c.inputAccum(c.inputOrdinalForItem(transcript.ItemID))
					// The completion is authoritative even when it is empty:
					// it must not fall back to interim deltas and invent a
					// user utterance in the finalized log.
					accum.completed = true
					accum.fullText = transcript.FullText
					return
				}
				turn.inputTranscriptCompleted = true
				turn.inputFullText = transcript.FullText
			} else if transcript.FullText != "" {
				turn.responseFullText = transcript.FullText
			}
		}
	case messages.StreamTypeToolCallStart, messages.StreamTypeToolCallDelta, messages.StreamTypeToolCallEnd:
		if !outbound {
			// A provider tool-call MESSAGE.END is an intermediate assistant
			// boundary. Keep this turn open so the execution-boundary result and
			// its later continuation remain correlated with the spoken turn.
			turn.toolCallMessage = true
		}
	case messages.StreamTypeMessageEnd:
		if outbound {
			// End-of-turn control plane: commit plus response.create on the
			// realtime wire. Its value is intentionally empty, so the type is
			// the carrier of meaning here. The user side of this turn is now
			// complete, and its position in commit order binds it to the
			// server-side input item created for the committed audio.
			if !turn.inputCommitted {
				turn.inputCommitted = true
				turn.inputOrdinal = c.committedInputs
				turn.inputOrdinalSet = true
				c.committedInputs++
				turn.committedAt = c.wallNow()
			}
			return
		}
		if turn.toolCallMessage {
			turn.toolCallMessage = false
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
		inputText := turn.inputText
		if accum, ok := c.inputTexts[turn.inputOrdinal]; ok && turn.inputOrdinalSet {
			// Item-correlated ASR is authoritative for committed turns: the
			// completed transcript when the provider finalized one (even
			// empty — never invent an utterance from interim deltas), the
			// accumulated deltas otherwise.
			if accum.completed {
				if strings.TrimSpace(accum.fullText) != "" {
					inputText = accum.fullText
				} else {
					inputText = ""
				}
			} else if deltas := accum.deltas.String(); strings.TrimSpace(deltas) != "" {
				inputText = deltas
			}
		} else if turn.inputTranscriptCompleted {
			if strings.TrimSpace(turn.inputFullText) != "" {
				inputText = turn.inputFullText
			} else {
				inputText = ""
			}
		} else if transcript := turn.inputTranscript.String(); strings.TrimSpace(transcript) != "" {
			inputText = transcript
		}
		text := turn.responseFullText
		if text == "" {
			text = turn.responseDeltas.String()
		}
		log = append(log, sessionConversationLogEntry{
			TurnIndex: index + 1,
			Input: sessionConversationTurnInput{
				Text:          inputText,
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
			ToolEvents: append([]sessionConversationToolEvent(nil), turn.toolEvents...),
		})
	}
	return log
}

// timingEntries snapshots real wall-clock turn timing (empty when no wall
// clock was injected). Written as the run-specific timing.json diagnostic
// beside the deterministic bundle.
func (c *sessionConversationCollector) timingEntries() []sessionConversationTurnTiming {
	all := make([]sessionConversationTurn, 0, len(c.closed)+1)
	all = append(all, c.closed...)
	if c.current.observed() {
		all = append(all, c.current)
	}
	var timings []sessionConversationTurnTiming
	for index, turn := range all {
		if turn.committedAt.IsZero() {
			continue
		}
		timing := sessionConversationTurnTiming{
			TurnIndex:   index + 1,
			CommittedAt: turn.committedAt.UTC().Format(time.RFC3339Nano),
		}
		if !turn.firstResponseAudioAt.IsZero() {
			timing.FirstResponseAudioMS = turn.firstResponseAudioAt.Sub(turn.committedAt).Milliseconds()
		}
		timings = append(timings, timing)
	}
	return timings
}

func boundSessionToolEventValue(value string) string {
	if len(value) <= maxSessionToolEventValueBytes {
		return value
	}
	const suffix = "...[truncated]"
	limit := maxSessionToolEventValueBytes - len(suffix)
	for limit > 0 && !utf8.ValidString(value[:limit]) {
		limit--
	}
	return value[:limit] + suffix
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
