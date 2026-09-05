package audio_test

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
	audiosubsystem "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/subsystems/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func TestAudioSubsystemQueuesInterruptDespiteFullPlayback(t *testing.T) {
	media, _, playback, err := audio.NewFrameBuffer(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := media.TrySubmit(audio.PCMFrame{Samples: []int16{5}}); err != nil {
		t.Fatal(err)
	}
	commands, consumer, err := audio.NewCommandBuffer(1)
	if err != nil {
		t.Fatal(err)
	}
	s := audiosubsystem.New(audiosubsystem.Ports{Playback: playback, Commands: commands})
	st := &state.LoopState{}
	// A loop pass is unrelated to the playback generation. Use a large pass
	// value so an accidental pass-to-epoch conversion is observable.
	st.History.CurrentPassID = 99
	st.Inputs.UserControlPlaneMessage = []messages.Message{{ContentParts: []messages.ContentPart{messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypeInterrupt}}}}
	if err := s.Execute(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	command, err := consumer.Receive(context.Background())
	if err != nil || command.Kind != audio.CommandInterrupt || command.Epoch != 1 {
		t.Fatalf("command=%+v err=%v, want playback epoch 1 independent of loop pass", command, err)
	}
	if st.Audio == nil || st.Audio.Playback.QueuedSamples != 1 || st.Audio.LastCommandID != command.ID {
		t.Fatalf("observation=%+v", st.Audio)
	}
	// Device cancellation runs outside this tick. Its worker consumes the command.
	if got := playback.Snapshot().QueuedSamples; got != 1 {
		t.Fatalf("tick performed playback work: %d", got)
	}
	if err := s.Execute(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	commands.Close()
	if _, err := consumer.Receive(context.Background()); err == nil {
		t.Fatal("repeated tick queued duplicate interrupt")
	}
}

func TestAudioSubsystemLeavesEpochUnspecifiedWithoutPlaybackPort(t *testing.T) {
	commands, consumer, err := audio.NewCommandBuffer(1)
	if err != nil {
		t.Fatal(err)
	}
	s := audiosubsystem.New(audiosubsystem.Ports{Commands: commands})
	state := &state.LoopState{}
	state.History.CurrentPassID = 123
	state.Inputs.UserControlPlaneMessage = []messages.Message{{ContentParts: []messages.ContentPart{
		messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypeInterrupt},
	}}}
	if err := s.Execute(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	command, err := consumer.Receive(context.Background())
	if err != nil || command.Epoch != 0 {
		t.Fatalf("command=%+v err=%v, want unspecified playback epoch", command, err)
	}
}

func TestAudioSubsystemControlOverflowIsExplicit(t *testing.T) {
	p, _, err := audio.NewCommandBuffer(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.TrySubmit(audio.Command{ID: 99, Kind: audio.CommandDrain}); err != nil {
		t.Fatal(err)
	}
	s := audiosubsystem.New(audiosubsystem.Ports{Commands: p})
	st := &state.LoopState{}
	st.History.CurrentPassID = 1
	st.Inputs.UserControlPlaneMessage = []messages.Message{{ContentParts: []messages.ContentPart{messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypeInterrupt}}}}
	if err := s.Execute(context.Background(), st); !errors.Is(err, audio.ErrControlFull) {
		t.Fatalf("control overflow=%v", err)
	}
}
