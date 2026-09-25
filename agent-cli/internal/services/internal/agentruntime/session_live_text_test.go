package agentruntime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	agentruntime "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime"
	sessionservicewire "github.com/portpowered/go-agent-harness/agent-cli/internal/services/wire"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/test/functional"
	audioiowire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
	sessionclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func TestSessionCommandHelpExposesPromptSeed(t *testing.T) {
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), newInjectedSessionService(sessionservicewire.SessionDependencies{Clock: sessionclock.Real{}}), nil).Generate()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("session --help: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "--prompt string") || !strings.Contains(got, "Seed the realtime session with text") {
		t.Fatalf("session help does not describe --prompt:\n%s", got)
	}
}

func TestSessionCommandPromptPresenceMatrix(t *testing.T) {
	longPrompt := strings.Repeat("long-", 4096) + "終"
	cases := []struct {
		name          string
		args          []string
		wantText      string
		wantTextEvent bool
	}{
		{
			name:          "absent preserves positional text",
			args:          []string{"--replay", "synthetic.json", "legacy positional text"},
			wantText:      "legacy positional text",
			wantTextEvent: true,
		},
		{
			name:          "absent with no positional text sends no seed",
			args:          []string{"--replay", "synthetic.json"},
			wantTextEvent: false,
		},
		{
			name:          "present empty",
			args:          []string{"--replay", "synthetic.json", "--prompt="},
			wantText:      "",
			wantTextEvent: true,
		},
		{
			name:          "present whitespace",
			args:          []string{"--replay", "synthetic.json", "--prompt= \t  "},
			wantText:      " \t  ",
			wantTextEvent: true,
		},
		{
			name:          "present long",
			args:          []string{"--replay", "synthetic.json", "--prompt", longPrompt},
			wantText:      longPrompt,
			wantTextEvent: true,
		},
		{
			name:          "present newline and non-ascii",
			args:          []string{"--replay", "synthetic.json", "--prompt", "第一行\nδεύτερη\n🙂"},
			wantText:      "第一行\nδεύτερη\n🙂",
			wantTextEvent: true,
		},
		{
			name:          "present flag overrides positional text",
			args:          []string{"--replay", "synthetic.json", "--prompt", "flag text", "positional text"},
			wantText:      "flag text",
			wantTextEvent: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runPromptPresenceCase(t, tc.args, tc.wantText, tc.wantTextEvent)
		})
	}
}

// runPromptPresenceCase runs the session command against a mock provider and
// asserts whether exactly one text seed with the expected value is sent.
func runPromptPresenceCase(t *testing.T, args []string, wantText string, wantTextEvent bool) {
	t.Helper()
	inf := functional.NewMockSessionInferencer()
	t.Cleanup(inf.Close)
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), newInjectedSessionService(sessionservicewire.SessionDependencies{Clock: sessionclock.Real{}, SessionInferencer: inf}), nil).Generate()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	result := make(chan error, 1)
	go func() {
		result <- cmd.ExecuteContext(context.Background())
	}()
	if wantTextEvent {
		assertOneTextSeed(t, inf, wantText)
		inf.AddServerEvent(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("prompt test response")})
		inf.AddServerEvent(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	} else {
		if _, ok := inf.WaitForSentMessage(messages.StreamTypeTextDelta, 2*time.Second); ok {
			t.Fatal("absent prompt unexpectedly sent a text seed")
		}
		inf.Close()
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("RunSessionWithTextSeed: %v", err)
		}
	case <-time.After(3 * time.Second):
		inf.Close()
		t.Fatal("timed out waiting for session completion")
	}
	inf.Close()
}

func assertOneTextSeed(t *testing.T, inf *functional.MockSessionInferencer, wantText string) {
	t.Helper()
	msg, ok := inf.WaitForSentMessage(messages.StreamTypeTextDelta, 2*time.Second)
	if !ok {
		t.Fatal("timed out waiting for the session text seed")
	}
	value, ok := msg.Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("seed value type = %T, want *messages.TextDeltaValue", msg.Value)
	}
	if value.Content != wantText {
		t.Fatalf("seed text = %q, want %q", value.Content, wantText)
	}
	if _, ok := inf.WaitForSentMessage(messages.StreamTypeTextDelta, 100*time.Millisecond); ok {
		t.Fatal("session sent more than one text seed")
	}
}

