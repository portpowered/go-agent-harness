package service

import (
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/conversationlog"
)

type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time { return c.now }

func messageEnd() messages.StreamMessage {
	return messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}
}

func audioDelta(content []byte) messages.StreamMessage {
	return messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue(content)}
}

func textDelta(content string) messages.StreamMessage {
	return messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(content)}
}

func TestServicePreservesFrozenJSONLAndTiming(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)}
	reducer := New(clock)

	reducer.Observe(audioDelta([]byte{1, 2}), true, 0, -1)
	reducer.Observe(audioDelta([]byte{3}), true, 1, -1)
	reducer.Observe(textDelta("typed fallback"), true, -1, -1)
	reducer.Observe(messageEnd(), true, -1, -1)
	reducer.Observe(messages.StreamMessage{Type: messages.StreamTypeInputItemAdded, Role: messages.RoleUser, Value: messages.NewInputItemAddedValue("item-1")}, false, -1, -1)
	reducer.Observe(messages.StreamMessage{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleUser, Value: messages.NewTranscriptDeltaValueForItem("spoken ", "item-1")}, false, -1, -1)

	// A tool-call MESSAGE.END is an intermediate boundary and must not close
	// the spoken turn before execution-boundary events and the continuation.
	reducer.Observe(messages.StreamMessage{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, Value: messages.NewToolCallStartValue("call-1", "inspect_tab")}, false, -1, -1)
	reducer.Observe(messageEnd(), false, -1, -1)
	call := messages.ToolCall{ID: "call-1", Name: "inspect_tab", Arguments: `{"tab":"first"}`}
	reducer.ObserveToolCall(call)
	reducer.ObserveToolResult(call, messages.ToolCallResponse{Content: `{"ok":true}`}, false, &conversationlog.ImageEvidence{
		Path: "images/000.png", Source: "browser", BrowserID: "browser-1", TargetID: "tab-1", MIMEType: "image/png", ByteLength: 7, Width: 3, Height: 2, SHA256: "abc", TypedProjection: "screen",
	})
	reducer.Observe(textDelta("delta response"), false, -1, -1)
	reducer.Observe(messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleAssistant, Value: messages.NewTranscriptEndValue("full response")}, false, -1, -1)
	clock.now = clock.now.Add(1250 * time.Millisecond)
	reducer.Observe(audioDelta([]byte{9, 8}), false, -1, 0)
	clock.now = clock.now.Add(500 * time.Millisecond)
	reducer.Observe(audioDelta([]byte{7}), false, -1, 1)
	reducer.Observe(messageEnd(), false, -1, -1)
	reducer.Observe(messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleUser, Value: messages.NewTranscriptEndValueForItem("spoken full", "item-1")}, false, -1, -1)

	got, err := reducer.JSONL()
	if err != nil {
		t.Fatalf("JSONL: %v", err)
	}
	want := `{"turn_index":1,"input":{"text":"spoken full","audio_bytes":3,"committed":true,"audio_segments":["audio/in-000.pcm","audio/in-001.pcm"]},"response":{"text":"full response","complete":true,"audio_bytes":3,"audio_segments":["audio/out-000.pcm","audio/out-001.pcm"]},"tool_events":[{"sequence":1,"type":"tool_call","tool_call_id":"call-1","tool_name":"inspect_tab","arguments":"{\"tab\":\"first\"}"},{"sequence":2,"type":"tool_result","tool_call_id":"call-1","tool_name":"inspect_tab","status":"completed","content":"{\"ok\":true}","image":{"path":"images/000.png","source":"browser","browser_id":"browser-1","target_id":"tab-1","mime_type":"image/png","byte_length":7,"width":3,"height":2,"sha256":"abc","typed_projection":"screen"}}]}` + "\n"
	if string(got) != want {
		t.Fatalf("JSONL = %q, want %q", got, want)
	}
	timings := reducer.TimingEntries()
	if len(timings) != 1 || timings[0].TurnIndex != 1 || timings[0].CommittedAt != "2026-08-30T12:00:00Z" || timings[0].FirstResponseAudioMS != 1250 {
		t.Fatalf("timing = %+v", timings)
	}
	if strings.Contains(string(got), "committed_at") || strings.Contains(string(got), "first_response_audio") {
		t.Fatalf("deterministic JSONL contains timing: %s", got)
	}
	second, err := reducer.JSONL()
	if err != nil || string(second) != string(got) {
		t.Fatalf("repeated JSONL changed: first=%q second=%q err=%v", got, second, err)
	}
}

