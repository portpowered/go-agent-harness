// Package functional provides an end-to-end test harness for the go-agent-loop library.
//
// Tests in this package exercise the public AgenticLoop API against mock inference
// and tool-execution backends, validating full message flows (conversation history,
// delta stream events, streaming text) without coupling to internal implementation
// details.
//
// # Extending the harness
//
// To add a new scenario (tool use, multi-turn, batch tools, etc.) add the relevant
// MockInferencer responses and MockToolExecutor results, then build a Scenario via
// NewScenario and call Execute or ExecuteStreaming on it. The assertion helpers in
// this file (AssertMessages, AssertDeltaContains) can be combined freely.
package media

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ---------------------------------------------------------------------------
// MockInferencer
// ---------------------------------------------------------------------------

// inferenceEntry stores one configured response together with optional chunk
// slices that should be emitted as separate DELTA events via InferStream.
type inferenceEntry struct {
	result          messages.InferenceResult
	chunks          []string // non-nil → emit as multiple TEXT.DELTA events
	reasoningChunks []string // non-nil → emit as multiple REASONING.DELTA events before text
	imageChunks     [][]byte // non-nil → emit as multiple IMAGE.DELTA events
	audioChunks     [][]byte // non-nil → emit as multiple AUDIO.DELTA events
	videoChunks     [][]byte // non-nil → emit as multiple VIDEO.DELTA events
	fileChunks      [][]byte // non-nil → emit as multiple FILE.DELTA events
}

// MockInferencer is a configurable test double for subsystems.Inferencer.
//
// Use AddTextResponse, AddChunkedTextResponse, AddToolCallResponse, and
// AddBatchToolCallResponse to define the sequence of results the mock returns.
// The last configured response is repeated once all entries are consumed.
//
// InferStream is always used when available (the model runner prefers it). The
// mock emits TEXT.START / TEXT.DELTA / TEXT.END / MESSAGE.END events with
// Role=RoleAssistant so tests can assert on delta stream role ordering.
type MockInferencer struct {
	entries          []inferenceEntry
	callCount        int
	CapturedMessages [][]messages.Message
}

// AddTextResponse appends a plain-text assistant response to the mock's queue.
func (m *MockInferencer) AddTextResponse(text string) *MockInferencer {
	m.entries = append(m.entries, inferenceEntry{
		result: messages.InferenceResult{
			Message: messages.NewTextMessage(messages.RoleAssistant, text),
		},
	})
	return m
}

// AddChunkedTextResponse appends a text response whose InferStream path emits
// each string in chunks as a separate TEXT.DELTA event. Useful for verifying
// that the stream reader assembles partial deltas correctly.
func (m *MockInferencer) AddChunkedTextResponse(chunks []string) *MockInferencer {
	combined := strings.Join(chunks, "")
	m.entries = append(m.entries, inferenceEntry{
		result: messages.InferenceResult{
			Message: messages.NewTextMessage(messages.RoleAssistant, combined),
		},
		chunks: chunks,
	})
	return m
}

// AddChunkedReasoningThenTextResponse appends a response that emits REASONING.START,
// then each string in reasoningChunks as a separate REASONING.DELTA, then REASONING.END,
// then TEXT events (with text). Verifies that chunked reasoning is assembled and recorded.
// Pass nil or empty reasoningChunks for no reasoning.
func (m *MockInferencer) AddChunkedReasoningThenTextResponse(reasoningChunks []string, text string) *MockInferencer {
	reasoningFull := strings.Join(reasoningChunks, "")
	var parts []messages.ContentPart
	if reasoningFull != "" {
		parts = append(parts, messages.NewReasoningPart(reasoningFull))
	}
	if text != "" {
		parts = append(parts, messages.NewTextPart(text))
	}
	msg := messages.Message{Role: messages.RoleAssistant, ContentParts: parts}
	m.entries = append(m.entries, inferenceEntry{
		result:          messages.InferenceResult{Message: msg},
		reasoningChunks: reasoningChunks,
	})
	return m
}

// AddToolCallResponse appends a single-tool-call response to the mock's queue.
func (m *MockInferencer) AddToolCallResponse(toolID, toolName, args string) *MockInferencer {
	tc := messages.ToolCall{ID: toolID, Name: toolName, Arguments: args}
	m.entries = append(m.entries, inferenceEntry{
		result: messages.InferenceResult{
			Message:   messages.Message{Role: messages.RoleAssistant, ToolCalls: []messages.ToolCall{tc}},
			ToolCalls: []messages.ToolCall{tc},
		},
	})
	return m
}