func TestSessionCommandPromptKeepsPCMOutOfTextOutput(t *testing.T) {
	inf := functional.NewMockSessionInferencer()
	t.Cleanup(inf.Close)
	output := &recordingSessionOutput{}
	workDir := t.TempDir()
	canonicalWorkDir, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		t.Fatalf("canonicalize test workdir: %v", err)
	}
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	globalFlags.WorkDirPath = workDir
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), globalFlags, newInjectedSessionService(sessionservicewire.SessionDependencies{Clock: sessionclock.Real{}, SessionInferencer: inf}), nil).Generate()
	cmd.SetOut(output)
	cmd.SetArgs([]string{"--replay", "synthetic.json", "--wait-for-close", "--prompt", "distinctive text seed"})
	result := make(chan error, 1)
	go func() { result <- cmd.ExecuteContext(context.Background()) }()

	sent, ok := inf.WaitForSentMessage(messages.StreamTypeTextDelta, 3*time.Second)
	if !ok {
		t.Fatal("timed out waiting for text seed")
	}
	textValue, ok := sent.Value.(*messages.TextDeltaValue)
	if !ok || textValue.Content != "distinctive text seed" {
		t.Fatalf("sent text = %#v, want distinctive prompt", sent.Value)
	}
	if _, ok := inf.WaitForSentMessage(messages.StreamTypeTextDelta, 100*time.Millisecond); ok {
		t.Fatal("session sent more than one text seed")
	}

	pcm := []byte{0x52, 0x49, 0x46, 0x46, 0x10, 0x20, 0x30, 0x40}
	const transcript = "readable transcript"
	inf.AddServerEventSequence([]messages.StreamMessage{
		{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, Value: messages.NewAudioStartValue()},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue(pcm)},
		{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant, Value: messages.NewTranscriptDeltaValue(transcript)},
		{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, Value: messages.NewAudioEndValue()},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
		{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("mock-session", "provider_closed")},
	})

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("session command: %v", err)
		}
	case <-time.After(3 * time.Second):
		inf.Close()
		t.Fatal("timed out waiting for session command")
	}

	writes := output.Writes()
	got := string(bytes.Join(writes, nil))
	want := "Filesystem scope: workdir=" + canonicalWorkDir + "; additional_allowed_roots=none\n" +
		tools.FilesystemScopeStartupNotice + "\n" +
		"Tools: none\n" +
		"Assistant: " + transcript + "\n[session closed: provider_closed]\n" +
		"[session terminal: classification=transport terminal_reason=provider_close terminal_provenance=session output_state=not_applicable]\n"
	if got != want {
		t.Fatalf("text output = %q, want %q", got, want)
	}
	if bytes.Contains([]byte(got), pcm) {
		t.Fatalf("text output contains raw PCM: %q", got)
	}
	inf.Close()
}

type recordingSessionOutput struct {
	mu     sync.Mutex
	writes [][]byte
}

func (w *recordingSessionOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes = append(w.writes, append([]byte(nil), p...))
	return len(p), nil
}

func (w *recordingSessionOutput) Writes() [][]byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	writes := make([][]byte, len(w.writes))
	for i, write := range w.writes {
		writes[i] = append([]byte(nil), write...)
	}
	return writes
}

// scriptedRealtimeDialer is a hermetic OpenAI Realtime server: it answers
// session.update with session.created and response.create with a completed
// response, so the full session loop runs without any network access.
type scriptedRealtimeDialer struct {
	mu      sync.Mutex
	pending [][]byte
	closed  bool
}

type scriptedRealtimeConn struct {
	dialer *scriptedRealtimeDialer
}

func (d *scriptedRealtimeDialer) Dial(_ string, _ map[string]string) (transport.Conn, error) {
	return &scriptedRealtimeConn{dialer: d}, nil
}

func (d *scriptedRealtimeDialer) enqueue(payload []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pending = append(d.pending, payload)
}

func (c *scriptedRealtimeConn) WriteMessage(_ int, payload []byte) error {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}
	switch envelope.Type {
	case wireSessionUpdate:
		c.dialer.enqueue([]byte(`{"type":"session.created","session":{"id":"sess_wire","model":"gpt-realtime-2.1-mini"}}`))
	case "response.create":
		c.dialer.enqueue([]byte(`{"type":"response.created"}`))
		c.dialer.enqueue([]byte(`{"type":"response.output_audio_transcript.delta","delta":"ok"}`))
		c.dialer.enqueue([]byte(`{"type":"response.done"}`))
	case "response.cancel":
		c.dialer.enqueue([]byte(`{"type":"response.done"}`))
	}
	return nil
}