func TestServiceCorrelatesLateAndEmptyTranscripts(t *testing.T) {
	reducer := New(nil)
	commit := func(index int) {
		reducer.Observe(audioDelta([]byte{byte(index)}), true, index, -1)
		reducer.Observe(messageEnd(), true, -1, -1)
	}
	item := func(id string) {
		reducer.Observe(messages.StreamMessage{Type: messages.StreamTypeInputItemAdded, Role: messages.RoleUser, Value: messages.NewInputItemAddedValue(id)}, false, -1, -1)
	}
	transcriptDelta := func(text, id string) {
		reducer.Observe(messages.StreamMessage{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleUser, Value: messages.NewTranscriptDeltaValueForItem(text, id)}, false, -1, -1)
	}
	transcriptEnd := func(text, id string) {
		reducer.Observe(messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleUser, Value: messages.NewTranscriptEndValueForItem(text, id)}, false, -1, -1)
	}
	closeReply := func(text string) {
		reducer.Observe(textDelta(text), false, -1, -1)
		reducer.Observe(messageEnd(), false, -1, -1)
	}

	commit(0)
	item("item-1")
	transcriptDelta("first", "item-1")
	closeReply("reply one")
	commit(1)
	item("item-2")
	transcriptDelta(" turn", "item-1")
	transcriptEnd("first turn", "item-1")
	transcriptDelta("second turn", "item-2")
	closeReply("reply two")
	commit(2)
	item("item-3")
	closeReply("reply three")
	transcriptDelta("discarded interim", "item-3")
	transcriptEnd("", "item-3")

	entries := reducer.Entries()
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	for index, want := range []string{"first turn", "second turn", ""} {
		if entries[index].Input.Text != want {
			t.Fatalf("turn %d input = %q, want %q", index+1, entries[index].Input.Text, want)
		}
	}
	if entries[0].Response.Text != "reply one" || entries[1].Response.Text != "reply two" || entries[2].Response.Text != "reply three" {
		t.Fatalf("responses = %+v", entries)
	}
}

func TestServiceBoundsValuesAndDeeplySnapshotsState(t *testing.T) {
	reducer := New(nil)
	assertBoundedToolEvents(t, reducer)
	assertDeepSnapshot(t)
}

func assertBoundedToolEvents(t *testing.T, reducer *Service) {
	t.Helper()
	const exactBytes = conversationlog.MaxToolEventValueBytes
	exact := strings.Repeat("x", exactBytes)
	splitRune := strings.Repeat("界", 21840) + "x" + strings.Repeat("界", 6)
	wantSplitPrefix := strings.Repeat("界", 21840) + "x"
	callExact := messages.ToolCall{ID: "exact", Name: "bounded", Arguments: exact}
	callSplit := messages.ToolCall{ID: "split", Name: "bounded", Arguments: splitRune}
	reducer.ObserveToolCall(callExact)
	reducer.ObserveToolResult(callExact, messages.ToolCallResponse{Content: exact}, false)
	reducer.ObserveToolCall(callSplit)
	reducer.ObserveToolResult(callSplit, messages.ToolCallResponse{Content: splitRune}, true)
	emptyCall := messages.ToolCall{ID: "empty", Name: "bounded"}
	reducer.ObserveToolCall(emptyCall)
	reducer.ObserveToolResult(emptyCall, messages.ToolCallResponse{}, true)

	entries := reducer.Entries()
	if len(entries) != 1 || len(entries[0].ToolEvents) != 6 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].ToolEvents[1].Content != exact || len(entries[0].ToolEvents[1].Content) != exactBytes {
		t.Fatalf("exact content was changed: bytes=%d", len(entries[0].ToolEvents[1].Content))
	}
	bounded := entries[0].ToolEvents[3].Content
	wantBounded := wantSplitPrefix + conversationlog.ToolEventTruncationSuffix
	if bounded != wantBounded || len(bounded) > exactBytes || !utf8.ValidString(bounded) || !strings.HasSuffix(bounded, conversationlog.ToolEventTruncationSuffix) {
		t.Fatalf("bounded content bytes=%d want=%d valid=%t hasSuffix=%t gotPrefix=%t", len(bounded), len(wantBounded), utf8.ValidString(bounded), strings.HasSuffix(bounded, conversationlog.ToolEventTruncationSuffix), strings.HasPrefix(bounded, wantSplitPrefix))
	}
	if entries[0].ToolEvents[5].Status != "failed" || entries[0].ToolEvents[5].Content != "" {
		t.Fatalf("empty failed result = %+v", entries[0].ToolEvents[5])
	}
	raw, err := reducer.JSONL()
	if err != nil || !strings.Contains(string(raw), `"content":""`) {
		t.Fatalf("empty result content was omitted: raw=%s err=%v", raw, err)
	}
}

