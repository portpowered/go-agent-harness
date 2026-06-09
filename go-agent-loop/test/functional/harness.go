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
package functional

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
	// Retrieve the entry before callCount was incremented (callCount was already incremented by Infer).
	entryIdx := m.callCount - 1
	if entryIdx >= len(m.entries) {
		entryIdx = len(m.entries) - 1
	}
	var chunks []string
	var reasoningChunks []string
	var imageChunks [][]byte
	var audioChunks [][]byte
	var videoChunks [][]byte
	var fileChunks [][]byte
	if entryIdx >= 0 && len(m.entries) > 0 {
		chunks = m.entries[entryIdx].chunks
		reasoningChunks = m.entries[entryIdx].reasoningChunks
		imageChunks = m.entries[entryIdx].imageChunks
		audioChunks = m.entries[entryIdx].audioChunks
		videoChunks = m.entries[entryIdx].videoChunks
		fileChunks = m.entries[entryIdx].fileChunks
	}

	if err != nil {
		ch <- messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValue(err.Error())}
		close(ch)
		return ch, nil
	}

	// Tool-call response: emit TOOLCALL events.
	for i, tc := range result.ToolCalls {
		ch <- messages.StreamMessage{
			Type:               messages.StreamTypeToolCallStart,
			ActorProvidedIndex: i,
			Value:              messages.NewToolCallStartValue(tc.ID, tc.Name),
		}
		if tc.Arguments != "" {
			ch <- messages.StreamMessage{
				Type:               messages.StreamTypeToolCallDelta,
				ActorProvidedIndex: i,
				Value:              messages.NewToolCallDeltaValue(tc.Arguments),
			}
		}
		ch <- messages.StreamMessage{
			Type:               messages.StreamTypeToolCallEnd,
			ActorProvidedIndex: i,
			Value:              messages.NewToolCallEndValue(tc.ID, tc.Name, tc.Arguments),
		}
	}

	// Text response: emit REASONING then TEXT events (only when there are no tool calls).
	if len(result.ToolCalls) == 0 {
		// Emit reasoning deltas first so the ordering layer records a reasoning message.
		if len(reasoningChunks) > 0 {
			ch <- messages.StreamMessage{
				Type:               messages.StreamTypeReasoningStart,
				ActorProvidedIndex: 0,
				Role:               messages.RoleAssistant,
				Value:              messages.NewReasoningStartValue(),
			}
			for i, chunk := range reasoningChunks {
				ch <- messages.StreamMessage{
					Type:               messages.StreamTypeReasoningDelta,
					ActorProvidedIndex: i,
					Role:               messages.RoleAssistant,
					Value:              messages.NewReasoningDeltaValue(chunk),
				}
			}
			ch <- messages.StreamMessage{
				Type:               messages.StreamTypeReasoningEnd,
				ActorProvidedIndex: len(reasoningChunks),
				Role:               messages.RoleAssistant,
				Value:              messages.NewReasoningEndValue(),
			}
		}
		if text := result.Message.TextContent(); text != "" {
			ch <- messages.StreamMessage{
				Type:               messages.StreamTypeTextStart,
				ActorProvidedIndex: 0,
				Role:               messages.RoleAssistant,
				Value:              messages.NewTextStartValue(),
			}
			if len(chunks) > 0 {
				// Emit each chunk as a separate TEXT.DELTA.
				for i, chunk := range chunks {
					ch <- messages.StreamMessage{
						Type:               messages.StreamTypeTextDelta,
						ActorProvidedIndex: i,
						Role:               messages.RoleAssistant,
						Value:              messages.NewTextDeltaValue(chunk),
					}
				}
			} else {
				ch <- messages.StreamMessage{
					Type:               messages.StreamTypeTextDelta,
					ActorProvidedIndex: 0,
					Role:               messages.RoleAssistant,
					Value:              messages.NewTextDeltaValue(text),
				}
			}
			ch <- messages.StreamMessage{
				Type:               messages.StreamTypeTextEnd,
				ActorProvidedIndex: 0,
				Role:               messages.RoleAssistant,
				Value:              messages.NewTextEndValue(),
			}
		}

		// Image response: emit IMAGE events for any ImagePart in the result.
		// When imageChunks is set (via AddChunkedImageResponse), each chunk is
		// emitted as its own IMAGE.DELTA event; otherwise the full bytes are sent
		// in a single IMAGE.DELTA.
		for i, part := range result.Message.ContentParts {
			if ip, ok := part.(messages.ImagePart); ok && len(ip.Bytes) > 0 {
				ch <- messages.StreamMessage{
					Type:               messages.StreamTypeImageStart,
					ActorProvidedIndex: i,
					Role:               messages.RoleAssistant,
					Value:              messages.NewImageStartValue(ip.MediaType),
				}
				if len(imageChunks) > 0 {
					for j, chunk := range imageChunks {
						ch <- messages.StreamMessage{
							Type:               messages.StreamTypeImageDelta,
							ActorProvidedIndex: j,
							Role:               messages.RoleAssistant,
							Value:              messages.NewImageDeltaValue(chunk),
						}
					}
				} else {
					ch <- messages.StreamMessage{
						Type:               messages.StreamTypeImageDelta,
						ActorProvidedIndex: i,
						Role:               messages.RoleAssistant,
						Value:              messages.NewImageDeltaValue(ip.Bytes),
					}
				}
				ch <- messages.StreamMessage{
					Type:               messages.StreamTypeImageEnd,
					ActorProvidedIndex: i,
					Role:               messages.RoleAssistant,
					Value:              messages.NewImageEndValue(),
				}
			}
		}

		// Audio response: emit AUDIO events for any AudioPart in the result.
		// When audioChunks is set, each chunk is emitted as its own AUDIO.DELTA
		// event; otherwise the full bytes are sent in a single AUDIO.DELTA.
		for i, part := range result.Message.ContentParts {
			if ap, ok := part.(messages.AudioPart); ok && len(ap.Bytes) > 0 {
				ch <- messages.StreamMessage{
					Type:               messages.StreamTypeAudioStart,
					ActorProvidedIndex: i,
					Role:               messages.RoleAssistant,
					Value:              messages.NewAudioStartValue(),
				}
				if len(audioChunks) > 0 {
					for j, chunk := range audioChunks {
						ch <- messages.StreamMessage{
							Type:               messages.StreamTypeAudioDelta,
							ActorProvidedIndex: j,
							Role:               messages.RoleAssistant,
							Value:              messages.NewAudioDeltaValue(chunk),
						}
					}
				} else {
					ch <- messages.StreamMessage{
						Type:               messages.StreamTypeAudioDelta,
						ActorProvidedIndex: i,
						Role:               messages.RoleAssistant,
						Value:              messages.NewAudioDeltaValue(ap.Bytes),
					}
				}
				ch <- messages.StreamMessage{
					Type:               messages.StreamTypeAudioEnd,
					ActorProvidedIndex: i,
					Role:               messages.RoleAssistant,
					Value:              messages.NewAudioEndValue(),
				}
			}
		}

		// Video response: emit VIDEO events for any VideoPart in the result.
		// When videoChunks is set, each chunk is emitted as its own VIDEO.DELTA
		// event; otherwise the full bytes are sent in a single VIDEO.DELTA.
		for i, part := range result.Message.ContentParts {
			if vp, ok := part.(messages.VideoPart); ok && len(vp.Bytes) > 0 {
				ch <- messages.StreamMessage{
					Type:               messages.StreamTypeVideoStart,
					ActorProvidedIndex: i,
					Role:               messages.RoleAssistant,
					Value:              messages.NewVideoStartValue(vp.MediaType),
				}
				if len(videoChunks) > 0 {
					for j, chunk := range videoChunks {
						ch <- messages.StreamMessage{
							Type:               messages.StreamTypeVideoDelta,
							ActorProvidedIndex: j,
							Role:               messages.RoleAssistant,
							Value:              messages.NewVideoDeltaValue(chunk),
						}
					}
				} else {
					ch <- messages.StreamMessage{
						Type:               messages.StreamTypeVideoDelta,
						ActorProvidedIndex: i,
						Role:               messages.RoleAssistant,
						Value:              messages.NewVideoDeltaValue(vp.Bytes),
					}
				}
				ch <- messages.StreamMessage{
					Type:               messages.StreamTypeVideoEnd,
					ActorProvidedIndex: i,
					Role:               messages.RoleAssistant,
					Value:              messages.NewVideoEndValue(),
				}
			}
		}

		// File response: emit FILE events for any FilePart in the result.
		// When fileChunks is set, each chunk is emitted as its own FILE.DELTA
		// event; otherwise the full bytes are sent in a single FILE.DELTA.
		for i, part := range result.Message.ContentParts {
			if fp, ok := part.(messages.FilePart); ok && len(fp.Bytes) > 0 {
				ch <- messages.StreamMessage{
					Type:               messages.StreamTypeFileStart,
					ActorProvidedIndex: i,
					Role:               messages.RoleAssistant,
					Value:              messages.NewFileStartValue(fp.MediaType, fp.Name),
				}
				if len(fileChunks) > 0 {
					for j, chunk := range fileChunks {
						ch <- messages.StreamMessage{
							Type:               messages.StreamTypeFileDelta,
							ActorProvidedIndex: j,
							Role:               messages.RoleAssistant,
							Value:              messages.NewFileDeltaValue(chunk),
						}
					}
				} else {
					ch <- messages.StreamMessage{
						Type:               messages.StreamTypeFileDelta,
						ActorProvidedIndex: i,
						Role:               messages.RoleAssistant,
						Value:              messages.NewFileDeltaValue(fp.Bytes),
					}
				}
				ch <- messages.StreamMessage{
					Type:               messages.StreamTypeFileEnd,
					ActorProvidedIndex: i,
					Role:               messages.RoleAssistant,
					Value:              messages.NewFileEndValue(),
				}
			}
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