// AddImageResponse appends an image-only assistant response to the mock's queue.
// imageBytes is the raw image data; mediaType is the MIME type (e.g. "image/png").
// InferStream emits IMAGE.START / IMAGE.DELTA / IMAGE.END events for this entry.
func (m *MockInferencer) AddImageResponse(imageBytes []byte, mediaType string) *MockInferencer {
	msg := messages.Message{
		Role:         messages.RoleAssistant,
		ContentParts: []messages.ContentPart{messages.ImagePart{Bytes: imageBytes, MediaType: mediaType}},
	}
	m.entries = append(m.entries, inferenceEntry{
		result: messages.InferenceResult{Message: msg},
	})
	return m
}

// AddAudioResponse appends an audio-only assistant response to the mock's queue.
// audioBytes is the raw audio data; mediaType is the MIME type (e.g. "audio/pcm").
// InferStream emits AUDIO.START / AUDIO.DELTA / AUDIO.END events for this entry.
func (m *MockInferencer) AddAudioResponse(audioBytes []byte, mediaType string) *MockInferencer {
	msg := messages.Message{
		Role:         messages.RoleAssistant,
		ContentParts: []messages.ContentPart{messages.AudioPart{Bytes: audioBytes, MediaType: mediaType}},
	}
	m.entries = append(m.entries, inferenceEntry{
		result: messages.InferenceResult{Message: msg},
	})
	return m
}

// AddVideoResponse appends a video-only assistant response to the mock's queue.
// videoBytes is the raw video data; mediaType is the MIME type (e.g. "video/mp4").
// InferStream emits VIDEO.START / VIDEO.DELTA / VIDEO.END events for this entry.
func (m *MockInferencer) AddVideoResponse(videoBytes []byte, mediaType string) *MockInferencer {
	msg := messages.Message{
		Role:         messages.RoleAssistant,
		ContentParts: []messages.ContentPart{messages.VideoPart{Bytes: videoBytes, MediaType: mediaType}},
	}
	m.entries = append(m.entries, inferenceEntry{
		result: messages.InferenceResult{Message: msg},
	})
	return m
}

// AddFileResponse appends a file-only assistant response to the mock's queue.
// fileBytes is the raw file data; mediaType is the MIME type (e.g. "application/pdf"); name is optional.
// InferStream emits FILE.START / FILE.DELTA / FILE.END events for this entry.
func (m *MockInferencer) AddFileResponse(fileBytes []byte, mediaType, name string) *MockInferencer {
	msg := messages.Message{
		Role:         messages.RoleAssistant,
		ContentParts: []messages.ContentPart{messages.FilePart{Bytes: fileBytes, MediaType: mediaType, Name: name}},
	}
	m.entries = append(m.entries, inferenceEntry{
		result: messages.InferenceResult{Message: msg},
	})
	return m
}

// AddBatchToolCallResponse appends a response that triggers multiple tool calls.
func (m *MockInferencer) AddBatchToolCallResponse(calls []messages.ToolCall) *MockInferencer {
	m.entries = append(m.entries, inferenceEntry{
		result: messages.InferenceResult{
			Message:   messages.Message{Role: messages.RoleAssistant, ToolCalls: calls},
			ToolCalls: calls,
		},
	})
	return m
}

// CallCount returns the number of times Infer has been called.
func (m *MockInferencer) CallCount() int { return m.callCount }

func (m *MockInferencer) currentEntry() inferenceEntry {
	if len(m.entries) == 0 {
		return inferenceEntry{result: messages.InferenceResult{
			Message: messages.NewTextMessage(messages.RoleAssistant, ""),
		}}
	}
	idx := m.callCount
	if idx >= len(m.entries) {
		idx = len(m.entries) - 1
	}
	return m.entries[idx]
}

// Infer implements messages.Inferencer (non-streaming fallback).
func (m *MockInferencer) Infer(ctx context.Context, req messages.InferenceRequest) (messages.InferenceResult, error) {
	snapshot := make([]messages.Message, len(req.Messages))
	copy(snapshot, req.Messages)
	m.CapturedMessages = append(m.CapturedMessages, snapshot)
	entry := m.currentEntry()
	m.callCount++
	return entry.result, nil
}

