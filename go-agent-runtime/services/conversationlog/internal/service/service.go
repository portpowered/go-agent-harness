package service

import (
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

var _ conversationlog.Service = (*Service)(nil)
