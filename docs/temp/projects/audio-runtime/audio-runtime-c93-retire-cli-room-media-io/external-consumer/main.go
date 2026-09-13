package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roommedia"
	roommediawire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roommedia/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

func main() {
	if err := runExternalConsumer(); err != nil {
		panic(err)
	}
}

type fakeMixer struct {
	format roommedia.PCM16Format
	frames []roommedia.MixedFrame
	index  int
	err    error
}

func (m *fakeMixer) Format() roommedia.PCM16Format { return m.format }
func (m *fakeMixer) ReadFrameWithSources(context.Context) (roommedia.MixedFrame, error) {
	if m.index == len(m.frames) {
		if m.err != nil {
			return roommedia.MixedFrame{}, m.err
		}
		return roommedia.MixedFrame{}, io.EOF
	}
	frame := m.frames[m.index]
	m.index++
	return roommedia.MixedFrame{PCM: append([]byte(nil), frame.PCM...), Sources: append([]string(nil), frame.Sources...)}, nil
}

type fakeInput struct {
	frames [][]int16
	index  int
}

func (i *fakeInput) ReadFrame(_ context.Context, frame []int16) error {
	if i.index == len(i.frames) {
		return io.EOF
	}
	copy(frame, i.frames[i.index])
	i.index++
	return nil
}

type fakeOutput struct {
	mu     sync.Mutex
	frames [][]int16
	err    error
}

func (o *fakeOutput) WriteFrame(_ context.Context, frame []int16) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return o.err
	}
	o.frames = append(o.frames, append([]int16(nil), frame...))
	return nil
}

type fakeClock struct{ now time.Time }

func (c fakeClock) Now() time.Time { return c.now }

func runExternalConsumer() error {
	serviceA := roommediawire.NewService(roommediawire.Dependencies{Clock: fakeClock{now: time.Unix(10, 0)}})
	serviceB := roommediawire.NewService(roommediawire.Dependencies{Clock: fakeClock{now: time.Unix(20, 0)}})
	pcm := codec.EncodePCM16([]int16{100, -200, 300, -400})
	converted, err := serviceA.ConvertProviderInput(pcm, roommedia.PCM16Format{SampleRate: 24000, Channels: 1}, 16000)
	if err != nil || len(converted) == 0 {
		return fmt.Errorf("public conversion: %w", err)
	}
	if _, err := serviceB.ConvertProviderInput([]byte{1}, roommedia.PCM16Format{SampleRate: 16000, Channels: 1}, 0); !errors.Is(err, roommedia.ErrPCM16Truncated) {
		return fmt.Errorf("public truncation error=%v", err)
	}

	inputFrame := make([]int16, audio.FrameSize)
	for index := range inputFrame {
		inputFrame[index] = int16(index)
	}
	fanned := make([][]byte, 0, 2)
	err = serviceA.CaptureHuman(context.Background(), roommedia.HumanCaptureRequest{
		ParticipantID: "self",
		Input:         &fakeInput{frames: [][]int16{inputFrame}},
		Mixer:         &fakeMixer{format: roommedia.PCM16Format{SampleRate: 24000, Channels: 1}},
		Targets: func() []roommedia.FanoutTarget {
			return []roommedia.FanoutTarget{
				{ID: "self", Format: roommedia.PCM16Format{SampleRate: 24000, Channels: 1}, Write: func(context.Context, string, []byte) error { return errors.New("self fan-out") }},
				{ID: "peer", Format: roommedia.PCM16Format{SampleRate: 16000, Channels: 1}, Write: func(_ context.Context, _ string, frame []byte) error {
					fanned = append(fanned, append([]byte(nil), frame...))
					return nil
				}},
			}
		},
	})
	if !errors.Is(err, io.EOF) || len(fanned) != 1 || len(fanned[0]) != audio.FrameSize*2 {
		return fmt.Errorf("public fan-out err=%v frames=%d bytes=%d", err, len(fanned), len(fanned[0]))
	}

	bufferOutput := &fakeOutput{}
	buffer, err := serviceA.NewOutputBuffer(roommedia.OutputBufferRequest{Output: bufferOutput, Format: roommedia.PCM16Format{SampleRate: 24000, Channels: 1}, TargetSampleRate: 16000, FrameSamples: audio.FrameSize})
	if err != nil {
		return fmt.Errorf("public buffer: %w", err)
	}
	chunk := codec.EncodePCM16(make([]int16, 480))
	if err := buffer.WriteFrame(context.Background(), chunk); err != nil {
		return fmt.Errorf("public first tail: %w", err)
	}
	if buffer.PendingSamples() == 0 {
		return errors.New("public buffer did not retain a partial tail")
	}
	if err := buffer.WriteFrame(context.Background(), chunk); err != nil {
		return fmt.Errorf("public second tail: %w", err)
	}

	output := &fakeOutput{}
	err = serviceB.PumpHumanOutput(context.Background(), roommedia.HumanOutputRequest{Mixer: &fakeMixer{format: roommedia.PCM16Format{SampleRate: 16000, Channels: 1}, frames: []roommedia.MixedFrame{{PCM: codec.EncodePCM16(make([]int16, audio.FrameSize))}}}, ReadContext: context.Background(), Output: output})
	if !errors.Is(err, io.EOF) {
		return fmt.Errorf("public output close: %w", err)
	}

	cancel, cancelFn := context.WithCancel(context.Background())
	cancelFn()
	err = serviceA.PumpProviderInput(cancel, roommedia.ProviderInputRequest{Mixer: &fakeMixer{format: roommedia.PCM16Format{SampleRate: 16000, Channels: 1}, err: context.Canceled}, Send: func(context.Context, []byte, roommedia.InputPolicy) error { return nil }})
	if !errors.Is(err, context.Canceled) {
		return fmt.Errorf("public cancellation=%v", err)
	}
	closeErr := errors.New("virtual output closed")
	err = serviceB.PumpHumanOutput(context.Background(), roommedia.HumanOutputRequest{Mixer: &fakeMixer{format: roommedia.PCM16Format{SampleRate: 16000, Channels: 1}, frames: []roommedia.MixedFrame{{PCM: codec.EncodePCM16(make([]int16, audio.FrameSize))}}}, Output: &fakeOutput{err: closeErr}})
	if !errors.Is(err, closeErr) {
		return fmt.Errorf("public output error=%v", err)
	}
	return nil
}