// InferStream implements messages.Inferencer (streaming path).
// Emits properly ordered TEXT.START / TEXT.DELTA / TEXT.END events with
// Role=RoleAssistant for text responses, or TOOLCALL.START/DELTA/END for
// tool-call responses, followed by MESSAGE.END.
// When the entry was created with AddChunkedTextResponse, each chunk is emitted
// as its own TEXT.DELTA event.
func (m *MockInferencer) InferStream(ctx context.Context, req messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	ch := make(chan messages.StreamMessage, 64)

	// Record the call and advance the counter via Infer.
	result, err := m.Infer(ctx, req)
	entry := m.streamedEntry()
	if err != nil {
		ch <- messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValue(err.Error())}
		close(ch)
		return ch, nil
	}

	emitToolCallEvents(ch, result.ToolCalls)
	// Text response: emit REASONING then TEXT events (only when there are no tool calls).
	if len(result.ToolCalls) == 0 {
		// Emit reasoning deltas first so the ordering layer records a reasoning message.
		emitReasoningEvents(ch, entry.reasoningChunks)
		if text := result.Message.TextContent(); text != "" {
			emitTextEvents(ch, text, entry.chunks)
		}
		// Binary responses: emit IMAGE, AUDIO, VIDEO, then FILE events for the
		// matching content parts. When chunks are set for a modality, each chunk
		// is emitted as its own DELTA event; otherwise the full bytes are sent in
		// a single DELTA.
		for _, stream := range binaryPartStreams(entry) {
			stream.emit(ch, result.Message.ContentParts)
		}
	}

	ch <- messages.StreamMessage{
		Type:               messages.StreamTypeMessageEnd,
		ActorProvidedIndex: 0,
		Value:              messages.NewMessageEndValue(result.TokenUsage),
	}
	close(ch)
	return ch, nil
}

// streamedEntry returns the entry consumed by the call Infer just recorded
// (callCount was already incremented by Infer).
func (m *MockInferencer) streamedEntry() inferenceEntry {
	entryIdx := m.callCount - 1
	if entryIdx >= len(m.entries) {
		entryIdx = len(m.entries) - 1
	}
	if entryIdx >= 0 && len(m.entries) > 0 {
		return m.entries[entryIdx]
	}
	return inferenceEntry{}
}

func emitToolCallEvents(ch chan<- messages.StreamMessage, calls []messages.ToolCall) {
	for i, tc := range calls {
		ch <- messages.StreamMessage{Type: messages.StreamTypeToolCallStart, ActorProvidedIndex: i, Value: messages.NewToolCallStartValue(tc.ID, tc.Name)}
		if tc.Arguments != "" {
			ch <- messages.StreamMessage{Type: messages.StreamTypeToolCallDelta, ActorProvidedIndex: i, Value: messages.NewToolCallDeltaValue(tc.Arguments)}
		}
		ch <- messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, ActorProvidedIndex: i, Value: messages.NewToolCallEndValue(tc.ID, tc.Name, tc.Arguments)}
	}
}

func assistantEvent(kind messages.StreamMessageType, index int, value messages.StreamMessageValue) messages.StreamMessage {
	return messages.StreamMessage{Type: kind, ActorProvidedIndex: index, Role: messages.RoleAssistant, Value: value}
}

func emitReasoningEvents(ch chan<- messages.StreamMessage, chunks []string) {
	if len(chunks) == 0 {
		return
	}
	ch <- assistantEvent(messages.StreamTypeReasoningStart, 0, messages.NewReasoningStartValue())
	for i, chunk := range chunks {
		ch <- assistantEvent(messages.StreamTypeReasoningDelta, i, messages.NewReasoningDeltaValue(chunk))
	}
	ch <- assistantEvent(messages.StreamTypeReasoningEnd, len(chunks), messages.NewReasoningEndValue())
}

// emitTextEvents emits each chunk as a separate TEXT.DELTA, or the full text
// as one TEXT.DELTA when the entry is not chunked.
func emitTextEvents(ch chan<- messages.StreamMessage, text string, chunks []string) {
	ch <- assistantEvent(messages.StreamTypeTextStart, 0, messages.NewTextStartValue())
	if len(chunks) == 0 {
		chunks = []string{text}
	}
	for i, chunk := range chunks {
		ch <- assistantEvent(messages.StreamTypeTextDelta, i, messages.NewTextDeltaValue(chunk))
	}
	ch <- assistantEvent(messages.StreamTypeTextEnd, 0, messages.NewTextEndValue())
}

