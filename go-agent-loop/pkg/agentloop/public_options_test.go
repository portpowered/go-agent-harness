package agentloop

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/participants"
	audiosubsystem "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/subsystems/audio"
)

type optionRecorder struct{}

func (optionRecorder) Record(context.Context, []messages.Message) error { return nil }

type optionTokenCounter struct{}

func (optionTokenCounter) Count([]messages.Message) int { return 0 }

// The public options that no caller in this repository uses must still reach
// the loop configuration they document.
func TestPublicOptionsConfigureTheLoop(t *testing.T) {
	audio := audiosubsystem.New(audiosubsystem.Ports{})
	bargeIn := participants.DefaultBargeInConfig()
	bargeIn.MinSpeech *= 2
	var config AgentLoopConfig
	for _, option := range []Option{
		WithAudioSubsystem(audio),
		WithRecorder(optionRecorder{}),
		WithTokenCounter(optionTokenCounter{}, 4096),
		WithBargeInConfig(bargeIn),
	} {
		option(&config)
	}
	if config.Audio != audio || config.Recorder == nil || config.TokenCounter == nil || config.MaxTokens != 4096 {
		t.Fatalf("config = %#v, want the audio subsystem, recorder and token counter applied", config)
	}
	if config.BargeIn == nil || *config.BargeIn != bargeIn {
		t.Fatalf("BargeIn = %#v, want %#v", config.BargeIn, bargeIn)
	}
}
