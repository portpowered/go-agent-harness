package services

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/agent"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

var updateChatGolden = flag.Bool("update", false, "update chat golden files")

var chatTimestampPattern = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})\b`)

type chatTestHarness struct {
	model       ChatModel
	globalFlags *flags.GlobalFlags
	askFlags    *flags.AskFlags
	out         *bytes.Buffer
	errOut      *bytes.Buffer
	inferencer  *chatTestInferencer
}

type chatTestInferencer struct {
	responses []string
	index     int
	calls     int
	streamErr error
}

func (i *chatTestInferencer) Infer(context.Context, messages.InferenceRequest) (messages.InferenceResult, error) {
	i.calls++
	if i.streamErr != nil {
		return messages.InferenceResult{}, i.streamErr
	}
	return messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, i.nextResponse())}, nil
}

func (i *chatTestInferencer) InferStream(ctx context.Context, req messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	i.calls++
	if i.streamErr != nil {
		return nil, i.streamErr
	}
	text := i.nextResponse()
	stream := make(chan messages.StreamMessage, 4)
	stream <- messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()}
	stream <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(text)}
	stream <- messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()}
	stream <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})}
	close(stream)
	return stream, nil
}

func (i *chatTestInferencer) nextResponse() string {
	if len(i.responses) == 0 {
		return ""
	}
	if i.index >= len(i.responses) {
		return i.responses[len(i.responses)-1]
	}
	response := i.responses[i.index]
	i.index++
	return response
}

type chatTestToolExecutor struct{}

func (chatTestToolExecutor) Execute(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
	return messages.ToolCallResponse{}, nil
}

func newChatTestHarness(t *testing.T, responses ...string) *chatTestHarness {
	t.Helper()
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	askFlags := flags.NewAskFlags()
	askFlags.NoSystemInformation = true
	inferencer := &chatTestInferencer{responses: responses}
	executor := agent.NewExecutor(chatTestToolExecutor{}, nil, inferencer, true)
	cfg := BuildAgentConfigFromFlags(globalFlags, askFlags, nil, "")
	sessionID, err := executor.NewChatSessionID(cfg)
	if err != nil {
		t.Fatalf("NewChatSessionID: %v", err)
	}
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	model := NewChatModel(executor, sessionID, globalFlags, askFlags, context.Background(), out, errOut)
	updated, _ := model.Update(FocusInputMsg{})
	return &chatTestHarness{
		model:       updated.(ChatModel),
		globalFlags: globalFlags,
		askFlags:    askFlags,
		out:         out,
		errOut:      errOut,
		inferencer:  inferencer,
	}
}

func typeChatInput(model ChatModel, input string) ChatModel {
	for _, r := range input {
		keyType := tea.KeyRunes
		if r == ' ' {
			keyType = tea.KeySpace
		}
		updated, _ := model.Update(tea.KeyMsg{Type: keyType, Runes: []rune{r}})
		model = updated.(ChatModel)
	}
	return model
}

func submitChatInput(model ChatModel, input string) ChatModel {
	model = typeChatInput(model, input)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(ChatModel)
	return drainChatCommands(model, cmd)
}

func drainChatCommands(model ChatModel, first tea.Cmd) ChatModel {
	commands := []tea.Cmd{first}
	for len(commands) > 0 {
		cmd := commands[0]
		commands = commands[1:]
		if cmd == nil {
			continue
		}
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			commands = append(commands, batch...)
			continue
		}
		if msg == nil {
			continue
		}
		updated, next := model.Update(msg)
		model = updated.(ChatModel)
		if next != nil {
			commands = append(commands, next)
		}
	}
	return model
}

func normalizeChatTranscript(transcript string) string {
	lines := strings.Split(transcript, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return chatTimestampPattern.ReplaceAllString(strings.Join(lines, "\n"), "<timestamp>")
}

func TestChatREPL_S1ScriptedInput_S3Golden(t *testing.T) {
	harness := newChatTestHarness(t, "Scripted assistant response.")
	model := harness.model
	model = submitChatInput(model, "hello from scripted input")
	model = submitChatInput(model, "/help")
	model = submitChatInput(model, "exit")

	transcript := strings.TrimSpace(model.ViewHistory() + "\n" + harness.out.String())
	if !strings.Contains(transcript, "You: hello from scripted input") || !strings.Contains(transcript, "Scripted assistant response.") {
		t.Fatalf("scripted transcript lost input or response:\n%s", transcript)
	}
	if !strings.Contains(transcript, "/system") || !strings.Contains(harness.out.String(), "Goodbye!") {
		t.Fatalf("scripted transcript did not execute the command and exit:\n%s", transcript)
	}

	goldenPath := filepath.Join("testdata", "chat", "s1_transcript.golden")
	got := normalizeChatTranscript(transcript)
	if *updateChatGolden {
		if err := os.WriteFile(goldenPath, []byte(got+"\n"), 0600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	wantBytes, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update only for an intentional fixture change)", goldenPath, err)
	}
	want := strings.TrimSpace(normalizeChatTranscript(string(wantBytes)))
	if got != want {
		t.Fatalf("transcript differs from %s (-got +want):\n%s\n--- WANT ---\n%s", goldenPath, got, want)
	}
}

func TestChatREPL_KeyboardAndStreamBranches(t *testing.T) {
	harness := newChatTestHarness(t, "streamed response")
	model := harness.model
	if model.Init() == nil || model.SessionID() == "" || model.IsQuitting() {
		t.Fatal("new chat model did not initialize with a live session")
	}
	updated, _ := model.Update(FocusInputMsg{})
	model = updated.(ChatModel)
	if !model.InputFocused() {
		t.Fatal("FocusInputMsg did not focus the input")
	}
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 10, Height: 4})
	model = updated.(ChatModel)
	if model.width != 20 {
		t.Fatalf("narrow terminal width = %d, want 20", model.width)
	}
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	model = updated.(ChatModel)
	if model.width != 100 || model.height != 20 {
		t.Fatalf("window size = (%d, %d), want (100, 20)", model.width, model.height)
	}

	model.input.SetValue("/")
	model.updateCmdAutocomplete()
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(ChatModel)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(ChatModel)
	if !strings.HasPrefix(model.input.Value(), "/") {
		t.Fatalf("command completion changed input to %q", model.input.Value())
	}
	model.input.SetValue("/")
	model.updateCmdAutocomplete()
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(ChatModel)
	model.input.SetValue("plain text")
	model.updateCmdAutocomplete()
	if model.cmdAutocomplete.IsActive() {
		t.Fatal("command autocomplete remained active outside slash context")
	}

	model = submitChatInput(model, "hello")
	if !strings.Contains(model.ViewHistory(), "streamed response") || harness.inferencer.calls != 1 {
		t.Fatalf("normal input did not execute one streamed turn: calls=%d history=%s", harness.inferencer.calls, model.ViewHistory())
	}

	model = submitChatInput(model, " ")
	if harness.inferencer.calls != 1 {
		t.Fatal("whitespace-only input invoked the inferencer")
	}

	model = submitChatInput(model, "exit")
	if !model.IsQuitting() || !strings.Contains(harness.out.String(), "Goodbye!") || model.View() != "" {
		t.Fatalf("exit state = quitting %t, view %q, output %q", model.IsQuitting(), model.View(), harness.out.String())
	}

	ctrlC := newChatTestHarness(t).model
	updated, cmd := ctrlC.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	ctrlC = updated.(ChatModel)
	if !ctrlC.IsQuitting() || cmd == nil {
		t.Fatal("Ctrl+C did not quit with a command")
	}
}

func TestChatREPL_ApplyStreamEventsAndDrain(t *testing.T) {
	harness := newChatTestHarness(t)
	model := harness.model
	model.applyStreamEvent(messages.StreamMessage{Type: messages.StreamTypeReasoningStart})
	model.applyStreamEvent(messages.StreamMessage{Type: messages.StreamTypeReasoningDelta, Value: messages.NewReasoningDeltaValue("inspect")})
	model.applyStreamEvent(messages.StreamMessage{Type: messages.StreamTypeReasoningEnd})
	model.applyStreamEvent(messages.StreamMessage{Type: messages.StreamTypeToolCallStart, Value: messages.NewToolCallStartValue("id", "lookup")})
	model.applyStreamEvent(messages.StreamMessage{Type: messages.StreamTypeImageStart, Role: messages.RoleTool})
	model.applyStreamEvent(messages.StreamMessage{Type: messages.StreamTypeAudioStart, ToolCallId: "audio"})
	model.applyStreamEvent(messages.StreamMessage{Type: messages.StreamTypeVideoStart, Role: messages.RoleTool})
	model.applyStreamEvent(messages.StreamMessage{Type: messages.StreamTypeFileStart, Role: messages.RoleTool, Value: messages.NewFileStartValue("text/plain", "result.txt")})
	model.applyStreamEvent(messages.StreamMessage{Type: messages.StreamTypeFileStart, Role: messages.RoleTool, Value: messages.NewFileStartValue("text/plain", "")})
	model.applyStreamEvent(messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleTool})
	model.applyStreamEvent(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleTool, Value: messages.NewTextDeltaValue("tool output")})
	model.applyStreamEvent(messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleTool})
	model.applyStreamEvent(messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant})
	model.applyStreamEvent(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("assistant output")})
	model.applyStreamEvent(messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant})
	// The reasoning end above must leave thinking inactive while preserving assistant text.
	if model.thinkingActive || model.assistantPartial != "assistant output" {
		t.Fatalf("stream state = thinking %t assistant %q", model.thinkingActive, model.assistantPartial)
	}
	if len(model.currentTurnLines) < 8 || !strings.Contains(model.currentTurnLines[0].content, "Thinking") || !strings.Contains(model.currentTurnLines[len(model.currentTurnLines)-1].content, "tool output") {
		t.Fatalf("stream lines = %#v, want thinking, media, tool, and result lines", model.currentTurnLines)
	}

	stream := &chatTestStream{events: []messages.StreamMessage{
		{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("drained")},
		{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()},
	}}
	updated, cmd := model.Update(streamReadyMsg{stream: stream})
	model = updated.(ChatModel)
	model = drainChatCommands(model, cmd)
	if stream.closed == false || !strings.Contains(model.ViewHistory(), "drained") {
		t.Fatalf("stream was not drained and committed: closed=%t history=%s", stream.closed, model.ViewHistory())
	}

	errorMessage := errors.New("stream startup failed")
	updated, cmd = model.Update(streamReadyMsg{err: errorMessage})
	model = updated.(ChatModel)
	if cmd != nil || !strings.Contains(harness.errOut.String(), errorMessage.Error()) {
		t.Fatalf("stream-ready error = cmd %v stderr %q", cmd, harness.errOut.String())
	}

	harness = newChatTestHarness(t)
	harness.inferencer.streamErr = errorMessage
	model = submitChatInput(harness.model, "fails to start")
	if !strings.Contains(harness.errOut.String(), errorMessage.Error()) || model.IsQuitting() {
		t.Fatalf("stream execution error not rendered without quitting: stderr=%q", harness.errOut.String())
	}
}

func TestChatREPL_AtFileErrorUsesRealInputSurface(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	harness := newChatTestHarness(t, "must not run")
	model := submitChatInput(harness.model, "@missing.txt explain this")
	history := model.ViewHistory()
	if !strings.Contains(history, "File not found: missing.txt") || strings.Contains(history, "must not run") || harness.inferencer.calls != 0 {
		t.Fatalf("missing at-file reference was not rejected at the REPL boundary: calls=%d history=%s", harness.inferencer.calls, history)
	}
}

func TestChatREPL_ErrorEventAndEmptyDoneBranches(t *testing.T) {
	harness := newChatTestHarness(t)
	model := harness.model
	stream := &chatTestStream{events: []messages.StreamMessage{{Type: messages.StreamTypeError, Value: messages.NewErrorValue("event failed")}}}
	updated, cmd := model.Update(streamReadyMsg{stream: stream})
	model = updated.(ChatModel)
	model = drainChatCommands(model, cmd)
	if !strings.Contains(harness.errOut.String(), "event failed") {
		t.Fatalf("stream error event was not reported: %q", harness.errOut.String())
	}

	updated, cmd = model.Update(streamDoneMsg{})
	model = updated.(ChatModel)
	if cmd != nil || model.stream != nil || model.assistantPartial != "" {
		t.Fatalf("empty stream completion left state behind: cmd=%v stream=%v assistant=%q", cmd, model.stream, model.assistantPartial)
	}
}

type chatTestStream struct {
	events []messages.StreamMessage
	index  int
	closed bool
}

func (s *chatTestStream) HasNext() bool { return s.index < len(s.events) }

func (s *chatTestStream) Response() agentloop.Response {
	event := s.events[s.index]
	s.index++
	return event
}

func (s *chatTestStream) Err() error { return nil }

func (s *chatTestStream) Outcome() agentloop.StreamOutcome {
	return agentloop.StreamOutcome{Status: agentloop.StreamDrained}
}

func (s *chatTestStream) Close() error {
	s.closed = true
	return nil
}