// binaryPartStream describes the START/DELTA/END events for one binary
// content-part modality.
type binaryPartStream struct {
	start, delta, end messages.StreamMessageType
	chunks            [][]byte
	// open returns the part bytes and START value when part is this modality.
	open       func(part messages.ContentPart) ([]byte, messages.StreamMessageValue, bool)
	deltaValue func(content []byte) messages.StreamMessageValue
	endValue   func() messages.StreamMessageValue
}

func binaryPartStreams(entry inferenceEntry) []binaryPartStream {
	return []binaryPartStream{
		{
			start: messages.StreamTypeImageStart, delta: messages.StreamTypeImageDelta, end: messages.StreamTypeImageEnd, chunks: entry.imageChunks,
			open: func(part messages.ContentPart) ([]byte, messages.StreamMessageValue, bool) {
				ip, ok := part.(messages.ImagePart)
				return ip.Bytes, messages.NewImageStartValue(ip.MediaType), ok
			},
			deltaValue: func(content []byte) messages.StreamMessageValue { return messages.NewImageDeltaValue(content) },
			endValue:   func() messages.StreamMessageValue { return messages.NewImageEndValue() },
		},
		{
			start: messages.StreamTypeAudioStart, delta: messages.StreamTypeAudioDelta, end: messages.StreamTypeAudioEnd, chunks: entry.audioChunks,
			open: func(part messages.ContentPart) ([]byte, messages.StreamMessageValue, bool) {
				ap, ok := part.(messages.AudioPart)
				return ap.Bytes, messages.NewAudioStartValue(), ok
			},
			deltaValue: func(content []byte) messages.StreamMessageValue { return messages.NewAudioDeltaValue(content) },
			endValue:   func() messages.StreamMessageValue { return messages.NewAudioEndValue() },
		},
		{
			start: messages.StreamTypeVideoStart, delta: messages.StreamTypeVideoDelta, end: messages.StreamTypeVideoEnd, chunks: entry.videoChunks,
			open: func(part messages.ContentPart) ([]byte, messages.StreamMessageValue, bool) {
				vp, ok := part.(messages.VideoPart)
				return vp.Bytes, messages.NewVideoStartValue(vp.MediaType), ok
			},
			deltaValue: func(content []byte) messages.StreamMessageValue { return messages.NewVideoDeltaValue(content) },
			endValue:   func() messages.StreamMessageValue { return messages.NewVideoEndValue() },
		},
		{
			start: messages.StreamTypeFileStart, delta: messages.StreamTypeFileDelta, end: messages.StreamTypeFileEnd, chunks: entry.fileChunks,
			open: func(part messages.ContentPart) ([]byte, messages.StreamMessageValue, bool) {
				fp, ok := part.(messages.FilePart)
				return fp.Bytes, messages.NewFileStartValue(fp.MediaType, fp.Name), ok
			},
			deltaValue: func(content []byte) messages.StreamMessageValue { return messages.NewFileDeltaValue(content) },
			endValue:   func() messages.StreamMessageValue { return messages.NewFileEndValue() },
		},
	}
}

// emit streams every non-empty part of this modality. Chunked deltas are
// indexed by chunk; an unchunked delta shares the part index.
func (s binaryPartStream) emit(ch chan<- messages.StreamMessage, parts []messages.ContentPart) {
	for i, part := range parts {
		payload, startValue, ok := s.open(part)
		if !ok || len(payload) == 0 {
			continue
		}
		ch <- assistantEvent(s.start, i, startValue)
		if len(s.chunks) > 0 {
			for j, chunk := range s.chunks {
				ch <- assistantEvent(s.delta, j, s.deltaValue(chunk))
			}
		} else {
			ch <- assistantEvent(s.delta, i, s.deltaValue(payload))
		}
		ch <- assistantEvent(s.end, i, s.endValue())
	}
}

// ---------------------------------------------------------------------------
// MockToolExecutor
// ---------------------------------------------------------------------------

// MockToolExecutor is a configurable test double for messages.ToolExecutor.
// Use AddResult to configure per-tool responses. CallLog records every invocation.
// Use SetToolResponse to return rich ContentParts (e.g. ImagePart) for a tool; it takes precedence over AddResult.
// Safe for concurrent use from ToolRunner's parallel executeBatch.
type MockToolExecutor struct {
	mu            sync.Mutex
	Results       map[string]string
	CustomResults map[string]messages.ToolCallResponse // keyed by tool name; ToolCallID is set from call.ID at Execute time
	CallLog       []messages.ToolCall
}

