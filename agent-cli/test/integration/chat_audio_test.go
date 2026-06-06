package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/portpowered/agent-cli/internal/agent"
	"github.com/portpowered/agent-cli/internal/audio"
	"github.com/portpowered/agent-cli/internal/cli"
	"github.com/portpowered/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-loop/pkg/messages"
)

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// makePCMSpeech creates n frames of high-energy PCM samples (RMS ≈ 1000)
// that reliably exceed DefaultVADConfig.EnergyThreshold (300).
func makePCMSpeech(frames int) []int16 {
	n := audio.FrameSize * frames
	s := make([]int16, n)
	for i := range s {
		if i%2 == 0 {
			s[i] = 1000
		} else {
			s[i] = -1000
		}
	}
	return s
}

// makePCMSilence creates n frames of silence (zeros).
func makePCMSilence(frames int) []int16 {
	return make([]int16, audio.FrameSize*frames)
}

// msgsContainUserAudio returns true if any user message in msgs has an AudioPart.
func msgsContainUserAudio(msgs []messages.Message) bool {
	for _, m := range msgs {
		if m.Role != messages.RoleUser {
			continue
		}
		for _, p := range m.ContentParts {
			if _, ok := p.(messages.AudioPart); ok {
				return true
			}
		}
	}
	return false
}

// --------------------------------------------------------------------------
// Tests
// --------------------------------------------------------------------------

// TestChatAudio_SingleUtterance feeds one utterance (speech then silence) into
// RunChatWithAudio and verifies the agent is called once with an Audio payload.
func TestChatAudio_SingleUtterance(t *testing.T) {
	speechFrames := 20
	silenceFrames := audio.DefaultVADConfig.MaxSilenceFrames

	samples := append(makePCMSpeech(speechFrames), makePCMSilence(silenceFrames)...)
	src := audio.NewSliceSource(samples)

	fakeResponse := "Audio received!"
	rec := &recordingInferencer{response: fakeResponse}
	executor := agent.NewExecutor(&mockToolExecutor{}, nil, rec, true)
	globalFlags := flags.NewGlobalFlags()
	askFlags := flags.NewAskFlags()

	tw := NewTestWriter()
	ctx := context.Background()

	err := cli.RunChatWithAudio(ctx, tw.Stdout(), tw.Stderr(), executor, globalFlags, askFlags, src)
	if err != nil {
		t.Fatalf("RunChatWithAudio returned unexpected error: %v", err)
	}

	if n := len(rec.recorded); n != 1 {
		t.Fatalf("expected 1 agent call, got %d", n)
	}
	if !rec.hasUserMessageWithAudioPart("") {
		t.Fatal("expected ExecuteInput to produce a user message with AudioPart in the conversation")
	}

	out := tw.StdoutString()
	if !strings.Contains(out, "(speech detected, processing...)") {
		t.Errorf("expected status line in output, got:\n%s", out)
	}
	if !strings.Contains(out, fakeResponse) {
		t.Errorf("expected fake response in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Goodbye!") {
		t.Errorf("expected Goodbye! when source is exhausted, got:\n%s", out)
	}
}

// TestChatAudio_MultipleUtterances verifies that each burst of speech separated
// by silence becomes its own agent invocation.
func TestChatAudio_MultipleUtterances(t *testing.T) {
	speechFrames := 15
	silence := audio.DefaultVADConfig.MaxSilenceFrames

	samples := makePCMSpeech(speechFrames)
	samples = append(samples, makePCMSilence(silence)...)
	samples = append(samples, makePCMSpeech(speechFrames)...)
	samples = append(samples, makePCMSilence(silence)...)

	src := audio.NewSliceSource(samples)
	rec := &recordingInferencer{response: "ok"}
	executor := agent.NewExecutor(&mockToolExecutor{}, nil, rec, true)
	globalFlags := flags.NewGlobalFlags()
	askFlags := flags.NewAskFlags()

	tw := NewTestWriter()
	ctx := context.Background()

	if err := cli.RunChatWithAudio(ctx, tw.Stdout(), tw.Stderr(), executor, globalFlags, askFlags, src); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(rec.recorded) != 2 {
		t.Errorf("expected 2 agent calls (one per utterance), got %d", len(rec.recorded))
	}
	for i, msgs := range rec.recorded {
		if !msgsContainUserAudio(msgs) {
			t.Errorf("utterance %d: expected message with AudioPart", i)
		}
	}
}

// TestChatAudio_SilenceOnlySourceExitsGracefully verifies that a source
// containing only silence results in a clean exit (no agent calls, Goodbye!).
func TestChatAudio_SilenceOnlySourceExitsGracefully(t *testing.T) {
	src := audio.NewSliceSource(makePCMSilence(5))
	rec := &recordingInferencer{response: "should not happen"}
	executor := agent.NewExecutor(&mockToolExecutor{}, nil, rec, true)
	globalFlags := flags.NewGlobalFlags()
	askFlags := flags.NewAskFlags()

	tw := NewTestWriter()
	ctx := context.Background()

	if err := cli.RunChatWithAudio(ctx, tw.Stdout(), tw.Stderr(), executor, globalFlags, askFlags, src); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(rec.recorded) != 0 {
		t.Errorf("expected no agent calls for silence-only source, got %d", len(rec.recorded))
	}
	if !strings.Contains(tw.StdoutString(), "Goodbye!") {
		t.Error("expected Goodbye! on EOF exit")
	}
}

// TestChatAudio_ContextCancellationExitsGracefully verifies that cancelling the
// context causes RunChatWithAudio to return nil and print Goodbye!.
func TestChatAudio_ContextCancellationExitsGracefully(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	src := audio.NewSliceSource(makePCMSilence(1))
	rec := &recordingInferencer{response: "nope"}
	executor := agent.NewExecutor(&mockToolExecutor{}, nil, rec, true)
	globalFlags := flags.NewGlobalFlags()
	askFlags := flags.NewAskFlags()

	tw := NewTestWriter()
	if err := cli.RunChatWithAudio(ctx, tw.Stdout(), tw.Stderr(), executor, globalFlags, askFlags, src); err != nil {
		t.Fatalf("expected nil error on context cancel, got: %v", err)
	}

	if !strings.Contains(tw.StdoutString(), "Goodbye!") {
		t.Error("expected Goodbye! when context is cancelled")
	}
}