func (c *scriptedRealtimeConn) ReadMessage() (int, []byte, error) {
	for {
		c.dialer.mu.Lock()
		if c.dialer.closed && len(c.dialer.pending) == 0 {
			c.dialer.mu.Unlock()
			return 0, nil, io.EOF
		}
		if len(c.dialer.pending) > 0 {
			payload := c.dialer.pending[0]
			c.dialer.pending = c.dialer.pending[1:]
			c.dialer.mu.Unlock()
			return 1, payload, nil
		}
		c.dialer.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
	}
}

func (c *scriptedRealtimeConn) Close() error {
	c.dialer.mu.Lock()
	defer c.dialer.mu.Unlock()
	c.dialer.closed = true
	return nil
}

func TestWireCapturePromptReachesConversationItemCreate(t *testing.T) {
	const prompt = "Say hello in one short sentence."
	recorder := gwtesting.NewRecordingWebSocketDialer(&scriptedRealtimeDialer{}, "openai", "gpt-realtime-2.1-mini")

	opts := agentruntime.SessionRunOptions{ModelCatalog: testModelCatalog(),
		AudioService:    audioiowire.NewService(),
		Provider:        "openai",
		Model:           "gpt-realtime-2.1-mini",
		APIKey:          "test-key",
		ConfigDir:       t.TempDir(),
		WebSocketDialer: recorder,
	}
	out := &bytes.Buffer{}
	err := agentruntime.RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration(
		context.Background(), out, opts, "", 0,
		agentruntime.SessionTextSeed{Value: prompt, Present: true}, "",
	)
	if err != nil {
		t.Fatalf("session run: %v", err)
	}

	capture := recorder.Capture()
	var itemCreates []string
	for _, record := range capture.Records {
		payload := string(record.Payload)
		if strings.Contains(payload, "agent-cli-session-text-seed") {
			t.Fatalf("sentinel leaked to the wire in frame %q: %s", record.Type, payload)
		}
		if record.Direction == gwtesting.DirectionClientToServer && record.Type == wireConversationItemCreate {
			itemCreates = append(itemCreates, payload)
		}
	}
	if len(itemCreates) == 0 {
		t.Fatalf("no conversation.item.create captured; frames: %+v", capture.Records)
	}
	found := false
	for _, payload := range itemCreates {
		var decoded struct {
			Item struct {
				Content []struct {
					Text string `json:"text"`
					Type string `json:"type"`
				} `json:"content"`
			} `json:"item"`
		}
		if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
			t.Fatalf("decode conversation.item.create %s: %v", payload, err)
		}
		for _, content := range decoded.Item.Content {
			if content.Text == prompt {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("prompt %q not found in conversation.item.create payloads: %v", prompt, itemCreates)
	}
}

func TestWireCapturePromptReachesWireWithDurationBound(t *testing.T) {
	const prompt = "Say hello in one short sentence."
	recorder := gwtesting.NewRecordingWebSocketDialer(&scriptedRealtimeDialer{}, "openai", "gpt-realtime-2.1-mini")

	opts := agentruntime.SessionRunOptions{ModelCatalog: testModelCatalog(),
		AudioService:    audioiowire.NewService(),
		Provider:        "openai",
		Model:           "gpt-realtime-2.1-mini",
		APIKey:          "test-key",
		ConfigDir:       t.TempDir(),
		WebSocketDialer: recorder,
	}
	out := &bytes.Buffer{}
	err := agentruntime.RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration(
		context.Background(), out, opts, "", 2*time.Second,
		agentruntime.SessionTextSeed{Value: prompt, Present: true}, "",
	)
	if err != nil {
		t.Fatalf("session run: %v", err)
	}

	capture := recorder.Capture()
	var itemCreates []string
	promptOnWire := false
	for _, record := range capture.Records {
		payload := string(record.Payload)
		if strings.Contains(payload, "agent-cli-session-text-seed") {
			t.Fatalf("sentinel leaked to the wire in frame %q: %s", record.Type, payload)
		}
		if record.Direction == gwtesting.DirectionClientToServer && record.Type == wireConversationItemCreate {
			itemCreates = append(itemCreates, payload)
			var decoded struct {
				Item struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"item"`
			}
			if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
				t.Fatalf("decode conversation.item.create %s: %v", payload, err)
			}
			for _, content := range decoded.Item.Content {
				if content.Text == prompt {
					promptOnWire = true
				}
			}
		}
	}
	if !promptOnWire {
		t.Fatalf("prompt %q not found in conversation.item.create payloads: %v", prompt, itemCreates)
	}
}
