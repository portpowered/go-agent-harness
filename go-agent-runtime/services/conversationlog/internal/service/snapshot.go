package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/conversationlog"
)

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
	inputText := s.inputTextForTurn(t)
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

func (s *Service) inputTextForTurn(t *turn) string {
	inputText := t.inputText
	if accum, ok := s.inputTexts[t.inputOrdinal]; ok && t.inputOrdinalSet {
		return completedInputText(accum, inputText)
	}
	if t.inputTranscriptCompleted {
		return completedInputText(&inputTranscript{fullText: t.inputFullText, completed: true}, inputText)
	}
	if transcript := t.inputTranscript.String(); strings.TrimSpace(transcript) != "" {
		return transcript
	}
	return inputText
}

func completedInputText(accum *inputTranscript, fallback string) string {
	if accum.completed {
		if strings.TrimSpace(accum.fullText) != "" {
			return accum.fullText
		}
		return ""
	}
	if deltas := accum.deltas.String(); strings.TrimSpace(deltas) != "" {
		return deltas
	}
	return fallback
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