// NewMockToolExecutor returns an empty MockToolExecutor ready for use.
func NewMockToolExecutor() *MockToolExecutor {
	return &MockToolExecutor{
		Results:       make(map[string]string),
		CustomResults: make(map[string]messages.ToolCallResponse),
	}
}

// AddResult registers a string result for the named tool.
func (m *MockToolExecutor) AddResult(toolName, result string) *MockToolExecutor {
	m.mu.Lock()
	m.Results[toolName] = result
	m.mu.Unlock()
	return m
}

// SetToolResponse registers a full ToolCallResponse for the named tool (e.g. with ContentParts for image/audio).
// ToolCallID is filled from the call at Execute time.
func (m *MockToolExecutor) SetToolResponse(toolName string, resp messages.ToolCallResponse) *MockToolExecutor {
	m.mu.Lock()
	m.CustomResults[toolName] = resp
	m.mu.Unlock()
	return m
}

// Calls returns a snapshot of tool calls observed by Execute.
func (m *MockToolExecutor) Calls() []messages.ToolCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	calls := make([]messages.ToolCall, len(m.CallLog))
	copy(calls, m.CallLog)
	return calls
}

// Execute implements messages.ToolExecutor.
func (m *MockToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	m.mu.Lock()
	m.CallLog = append(m.CallLog, call)
	if custom, ok := m.CustomResults[call.Name]; ok {
		custom.ToolCallID = call.ID
		m.mu.Unlock()
		return custom, nil
	}
	content := m.Results[call.Name]
	m.mu.Unlock()
	return messages.ToolCallResponse{
		ToolCallID: call.ID,
		Content:    content,
	}, nil
}

// ---------------------------------------------------------------------------
// Scenario
// ---------------------------------------------------------------------------

// Scenario wraps a configured AgentLoop together with the mock inferencer and
// tool executor used to drive it. It provides convenience methods for executing
// turns and assertion helpers for validating message sequences and delta events.
//
// Create a Scenario with NewScenario; the returned value is ready to use.
type Scenario struct {
	t       *testing.T
	Loop    agentloop.AgenticLoop
	Inf     *MockInferencer
	Tool    *MockToolExecutor
	Timeout time.Duration
}

// NewScenario creates a Scenario with the supplied mock inferencer and tool
// executor. Additional agentloop.Option values (e.g. WithSystemPrompt,
// WithTools) can be passed to customise the loop further.
//
// The scenario uses a 10-second per-operation timeout by default.
func NewScenario(t *testing.T, inf *MockInferencer, tool *MockToolExecutor, opts ...agentloop.Option) *Scenario {
	t.Helper()

	allOpts := []agentloop.Option{
		agentloop.WithInferencer(inf),
		agentloop.WithToolExecutor(tool),
		// agentloop.WithLogger(test_logging.NewPrintLogger()),
	}
	allOpts = append(allOpts, opts...)

	loop, err := agentloop.New(allOpts...)
	if err != nil {
		t.Fatalf("NewScenario: failed to create loop: %v", err)
	}

	return &Scenario{
		t:       t,
		Loop:    loop,
		Inf:     inf,
		Tool:    tool,
		Timeout: 1000 * time.Second,
	}
}

// Execute runs a single non-streaming turn with the given user message and
// returns the result. The test is failed immediately if Execute returns an error.
func (s *Scenario) Execute(message string) agentloop.ExecuteResult {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), s.Timeout)
	defer cancel()
	result, err := s.Loop.Execute(ctx, agentloop.NewExecuteInput(message))
	if err != nil {
		s.t.Fatalf("Scenario.Execute(%q): %v", message, err)
	}
	return result
}

// streamTextFromEvents drains stream and returns concatenated TEXT.DELTA and REASONING.DELTA content.
func streamTextFromEvents(stream agentloop.Stream) string {
	var buf strings.Builder
	for stream.HasNext() {
		evt := stream.Response()
		switch evt.Type {
		case messages.StreamTypeTextDelta:
			if v, ok := evt.Value.(*messages.TextDeltaValue); ok {
				if evt.Role == messages.RoleAssistant {
					buf.WriteString(v.Content)
				}

			}
		case messages.StreamTypeReasoningDelta:
			if v, ok := evt.Value.(*messages.ReasoningDeltaValue); ok {
				if evt.Role == messages.RoleAssistant {
					buf.WriteString(v.Content)
				}
			}
		}
	}
	return buf.String()
}