func assertDeepSnapshot(t *testing.T) {
	t.Helper()
	image := &conversationlog.ImageEvidence{Path: "images/original.png", Source: "browser", MIMEType: "image/png", ByteLength: 3, Width: 1, Height: 1, SHA256: "hash", TypedProjection: "screen"}
	reducer := New(nil)
	reducer.Observe(audioDelta([]byte{1}), true, 0, -1)
	reducer.Observe(messageEnd(), true, -1, -1)
	reducer.ObserveToolResult(messages.ToolCall{ID: "image", Name: "screen"}, messages.ToolCallResponse{Content: "ok"}, false, image)
	image.Path = "mutated-before-snapshot"
	first := reducer.Entries()
	if len(first) != 1 || first[0].ToolEvents[0].Image == nil {
		t.Fatalf("snapshot = %+v", first)
	}
	first[0].Input.AudioSegments[0] = "caller-mutated.pcm"
	first[0].ToolEvents[0].Image.Path = "caller-mutated.png"
	firstRaw, err := reducer.JSONL()
	if err != nil {
		t.Fatalf("snapshot JSONL: %v", err)
	}
	secondRaw, err := reducer.JSONL()
	if err != nil || string(firstRaw) != string(secondRaw) || !strings.Contains(string(firstRaw), "images/original.png") {
		t.Fatalf("rendering was not stable or retained mutated input: %s", firstRaw)
	}
	second := reducer.Entries()
	if second[0].Input.AudioSegments[0] != "audio/in-000.pcm" || second[0].ToolEvents[0].Image.Path != "images/original.png" {
		t.Fatalf("state was aliased through snapshot: %+v", second[0])
	}
}

func TestNewCreatesIndependentReducers(t *testing.T) {
	first := New(nil)
	second := New(nil)
	for _, reducer := range []*Service{first, second} {
		reducer.Observe(audioDelta([]byte{1}), true, 0, -1)
		reducer.Observe(messageEnd(), true, -1, -1)
		reducer.Observe(messages.StreamMessage{Type: messages.StreamTypeInputItemAdded, Role: messages.RoleUser, Value: messages.NewInputItemAddedValue("same-item")}, false, -1, -1)
	}
	first.Observe(messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleUser, Value: messages.NewTranscriptEndValueForItem("first", "same-item")}, false, -1, -1)
	second.Observe(messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleUser, Value: messages.NewTranscriptEndValueForItem("second", "same-item")}, false, -1, -1)
	first.ObserveToolCall(messages.ToolCall{ID: "first-call", Name: "tool"})
	second.ObserveToolCall(messages.ToolCall{ID: "second-call", Name: "tool"})
	if got := first.Entries()[0].ToolEvents[0].Sequence; got != 1 {
		t.Fatalf("first sequence = %d, want 1", got)
	}
	if got := second.Entries()[0].ToolEvents[0].Sequence; got != 1 {
		t.Fatalf("second sequence = %d, want 1", got)
	}
	if got := first.Entries()[0].Input.Text; got != "first" {
		t.Fatalf("first input = %q", got)
	}
	if got := second.Entries()[0].Input.Text; got != "second" {
		t.Fatalf("second input = %q", got)
	}
}

