package lifecycle

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/mixer"
)

// mediaBridge connects a local capture/playback pair to one provider media
// memory with provider or device speed.
type mediaBridge struct {
	cancel context.CancelFunc
	done   chan struct{}

	mu  sync.Mutex
	err error
}

func newMediaBridge(parent context.Context, endpoints audio.MediaEndpoints, local rooms.MediaPorts, onError func(error)) *mediaBridge {
	ctx, cancel := context.WithCancel(parent)
	b := &mediaBridge{cancel: cancel, done: make(chan struct{})}
	var workers sync.WaitGroup
	start := func(run func(context.Context) error) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				b.setError(err)
				if onError != nil {
					onError(err)
				}
				b.cancel()
			}
		}()
	}
	if local.Capture != nil && endpoints.Outbound != nil {
		start(func(ctx context.Context) error { return local.Capture.Pump(ctx, endpoints.Outbound) })
	}
	if local.Playback != nil && endpoints.Inbound != nil {
		start(func(ctx context.Context) error { return local.Playback.Pump(ctx, endpoints.Inbound) })
	}
	go func() {
		workers.Wait()
		close(b.done)
	}()
	return b
}

func (b *mediaBridge) setError(err error) {
	if err == nil {
		return
	}
	b.mu.Lock()
	b.err = errors.Join(b.err, err)
	b.mu.Unlock()
}

func (b *mediaBridge) Wait() error {
	if b == nil {
		return nil
	}
	<-b.done
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}

func (b *mediaBridge) Stop() error {
	if b == nil {
		return nil
	}
	b.cancel()
	return nil
}

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
