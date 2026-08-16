package services_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	functional "github.com/portpowered/go-agent-harness/go-agent-loop/test/functional"
)

func TestSessionCommandHelpExposesPromptSeed(t *testing.T) {
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil).Generate()
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

func TestSessionTextSeedPresenceMatrix(t *testing.T) {
	longPrompt := strings.Repeat("long-", 4096) + "終"
	cases := []struct {
		name          string
		seed          services.SessionTextSeed
		positional    string
		wantText      string
		wantTextEvent bool
	}{
		{
			name:          "absent preserves positional text",
			seed:          services.SessionTextSeed{},
			positional:    "legacy positional text",
			wantText:      "legacy positional text",
			wantTextEvent: true,
		},
		{
			name:          "absent with no positional text sends no seed",
			seed:          services.SessionTextSeed{},
			wantTextEvent: false,
		},
		{
			name:          "present empty",
			seed:          services.SessionTextSeed{Present: true},
			wantText:      "",
			wantTextEvent: true,
		},
		{
			name:          "present whitespace",
			seed:          services.SessionTextSeed{Value: " \t  ", Present: true},
			wantText:      " \t  ",
			wantTextEvent: true,
		},
		{
			name:          "present long",
			seed:          services.SessionTextSeed{Value: longPrompt, Present: true},
			wantText:      longPrompt,
			wantTextEvent: true,
		},
		{
			name:          "present newline and non-ascii",
			seed:          services.SessionTextSeed{Value: "第一行\nδεύτερη\n🙂", Present: true},
			wantText:      "第一行\nδεύτερη\n🙂",
			wantTextEvent: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inf := functional.NewMockSessionInferencer()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				result <- services.RunSessionWithTextSeed(ctx, &bytes.Buffer{}, services.SessionRunOptions{
					ReplayPath:        "synthetic.json",
					Prompt:            tc.positional,
					SessionInferencer: inf,
				}, tc.seed)
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

				// Complete the same session through the existing audio/event path so
				// the prompt assertion cannot pass against a disconnected no-op.
				inf.AddServerEventSequence([]messages.StreamMessage{
					{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, Value: messages.NewAudioStartValue()},
					{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue([]byte{0x52, 0x49, 0x46, 0x46})},
					{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, Value: messages.NewAudioEndValue()},
					{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
				})
			} else {
				if _, ok := inf.WaitForSentMessage(messages.StreamTypeTextDelta, 100*time.Millisecond); ok {
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
		})
	}
}

func TestSessionTextAndAudioVerticalPreservesMockAudio(t *testing.T) {
	inf := functional.NewMockSessionInferencer()
	scenario := functional.NewSessionScenario(t, inf, functional.NewMockToolExecutor())
	scenario.Start()
	t.Cleanup(func() { inf.Close() })

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	const prompt = "distinctive text seed for audio"
	scenario.SendText(prompt)
	sent, ok := inf.WaitForSentMessage(messages.StreamTypeTextDelta, 3*time.Second)
	if !ok {
		t.Fatal("timed out waiting for text seed")
	}
	textValue, ok := sent.Value.(*messages.TextDeltaValue)
	if !ok || textValue.Content != prompt {
		t.Fatalf("sent text = %#v, want %q", sent.Value, prompt)
	}

	wantAudio := []byte{0x52, 0x49, 0x46, 0x46, 0x10, 0x20, 0x30, 0x40}
	inf.AddServerEventSequence([]messages.StreamMessage{
		{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, Value: messages.NewAudioStartValue()},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue(wantAudio)},
		{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, Value: messages.NewAudioEndValue()},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	})
	if !scenario.WaitForEvent(messages.StreamTypeMessageEnd, 3*time.Second) {
		t.Fatal("timed out waiting for MESSAGE.END")
	}

	var audioDeltas [][]byte
	for _, delta := range scenario.Deltas() {
		if delta.Type != messages.StreamTypeAudioDelta {
			continue
		}
		value, ok := delta.Value.(*messages.AudioDeltaValue)
		if !ok {
			t.Fatalf("audio delta value type = %T, want *messages.AudioDeltaValue", delta.Value)
		}
		audioDeltas = append(audioDeltas, value.Content)
	}
	if len(audioDeltas) != 1 {
		t.Fatalf("audio delta count = %d, want 1", len(audioDeltas))
	}
	if !bytes.Equal(audioDeltas[0], wantAudio) {
		t.Fatalf("audio bytes = %v, want %v", audioDeltas[0], wantAudio)
	}

	if err := scenario.Stop(3 * time.Second); err != nil {
		t.Fatalf("stop session: %v", err)
	}
}

func TestSessionCommandPromptOverridesPositionalMessage(t *testing.T) {
	inf := functional.NewMockSessionInferencer()
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), inf).Generate()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--replay", "synthetic.json", "--prompt", "flag text", "positional text"})

	result := make(chan error, 1)
	go func() { result <- cmd.Execute() }()

	msg, ok := inf.WaitForSentMessage(messages.StreamTypeTextDelta, 2*time.Second)
	if !ok {
		t.Fatal("timed out waiting for the explicit prompt")
	}
	value, ok := msg.Value.(*messages.TextDeltaValue)
	if !ok || value.Content != "flag text" {
		t.Fatalf("explicit prompt = %#v, want %q", msg.Value, "flag text")
	}

	inf.AddServerEventSequence([]messages.StreamMessage{
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
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
}