func TestServiceIgnoresInvalidObservationsAndOmitsTimingWithoutClock(t *testing.T) {
	reducer := New(nil)
	reducer.Observe(messages.StreamMessage{Type: messages.StreamTypeAudioDelta}, false, -1, -1)
	reducer.Observe(messages.StreamMessage{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleUser}, false, -1, -1)
	reducer.Observe(messages.StreamMessage{Type: messages.StreamTypeInputItemAdded, Role: messages.RoleUser}, false, -1, -1)
	if got := reducer.Entries(); got != nil {
		t.Fatalf("invalid-only entries = %+v, want nil", got)
	}
	if got := reducer.TimingEntries(); got != nil {
		t.Fatalf("invalid-only timings = %+v, want nil", got)
	}
	if raw, err := reducer.JSONL(); err != nil || raw != nil {
		t.Fatalf("invalid-only JSONL = %q, err=%v", raw, err)
	}
	reducer.Observe(audioDelta([]byte{1}), true, 0, -1)
	reducer.Observe(messageEnd(), true, -1, -1)
	reducer.Observe(audioDelta([]byte{2}), false, -1, 0)
	if got := reducer.TimingEntries(); got != nil {
		t.Fatalf("timing without clock = %+v, want nil", got)
	}
	raw, err := reducer.JSONL()
	if err != nil || strings.Contains(string(raw), "2026-") {
		t.Fatalf("clock leaked into JSONL: raw=%q err=%v", raw, err)
	}
}

func TestServiceRetainsMissingToolIdentityAndPartialTurns(t *testing.T) {
	reducer := New(nil)
	reducer.Observe(messageEnd(), false, -1, -1)
	if got := reducer.Entries(); got != nil {
		t.Fatalf("empty boundary entries = %+v, want nil", got)
	}
	reducer.ObserveToolCall(messages.ToolCall{})
	reducer.ObserveToolResult(messages.ToolCall{}, messages.ToolCallResponse{}, false)
	reducer.Observe(textDelta("partial"), false, -1, -1)
	entries := reducer.Entries()
	if len(entries) != 1 || entries[0].Response.Complete || entries[0].Response.Text != "partial" {
		t.Fatalf("partial entry = %+v", entries)
	}
	raw, err := reducer.JSONL()
	if err != nil {
		t.Fatalf("JSONL: %v", err)
	}
	want := `{"turn_index":1,"input":{"text":"","audio_bytes":0,"committed":false},"response":{"text":"partial","complete":false,"audio_bytes":0},"tool_events":[{"sequence":1,"type":"tool_call","tool_call_id":"","tool_name":"","arguments":""},{"sequence":2,"type":"tool_result","tool_call_id":"","tool_name":"","status":"completed","content":""}]}` + "\n"
	if string(raw) != want {
		t.Fatalf("partial/missing-identity JSONL = %q, want %q", raw, want)
	}
}

func TestServiceConcurrentObservationAndSnapshots(t *testing.T) {
	reducer := New(nil)
	var wg sync.WaitGroup
	for index := 0; index < 8; index++ {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			reducer.Observe(textDelta("text"), false, -1, -1)
			call := messages.ToolCall{ID: "call", Name: "tool", Arguments: "{}"}
			reducer.ObserveToolCall(call)
			reducer.ObserveToolResult(call, messages.ToolCallResponse{Content: "result"}, index%2 == 0)
		}()
	}
	for index := 0; index < 4; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iteration := 0; iteration < 16; iteration++ {
				if _, err := reducer.JSONL(); err != nil {
					t.Errorf("concurrent JSONL: %v", err)
				}
				_ = reducer.Entries()
				_ = reducer.TimingEntries()
			}
		}()
	}
	wg.Wait()
	if len(reducer.Entries()) == 0 {
		t.Fatal("concurrent observations produced no entry")
	}
}
