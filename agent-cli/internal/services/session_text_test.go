package services_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	functional "github.com/portpowered/go-agent-harness/go-agent-loop/test/functional"
)

func TestSessionCommandHelpExposesPromptSeed(t *testing.T) {
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
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
			inf := functional.NewMockSessionInferencer()
			t.Cleanup(inf.Close)
			cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, inf).Generate()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs(tc.args)
			result := make(chan error, 1)
			go func() {
				result <- cmd.ExecuteContext(context.Background())
			}()

			if tc.wantTextEvent {
				msg, ok := inf.WaitForSentMessage(messages.StreamTypeTextDelta, 2*time.Second)
				if !ok {
					t.Fatal("timed out waiting for the session text seed")
				}
				value, ok := msg.Value.(*messages.TextDeltaValue)
				if !ok {
					t.Fatalf("seed value type = %T, want *messages.TextDeltaValue", msg.Value)
				}
				if value.Content != tc.wantText {
					t.Fatalf("seed text = %q, want %q", value.Content, tc.wantText)
				}
				if _, ok := inf.WaitForSentMessage(messages.StreamTypeTextDelta, 100*time.Millisecond); ok {
					t.Fatal("session sent more than one text seed")
				}
				inf.AddServerEvent(messages.StreamMessage{
					Type:  messages.StreamTypeTextDelta,
					Role:  messages.RoleAssistant,
					Value: messages.NewTextDeltaValue("prompt test response"),
				})
				inf.AddServerEvent(messages.StreamMessage{
					Type:  messages.StreamTypeMessageEnd,
					Role:  messages.RoleAssistant,
					Value: messages.NewMessageEndValue(messages.TokenUsage{}),
				})
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
		})
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
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), globalFlags, nil, inf).Generate()
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
