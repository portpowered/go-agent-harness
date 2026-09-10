package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/mixer"
)

type report struct {
	DirectPCM             []int16  `json:"direct_pcm"`
	CanonicalPCM          []int16  `json:"canonical_pcm"`
	CanonicalSources      []string `json:"canonical_sources"`
	TailPCM               []int16  `json:"tail_pcm"`
	TailEnd               bool     `json:"tail_end"`
	BoundaryPCM           []int16  `json:"boundary_pcm"`
	BoundaryEnd           bool     `json:"boundary_end"`
	InputIsolation        bool     `json:"input_isolation"`
	WallTimeDidNotAdvance bool     `json:"wall_time_did_not_advance"`
	SourceLimitError      string   `json:"source_limit_error"`
	CleanShutdown         bool     `json:"clean_shutdown"`
}

type readyClock struct {
	virtual *clock.Deterministic
	ready   chan struct{}
	once    sync.Once
}

func (c *readyClock) Now() time.Time {
	return c.virtual.Now()
}

func (c *readyClock) NewTimer(duration time.Duration) clock.Timer {
	c.once.Do(func() { close(c.ready) })
	return c.virtual.NewTimer(duration)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "pcm consumer: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	directSources := [][]int16{
		{32767, 32767, 0},
		{32767, -32768, 1},
		{-32768, 4, -32768},
	}
	wantDirect := []int16{32766, 3, -32767}
	direct, err := mixer.MixPCM16Samples(directSources, len(wantDirect))
	if err != nil {
		return fmt.Errorf("direct shared mix: %w", err)
	}
	if !reflect.DeepEqual(direct, wantDirect) {
		return fmt.Errorf("direct samples = %v, want %v", direct, wantDirect)
	}

	isolationSource := []int16{11, 12}
	isolationOutput, err := mixer.MixPCM16Samples([][]int16{isolationSource}, len(isolationSource))
	if err != nil {
		return fmt.Errorf("input isolation mix: %w", err)
	}
	isolationSource[0] = 99
	if !reflect.DeepEqual(isolationOutput, []int16{11, 12}) {
		return fmt.Errorf("shared mix retained or mutated input: %v", isolationOutput)
	}

	tooManySources := make([][]int16, mixer.MaxPCM16MixSources+1)
	_, limitErr := mixer.MixPCM16Samples(tooManySources, 1)
	if !errors.Is(limitErr, mixer.ErrPCM16MixSourceLimit) {
		return fmt.Errorf("source bound error = %v, want ErrPCM16MixSourceLimit", limitErr)
	}

	format := mixer.Format{SampleRate: 1000, Channels: 1, FrameDuration: 4 * time.Millisecond}
	scheduler := &readyClock{
		virtual: clock.NewDeterministic(time.Unix(0, 0), time.Millisecond),
		ready:   make(chan struct{}),
	}
	mix, err := mixer.New(context.Background(), scheduler, mixer.Config{
		Format:            format,
		StreamID:          "consumer-mix",
		InputQueueFrames:  2,
		OutputQueueFrames: 2,
	})
	if err != nil {
		return fmt.Errorf("canonical mixer: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = mix.Close()
		}
	}()
	select {
	case <-scheduler.ready:
	case <-time.After(time.Second):
		return errors.New("canonical mixer did not install its injected-clock timer")
	}

	ids := []string{"gamma", "alpha", "beta"}
	inputs := make(map[string]*mixer.Input, len(ids))
	for _, id := range ids {
		input, addErr := mix.AddInput(id)
		if addErr != nil {
			return fmt.Errorf("add %s: %w", id, addErr)
		}
		inputs[id] = input
	}
	frames := map[string][]int16{
		"alpha": {32767, 32767, 0, 0},
		"beta":  {32767, -32768, 1, 0},
		"gamma": {-32768, 4, -32768, 0},
	}
	for _, id := range ids {
		if writeErr := inputs[id].WriteFrame(context.Background(), audio.PCMFrame{
			Samples: frames[id],
			Format:  audio.PCM16DeviceFormat(format.SampleRate),
		}); writeErr != nil {
			return fmt.Errorf("write %s: %w", id, writeErr)
		}
	}
	scheduler.virtual.AdvanceBy(format.FrameDuration)
	canonical, err := readMixedFrame(mix)
	if err != nil {
		return fmt.Errorf("read canonical frame: %w", err)
	}
	wantCanonical := []int16{32766, 3, -32767, 0}
	if !reflect.DeepEqual(canonical.Frame.Samples, wantCanonical) {
		return fmt.Errorf("canonical samples = %v, want %v", canonical.Frame.Samples, wantCanonical)
	}
	if !reflect.DeepEqual(canonical.Sources, []string{"alpha", "beta", "gamma"}) {
		return fmt.Errorf("canonical sources = %v, want sorted attribution", canonical.Sources)
	}

	wallTimeDidNotAdvance := false
	readCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	_, readErr := mix.Output().ReadFrame(readCtx)
	cancel()
	if errors.Is(readErr, context.DeadlineExceeded) {
		wallTimeDidNotAdvance = true
	} else {
		return fmt.Errorf("canonical cadence advanced without virtual clock: %v", readErr)
	}

	tailInput, err := mix.AddInput("tail")
	if err != nil {
		return fmt.Errorf("add tail: %w", err)
	}
	if err := tailInput.WriteFrame(context.Background(), audio.PCMFrame{Samples: []int16{7, 8}, EndOfResponse: true}); err != nil {
		return fmt.Errorf("write tail: %w", err)
	}
	scheduler.virtual.AdvanceBy(format.FrameDuration)
	tail, err := readMixedFrame(mix)
	if err != nil {
		return fmt.Errorf("read tail: %w", err)
	}
	if !reflect.DeepEqual(tail.Frame.Samples, []int16{7, 8}) || !tail.Frame.EndOfResponse {
		return fmt.Errorf("tail = %+v, want exact terminal tail", tail.Frame)
	}

	boundaryInput, err := mix.AddInput("boundary")
	if err != nil {
		return fmt.Errorf("add boundary: %w", err)
	}
	if err := boundaryInput.WriteFrame(context.Background(), audio.PCMFrame{EndOfResponse: true}); err != nil {
		return fmt.Errorf("write boundary: %w", err)
	}
	scheduler.virtual.AdvanceBy(format.FrameDuration)
	boundary, err := readMixedFrame(mix)
	if err != nil {
		return fmt.Errorf("read boundary: %w", err)
	}
	if len(boundary.Frame.Samples) != 0 || !boundary.Frame.EndOfResponse {
		return fmt.Errorf("boundary = %+v, want empty terminal marker", boundary.Frame)
	}

	if err := mix.Close(); err != nil {
		return fmt.Errorf("close canonical mixer: %w", err)
	}
	closed = true
	result := report{
		DirectPCM:             direct,
		CanonicalPCM:          canonical.Frame.Samples,
		CanonicalSources:      canonical.Sources,
		TailPCM:               tail.Frame.Samples,
		TailEnd:               tail.Frame.EndOfResponse,
		BoundaryPCM:           boundary.Frame.Samples,
		BoundaryEnd:           boundary.Frame.EndOfResponse,
		InputIsolation:        true,
		WallTimeDidNotAdvance: wallTimeDidNotAdvance,
		SourceLimitError:      limitErr.Error(),
		CleanShutdown:         true,
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func readMixedFrame(mix *mixer.Mixer) (mixer.MixedFrame, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return mix.OutputWithSources().ReadMixedFrame(ctx)
}