// ExecuteStreamingText runs a single streaming turn, drains the event stream,
// and returns the concatenated text (reasoning + text) together with the full message list.
// The test is failed immediately on any error.
func (s *Scenario) ExecuteStreamingText(message string) (streamText string) {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), s.Timeout)
	defer cancel()

	result, err := s.Loop.ExecuteStreaming(ctx, agentloop.NewExecuteInput(message))
	if err != nil {
		s.t.Fatalf("Scenario.ExecuteStreaming(%q): %v", message, err)
	}

	streamText = streamTextFromEvents(result.EventStream)
	return streamText
}

// History returns the full conversation history (delegates to GetConversationHistory).
func (s *Scenario) History() []messages.Message {
	return s.Loop.GetConversationHistory()
}

// Deltas returns the full conversation delta buffer (delegates to GetConversationDeltas).
func (s *Scenario) Deltas() []messages.StreamMessage {
	return s.Loop.GetConversationDeltas()
}

// ---------------------------------------------------------------------------
// Assertion helpers
// ---------------------------------------------------------------------------

// ExpectedMessage describes one message in an expected conversation sequence.
type ExpectedMessage struct {
	Role         messages.Role
	Text         string // if non-empty, TextContent() must match exactly
	HasToolCalls bool   // if true, message must have at least one ToolCall
	IsToolResult bool   // shorthand for Role==RoleTool
}

// AssertMessages validates that msgs matches the expected sequence in length,
// role, text content, and tool-call presence.
func AssertMessages(t *testing.T, msgs []messages.Message, expected []ExpectedMessage) {
	t.Helper()
	if len(msgs) != len(expected) {
		t.Errorf("message count: got %d, want %d", len(msgs), len(expected))
		for i, m := range msgs {
			t.Logf("  [%d] role=%s text=%q toolCalls=%d", i, m.Role, m.TextContent(), len(m.ToolCalls))
		}
		return
	}
	for i, exp := range expected {
		m := msgs[i]
		if exp.Role != "" && m.Role != exp.Role {
			t.Errorf("message[%d].Role: got %q, want %q", i, m.Role, exp.Role)
		}
		if exp.Text != "" && m.TextContent() != exp.Text {
			t.Errorf("message[%d].Text: got %q, want %q", i, m.TextContent(), exp.Text)
		}
		if exp.HasToolCalls && len(m.ToolCalls) == 0 {
			t.Errorf("message[%d]: expected at least one tool call, got none", i)
		}
		if exp.IsToolResult && m.Role != messages.RoleTool {
			t.Errorf("message[%d]: expected RoleTool, got %q", i, m.Role)
		}
	}
}

// AssertDeltaContains checks that the delta slice contains at least one event
// matching every entry in required (checked in order, subsequence matching).
// This is deliberately lenient about extra events between matches so that
// non-deterministic LOOP.END placement does not cause spurious failures.
func AssertDeltaContains(t *testing.T, deltas []messages.StreamMessage, required []ExpectedDelta) {
	t.Helper()
	pos := 0
	for _, req := range required {
		found := false
		for pos < len(deltas) {
			d := deltas[pos]
			pos++
			typeMatch := req.Type == "" || d.Type == req.Type
			roleMatch := req.Role == "" || d.Role == req.Role
			textMatch := req.TextContent == "" || deltaTextContent(d) == req.TextContent
			if typeMatch && roleMatch && textMatch {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("AssertDeltaContains: no match for {Type:%q Role:%q Text:%q} from position %d in %d total deltas",
				req.Type, req.Role, req.TextContent, pos, len(deltas))
			for i, d := range deltas {
				t.Logf("  delta[%d] type=%s role=%s text=%q", i, d.Type, d.Role, deltaTextContent(d))
			}
			return
		}
	}
}

// ExpectedDelta describes one entry in an expected delta subsequence.
// Zero-value fields are treated as wildcards.
type ExpectedDelta struct {
	Type        messages.StreamMessageType
	Role        messages.Role
	TextContent string // matched against TEXT.DELTA content if non-empty
}

// deltaTextContent extracts text from a TEXT.DELTA value, returning "" otherwise.
func deltaTextContent(d messages.StreamMessage) string {
	if v, ok := d.Value.(*messages.TextDeltaValue); ok {
		return v.Content
	}
	return ""
}
