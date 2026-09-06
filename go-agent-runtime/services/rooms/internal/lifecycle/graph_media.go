package lifecycle

import (
	"context"
	"errors"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/mixer"
)

type frameFanout struct {
	graph    *roomGraph
	sourceID string
	recorder audioRecorder
	targets  []*mixer.Input
}

func (f frameFanout) WriteFrame(ctx context.Context, frame audio.PCMFrame) error {
	targets := f.routeTargets()
	if f.recorder != nil {
		f.recorder.RecordSource(f.sourceID, frame)
	}
	if observer, ok := f.recorder.(latencyRecorder); ok {
		observer.ObserveSpeakerAudio(f.sourceID, f.targetIDs(targets), frame)
	}
	for _, target := range targets {
		if target == nil {
			continue
		}
		if err := target.WriteFrame(ctx, frame); err != nil {
			if f.graph != nil && f.graph.inputRetired(target) {
				continue
			}
			return err
		}
	}
	return nil
}

func (f frameFanout) targetIDs(targets []*mixer.Input) []string {
	if f.graph == nil {
		return nil
	}
	return f.graph.routeTargetIDs(targets)
}

func (f frameFanout) routeTargets() []*mixer.Input {
	if f.graph == nil {
		return f.targets
	}
	if f.graph.isRetired(f.sourceID) {
		return nil
	}
	return f.graph.sourceInputs(f.sourceID)
}

func (frameFanout) Close() error { return nil }

type bufferedInbound struct{ consumer audio.FrameConsumer }

func (b bufferedInbound) ReadFrame(ctx context.Context) (audio.PCMFrame, error) {
	return b.consumer.Receive(ctx)
}

func (bufferedInbound) Close() error { return nil }

func (b bufferedInbound) pump(ctx context.Context, playback rooms.MediaPlayback) error {
	return playback.Pump(ctx, b)
}

func isGraphNormalStop(err error) bool {
	return err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF)
}
