package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/conversationlog"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

const (
	toolEventTypeCall   = "tool_call"
	toolEventTypeResult = "tool_result"
	toolEventStatusDone = "completed"
	toolEventStatusFail = "failed"
)

// Service is the private stateful implementation behind the public reducer
// contract. The mutex makes the public service safe for observation and
// snapshot callers that do not share the CLI recording mutex domain.
type Service struct {
	mu sync.Mutex

	closed  []turn
	current turn

	nextToolSequence uint64

	// Input transcription is keyed by provider item identity. The item
	// announcements define server commit order; transcript arrival order does
	// not.
	itemOrdinals    map[string]int
	nextItemOrdinal int
	inputTexts      map[int]*inputTranscript
	committedInputs int

	now clock.Source
}

// New constructs an independent reducer. A nil source intentionally omits
// wall-clock timing while retaining deterministic JSONL output.
func New(source clock.Source) *Service {
	return &Service{now: source}
}

type inputTranscript struct {
	deltas    strings.Builder
	fullText  string
	completed bool
}

type turn struct {
	inputText                string
	inputTranscript          strings.Builder
	inputFullText            string
	inputTranscriptCompleted bool
	inputAudioBytes          uint64
	inputCommitted           bool
	inputOrdinal             int
	inputOrdinalSet          bool
	committedAt              time.Time
	firstResponseAudioAt     time.Time
	inputSegments            []string
	responseDeltas           strings.Builder
	responseFullText         string
	outputAudioBytes         uint64
	outputSegments           []string
	complete                 bool
	toolEvents               []conversationlog.ToolEvent
	toolCallMessage          bool
}

func (t *turn) observed() bool {
	return t.inputText != "" ||
		strings.TrimSpace(t.inputTranscript.String()) != "" ||
		strings.TrimSpace(t.inputFullText) != "" ||
		t.inputAudioBytes > 0 ||
		t.responseDeltas.Len() > 0 ||
		t.responseFullText != "" ||
		t.outputAudioBytes > 0 ||
		len(t.toolEvents) > 0
}

func (s *Service) wallNow() time.Time {
	if s.now == nil {
		return time.Time{}
	}
	return s.now.Now()
}

func (s *Service) inputOrdinalForItem(itemID string) int {
	if s.itemOrdinals == nil {
		s.itemOrdinals = make(map[string]int)
	}
	ordinal, ok := s.itemOrdinals[itemID]
	if !ok {
		ordinal = s.nextItemOrdinal
		s.itemOrdinals[itemID] = ordinal
		s.nextItemOrdinal++
	}
	return ordinal
}

func (s *Service) inputAccum(ordinal int) *inputTranscript {
	if s.inputTexts == nil {
		s.inputTexts = make(map[int]*inputTranscript)
	}
	accum, ok := s.inputTexts[ordinal]
	if !ok {
		accum = &inputTranscript{}
		s.inputTexts[ordinal] = accum
	}
	return accum
}

// Observe folds one stream message into the reducer. inputIndex/outputIndex
// refer to the already-persisted audio segment indices and are never inferred
// from message arrival order.
func (s *Service) Observe(msg messages.StreamMessage, outbound bool, inputIndex, outputIndex int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observeLocked(msg, outbound, inputIndex, outputIndex)
}

