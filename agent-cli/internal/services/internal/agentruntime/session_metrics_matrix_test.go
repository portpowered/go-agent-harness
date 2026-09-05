package agentruntime

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
)

// One scripted turn streams text and audio, then the provider streams a tool
// call (arguments as TOOLCALL.DELTA bytes followed by TOOLCALL.END) after the
// turn completed. The observer must count the delta bytes exactly once in the
// output/tool series, keep per-turn attribution exact, and close the run with
// a terminal metrics matrix that carries provider-reported usage alongside
// the byte totals.
func TestSessionMetricsMatrixCountsToolDeltasAndEmitsTerminalMatrix(t *testing.T) {
	toolArgs := `{"city":"Paris"}`
	sessionInf := &scriptedSessionInferencer{
		events: []messages.StreamMessage{
			{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
			{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("Checking the weather.")},
			{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue([]byte{1, 2, 3, 4, 5, 6})},
			{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18})},
			// The provider streams its tool call after the turn completed
			// (the session runtime never executes tools), so the scripted
			// order mirrors the committed v7a fixture and stays deterministic
			// against the loop's default executor.
			{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, Value: messages.NewToolCallStartValue("call-weather", "get_weather")},
			{Type: messages.StreamTypeToolCallDelta, Role: messages.RoleAssistant, Value: messages.NewToolCallDeltaValue(toolArgs[:8])},
			{Type: messages.StreamTypeToolCallDelta, Role: messages.RoleAssistant, Value: messages.NewToolCallDeltaValue(toolArgs[8:])},
			{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, Value: messages.NewToolCallEndValue("call-weather", "get_weather", toolArgs)},
			{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("scripted-metrics", "done")},
		},
	}
	artifacts := runSessionWithDiagnostics(t, func(opts *SessionRunOptions) {
		opts.ReplayPath = "scripted-metrics-matrix.session.json"
		opts.Provider = "grok"
		opts.Model = "scripted-realtime"
		opts.SessionInferencer = sessionInf
		opts.WaitForClose = true
	})
	if artifacts.runErr != nil {
		t.Fatalf("metrics matrix replay failed: %v", artifacts.runErr)
	}

	tool := artifacts.series(metrics.DirectionOutput, metrics.ModalityTool)
	if tool.EventCount != 2 || tool.TotalBytes != uint64(len(toolArgs)) {
		t.Fatalf("output/tool series = (count=%d, bytes=%d), want (2, %d)", tool.EventCount, tool.TotalBytes, len(toolArgs))
	}
	text := artifacts.series(metrics.DirectionOutput, metrics.ModalityText)
	if text.EventCount != 1 || text.TotalBytes != uint64(len("Checking the weather.")) {
		t.Fatalf("output/text series = (count=%d, bytes=%d), want (1, %d)", text.EventCount, text.TotalBytes, len("Checking the weather."))
	}
	audio := artifacts.series(metrics.DirectionOutput, metrics.ModalityAudio)
	if audio.EventCount != 1 || audio.TotalBytes != 6 {
		t.Fatalf("output/audio series = (count=%d, bytes=%d), want (1, 6)", audio.EventCount, audio.TotalBytes)
	}

	turns := artifacts.turnRecords()
	if len(turns) != 1 {
		t.Fatalf("want exactly one turn record, got %d", len(turns))
	}
	fields := turns[0].Fields
	want := map[string]string{
		"output_text_bytes":  "21",
		"output_audio_bytes": "6",
		"output_tool_bytes":  "0",
	}
	for key, wantValue := range want {
		if got := fields[key]; got != wantValue {
			t.Fatalf("turn record field %q = %q, want %q", key, got, wantValue)
		}
	}

	matrices := artifacts.records.events(SessionDiagnosticEventMetrics)
	if len(matrices) != 1 {
		t.Fatalf("want exactly one %s record, got %d", SessionDiagnosticEventMetrics, len(matrices))
	}
	matrix := matrices[0].Fields
	for key, wantValue := range map[string]string{
		"input_audio_bytes":          "0",
		"input_text_bytes":           "0",
		"output_audio_bytes":         "6",
		"output_text_bytes":          "21",
		"output_tool_bytes":          "16",
		"provider_prompt_tokens":     "11",
		"provider_completion_tokens": "7",
		"provider_total_tokens":      "18",
	} {
		if got := matrix[key]; got != wantValue {
			t.Fatalf("metrics matrix field %q = %q, want %q", key, got, wantValue)
		}
	}
	if failures := artifacts.failureRecords(); len(failures) != 0 {
		t.Fatalf("clean metrics run emitted failure records: %v", failures)
	}
}

// A provider that skips TOOLCALL.DELTA and delivers full arguments on
// TOOLCALL.END still counts every argument byte exactly once. The script ends
// before MESSAGE.END so the unexecutable tool call never reaches the loop's
// default executor; the observer seam must have counted it regardless.
func TestSessionMetricsMatrixCountsToolArgumentsWithoutDeltas(t *testing.T) {
	toolArgs := `{"location":"Berlin","unit":"celsius"}`
	sessionInf := &scriptedSessionInferencer{
		events: []messages.StreamMessage{
			{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
			{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, Value: messages.NewToolCallStartValue("call-loc", "locate_city")},
			{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, Value: messages.NewToolCallEndValue("call-loc", "locate_city", toolArgs)},
			{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("scripted-tool-end-only", "done")},
		},
	}
	artifacts := runSessionWithDiagnostics(t, func(opts *SessionRunOptions) {
		opts.ReplayPath = "scripted-tool-end-only.session.json"
		opts.Provider = "grok"
		opts.Model = "scripted-realtime"
		opts.SessionInferencer = sessionInf
		opts.WaitForClose = true
	})
	if artifacts.runErr != nil {
		t.Fatalf("tool-end replay failed: %v", artifacts.runErr)
	}
	tool := artifacts.series(metrics.DirectionOutput, metrics.ModalityTool)
	if tool.EventCount != 1 || tool.TotalBytes != uint64(len(toolArgs)) {
		t.Fatalf("output/tool series = (count=%d, bytes=%d), want (1, %d)", tool.EventCount, tool.TotalBytes, len(toolArgs))
	}
	matrices := artifacts.records.events(SessionDiagnosticEventMetrics)
	if len(matrices) != 1 {
		t.Fatalf("want exactly one metrics matrix record, got %d", len(matrices))
	}
	if got := matrices[0].Fields["output_tool_bytes"]; got != "38" {
		t.Fatalf("metrics matrix output_tool_bytes = %q, want \"38\"", got)
	}
	if _, ok := matrices[0].Fields["provider_total_tokens"]; ok {
		t.Fatalf("zero-valued provider usage must stay absent from the matrix: %v", matrices[0].Fields)
	}
}
