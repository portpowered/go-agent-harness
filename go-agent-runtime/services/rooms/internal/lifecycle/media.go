package lifecycle

import (
	"context"
	"errors"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
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