func (s *Service) observeLocked(msg messages.StreamMessage, outbound bool, inputIndex, outputIndex int) {
	turn := &s.current
	switch msg.Type {
	case messages.StreamTypeInputItemAdded:
		if added, ok := msg.Value.(*messages.InputItemAddedValue); ok && added != nil && added.ItemID != "" {
			s.inputOrdinalForItem(added.ItemID)
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
			turn.firstResponseAudioAt = s.wallNow()
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
		transcript, ok := msg.Value.(*messages.TranscriptDeltaValue)
		if !ok || transcript == nil {
			return
		}
		if msg.Role == messages.RoleUser {
			if transcript.ItemID != "" {
				accum := s.inputAccum(s.inputOrdinalForItem(transcript.ItemID))
				if !accum.completed {
					accum.deltas.WriteString(transcript.Text)
				}
				return
			}
			turn.inputTranscript.WriteString(transcript.Text)
			return
		}
		turn.responseDeltas.WriteString(transcript.Text)
	case messages.StreamTypeTranscriptEnd:
		if outbound {
			return
		}
		transcript, ok := msg.Value.(*messages.TranscriptEndValue)
		if !ok || transcript == nil {
			return
		}
		if msg.Role == messages.RoleUser {
			if transcript.ItemID != "" {
				accum := s.inputAccum(s.inputOrdinalForItem(transcript.ItemID))
				// Completion is authoritative even when FullText is empty: an
				// interim delta must never invent a user utterance.
				accum.completed = true
				accum.fullText = transcript.FullText
				return
			}
			turn.inputTranscriptCompleted = true
			turn.inputFullText = transcript.FullText
			return
		}
		if transcript.FullText != "" {
			turn.responseFullText = transcript.FullText
		}
	case messages.StreamTypeToolCallStart, messages.StreamTypeToolCallDelta, messages.StreamTypeToolCallEnd:
		if !outbound {
			// Provider tool-call MESSAGE.END is an intermediate boundary. Keep
			// the spoken turn open for the execution-boundary result and its
			// later continuation.
			turn.toolCallMessage = true
		}
	case messages.StreamTypeMessageEnd:
		if outbound {
			if !turn.inputCommitted {
				turn.inputCommitted = true
				turn.inputOrdinal = s.committedInputs
				turn.inputOrdinalSet = true
				s.committedInputs++
				turn.committedAt = s.wallNow()
			}
			return
		}
		if turn.toolCallMessage {
			turn.toolCallMessage = false
			return
		}
		s.closeTurnLocked()
	case messages.StreamTypeMessageStart,
		messages.StreamTypeTextStart,
		messages.StreamTypeTextEnd,
		messages.StreamTypeAudioStart,
		messages.StreamTypeAudioEnd,
		messages.StreamTypeImageStart,
		messages.StreamTypeImageDelta,
		messages.StreamTypeImageEnd,
		messages.StreamTypeVideoStart,
		messages.StreamTypeVideoDelta,
		messages.StreamTypeVideoEnd,
		messages.StreamTypeFileStart,
		messages.StreamTypeFileDelta,
		messages.StreamTypeFileEnd,
		messages.StreamTypeEmbeddingStart,
		messages.StreamTypeEmbeddingDelta,
		messages.StreamTypeEmbeddingEnd,
		messages.StreamTypeReasoningStart,
		messages.StreamTypeReasoningDelta,
		messages.StreamTypeReasoningEnd,
		messages.StreamTypeVADSpeechStarted,
		messages.StreamTypeVADSpeechStopped,
		messages.StreamTypeTranscriptStart,
		messages.StreamTypePong,
		messages.StreamTypeSessionOpen,
		messages.StreamTypeSessionClose,
		messages.StreamTypeSessionCreated,
		messages.StreamTypeSessionUpdated,
		messages.StreamTypeSessionUpdate,
		messages.StreamTypeResponseCancel,
		messages.StreamTypeResponseCreate,
		messages.StreamTypeRefusal,
		messages.StreamTypeLoopEnd,
		messages.StreamTypeUsageInfo,
		messages.StreamTypeError,
		messages.StreamTypeSystemFullMessage:
		return
	default:
		// Unknown or lifecycle-only messages do not contribute to the
		// conversation projection and are intentionally ignored.
		return
	}
}

func (s *Service) closeTurnLocked() {
	if s.current.observed() {
		closed := cloneTurn(&s.current)
		closed.complete = true
		s.closed = append(s.closed, closed)
	}
	s.current = turn{}
}

// ObserveToolCall appends one execution-boundary call with a monotonic
// sequence number. It is intentionally separate from provider wire events.
func (s *Service) ObserveToolCall(call messages.ToolCall) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextToolSequence++
	s.current.toolEvents = append(s.current.toolEvents, conversationlog.ToolEvent{
		Sequence:   s.nextToolSequence,
		Type:       toolEventTypeCall,
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Arguments:  boundValue(call.Arguments),
	})
}

// ObserveToolResult appends one execution-boundary result. An optional image
// projection is copied immediately so later host mutation cannot alter a
// snapshot.
func (s *Service) ObserveToolResult(call messages.ToolCall, response messages.ToolCallResponse, failed bool, image ...*conversationlog.ImageEvidence) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextToolSequence++
	status := toolEventStatusDone
	if failed {
		status = toolEventStatusFail
	}
	var imageEvidence *conversationlog.ImageEvidence
	if len(image) > 0 {
		imageEvidence = cloneImage(image[0])
	}
	s.current.toolEvents = append(s.current.toolEvents, conversationlog.ToolEvent{
		Sequence:   s.nextToolSequence,
		Type:       toolEventTypeResult,
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Status:     status,
		Content:    boundValue(response.Content),
		Image:      imageEvidence,
	})
}

// Entries returns a detached ordered snapshot. No caller-owned slice or
// pointer is retained by the service or shared with the returned value.
func (s *Service) Entries() []conversationlog.LogEntry {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entriesLocked()
}

func (s *Service) entriesLocked() []conversationlog.LogEntry {
	count := len(s.closed)
	if s.current.observed() {
		count++
	}
	if count == 0 {
		return nil
	}
	entries := make([]conversationlog.LogEntry, 0, count)
	for index := range s.closed {
		entries = append(entries, s.entryLocked(&s.closed[index], index+1))
	}
	if s.current.observed() {
		entries = append(entries, s.entryLocked(&s.current, len(entries)+1))
	}
	return entries
}

