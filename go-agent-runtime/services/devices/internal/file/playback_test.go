package file

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func TestContinuousPlaybackForwardsFirstProviderDeltaBeforeBoundary(t *testing.T) {
	sink := &recordingSampleSink{written: make(chan struct{}, 1)}
	playback, err := newPlayback(devices.FileOutput{
		Sink: sink, SampleRate: sharedaudio.SampleRate, Continuous: true,
	}, sharedaudio.SampleRate)
	if err != nil {
		t.Fatalf("newPlayback: %v", err)
	}

	input := &gatedPlaybackInput{
		first:  sharedaudio.PCMFrame{Samples: []int16{1, 2}},
		second: sharedaudio.PCMFrame{EndOfResponse: true},
		beforeSecond: func(ctx context.Context) error {
			select {
			case <-sink.written:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := playback.Pump(ctx, input); err != nil {
		t.Fatalf("continuous playback pump: %v", err)
	}
	got := sink.snapshot()
	if len(got) != 1 || len(got[0]) != 2 || got[0][0] != 1 || got[0][1] != 2 {
		t.Fatalf("continuous sink samples = %v, want first delta before boundary", got)
	}
}

type gatedPlaybackInput struct {
	first        sharedaudio.PCMFrame
	second       sharedaudio.PCMFrame
	beforeSecond func(context.Context) error
	reads        int
}

func (i *gatedPlaybackInput) ReadFrame(ctx context.Context) (sharedaudio.PCMFrame, error) {
	i.reads++
	switch i.reads {
	case 1:
		return i.first, nil
	case 2:
		if i.beforeSecond != nil {
			if err := i.beforeSecond(ctx); err != nil {
				return sharedaudio.PCMFrame{}, err
			}
		}
		return i.second, nil
	default:
		return sharedaudio.PCMFrame{}, io.EOF
	}
}

func (*gatedPlaybackInput) Close() error { return nil }

type recordingSampleSink struct {
	mu      sync.Mutex
	frames  [][]int16
	written chan struct{}
}

func (s *recordingSampleSink) WriteFrame(context.Context, []int16) error {
	return io.ErrUnexpectedEOF
}

func (s *recordingSampleSink) WriteSamples(_ context.Context, samples []int16) error {
	s.mu.Lock()
	s.frames = append(s.frames, append([]int16(nil), samples...))
	s.mu.Unlock()
	select {
	case s.written <- struct{}{}:
	default:
	}
	return nil
}

func (s *recordingSampleSink) Close() error { return nil }

func (s *recordingSampleSink) snapshot() [][]int16 {
	s.mu.Lock()
	defer s.mu.Unlock()
	frames := make([][]int16, len(s.frames))
	for index, frame := range s.frames {
		frames[index] = append([]int16(nil), frame...)
	}
	return frames
}

var _ sharedaudio.InboundMedia = (*gatedPlaybackInput)(nil)
var _ sharedaudio.SampleSink = (*recordingSampleSink)(nil)