func (s *Service) entryLocked(t *turn, turnIndex int) conversationlog.LogEntry {
	inputText := t.inputText
	if accum, ok := s.inputTexts[t.inputOrdinal]; ok && t.inputOrdinalSet {
		if accum.completed {
			if strings.TrimSpace(accum.fullText) != "" {
				inputText = accum.fullText
			} else {
				inputText = ""
			}
		} else if deltas := accum.deltas.String(); strings.TrimSpace(deltas) != "" {
			inputText = deltas
		}
	} else if t.inputTranscriptCompleted {
		if strings.TrimSpace(t.inputFullText) != "" {
			inputText = t.inputFullText
		} else {
			inputText = ""
		}
	} else if transcript := t.inputTranscript.String(); strings.TrimSpace(transcript) != "" {
		inputText = transcript
	}
	text := t.responseFullText
	if text == "" {
		text = t.responseDeltas.String()
	}
	return conversationlog.LogEntry{
		TurnIndex: turnIndex,
		Input: conversationlog.TurnInput{
			Text:          inputText,
			AudioBytes:    t.inputAudioBytes,
			Committed:     t.inputCommitted,
			AudioSegments: cloneStrings(t.inputSegments),
		},
		Response: conversationlog.TurnResponse{
			Text:          text,
			Complete:      t.complete,
			AudioBytes:    t.outputAudioBytes,
			AudioSegments: cloneStrings(t.outputSegments),
		},
		ToolEvents: cloneToolEvents(t.toolEvents),
	}
}

// TimingEntries returns detached run-specific timing observations. Timing is
// absent when no non-zero wall-clock source was injected.
func (s *Service) TimingEntries() []conversationlog.TurnTiming {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var timings []conversationlog.TurnTiming
	for index := range s.closed {
		timing := timingForTurn(&s.closed[index], index+1)
		if timing != nil {
			timings = append(timings, *timing)
		}
	}
	if s.current.observed() {
		timing := timingForTurn(&s.current, len(s.closed)+1)
		if timing != nil {
			timings = append(timings, *timing)
		}
	}
	return timings
}

func timingForTurn(t *turn, turnIndex int) *conversationlog.TurnTiming {
	if t.committedAt.IsZero() {
		return nil
	}
	timing := &conversationlog.TurnTiming{
		TurnIndex:   turnIndex,
		CommittedAt: t.committedAt.UTC().Format(time.RFC3339Nano),
	}
	if !t.firstResponseAudioAt.IsZero() {
		timing.FirstResponseAudioMS = t.firstResponseAudioAt.Sub(t.committedAt).Milliseconds()
	}
	return timing
}

// JSONL renders one deterministic line per observed turn. A nil result means
// no conversational content was observed and therefore no artifact is needed.
func (s *Service) JSONL() ([]byte, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := s.entriesLocked()
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

func boundValue(value string) string {
	if len(value) <= conversationlog.MaxToolEventValueBytes {
		return value
	}
	limit := conversationlog.MaxToolEventValueBytes - len(conversationlog.ToolEventTruncationSuffix)
	for limit > 0 && !utf8.ValidString(value[:limit]) {
		limit--
	}
	return value[:limit] + conversationlog.ToolEventTruncationSuffix
}

func cloneTurn(source *turn) turn {
	copyTurn := turn{
		inputText:                source.inputText,
		inputFullText:            source.inputFullText,
		inputTranscriptCompleted: source.inputTranscriptCompleted,
		inputAudioBytes:          source.inputAudioBytes,
		inputCommitted:           source.inputCommitted,
		inputOrdinal:             source.inputOrdinal,
		inputOrdinalSet:          source.inputOrdinalSet,
		committedAt:              source.committedAt,
		firstResponseAudioAt:     source.firstResponseAudioAt,
		inputSegments:            cloneStrings(source.inputSegments),
		responseFullText:         source.responseFullText,
		outputAudioBytes:         source.outputAudioBytes,
		outputSegments:           cloneStrings(source.outputSegments),
		complete:                 source.complete,
		toolEvents:               cloneToolEvents(source.toolEvents),
		toolCallMessage:          source.toolCallMessage,
	}
	copyTurn.inputTranscript.WriteString(source.inputTranscript.String())
	copyTurn.responseDeltas.WriteString(source.responseDeltas.String())
	return copyTurn
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}

func cloneImage(image *conversationlog.ImageEvidence) *conversationlog.ImageEvidence {
	if image == nil {
		return nil
	}
	copyImage := *image
	return &copyImage
}

func cloneToolEvents(events []conversationlog.ToolEvent) []conversationlog.ToolEvent {
	if len(events) == 0 {
		return nil
	}
	cloned := make([]conversationlog.ToolEvent, len(events))
	for index, event := range events {
		cloned[index] = event
		cloned[index].Image = cloneImage(event.Image)
	}
	return cloned
}

var _ conversationlog.Service = (*Service)(nil)
