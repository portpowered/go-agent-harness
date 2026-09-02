package services

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
)

// TestRTCDeviceSinkSerializesConcurrentProducersAcrossCapacityAndWrite guards
// the provider-pump/hold-tone race: capacity admission and enqueue must behave
// as one producer transaction even though cancellation remains independent.
func TestRTCDeviceSinkSerializesConcurrentProducersAcrossCapacityAndWrite(t *testing.T) {
	handle := &adversarialCapacityHandle{release: make(chan struct{})}
	registry := newAdversarialCapacityRegistry(t, handle)
	sink, err := NewRTCDeviceSink(registry, "adversarial:output")
	if err != nil {
		t.Fatalf("open adversarial sink: %v", err)
	}
	defer func() { _ = sink.Close() }()

	const producers = 24
	start := make(chan struct{})
	results := make(chan error, producers)
	for index := 0; index < producers; index++ {
		go func(seed int) {
			<-start
			frame := make([]int16, audio.FrameSize)
			frame[0] = int16(seed + 1)
			results <- sink.observedWritePlayback(context.Background(), frame, 0, false, true)
		}(index)
	}
	close(start)

	deadline := time.Now().Add(time.Second)
	for handle.current.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if handle.current.Load() != 1 {
		t.Fatalf("capacity wait concurrency before release = %d, want 1", handle.current.Load())
	}
	// Give every goroutine a chance to contend. Without pacingMu all 24 enter
	// the backend capacity check before any enqueue occurs.
	time.Sleep(20 * time.Millisecond)
	if got := handle.maximum.Load(); got != 1 {
		t.Fatalf("concurrent capacity admissions = %d, want exactly 1", got)
	}
	close(handle.release)

	for index := 0; index < producers; index++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("producer %d: %v", index, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("producer %d did not finish", index)
		}
	}
	if got := handle.writes.Load(); got != producers {
		t.Fatalf("device writes = %d, want %d", got, producers)
	}
}

type adversarialCapacityRegistry struct {
	device audio.Device
	handle *adversarialCapacityHandle
}

func newAdversarialCapacityRegistry(t *testing.T, handle *adversarialCapacityHandle) *adversarialCapacityRegistry {
	t.Helper()
	device, err := audio.NewDevice("adversarial", "output", "Adversarial Output", audio.DirectionOutput)
	if err != nil {
		t.Fatal(err)
	}
	return &adversarialCapacityRegistry{device: device, handle: handle}
}

func (r *adversarialCapacityRegistry) List() ([]audio.Device, error) {
	return []audio.Device{r.device}, nil
}

func (r *adversarialCapacityRegistry) Default(direction audio.Direction) (audio.Device, error) {
	return r.device, nil
}

func (r *adversarialCapacityRegistry) Open(audio.DeviceID) (audio.OpenedDevice, error) {
	return r.handle, nil
}

type adversarialCapacityHandle struct {
	release chan struct{}
	current atomic.Int32
	maximum atomic.Int32
	writes  atomic.Int32
	closed  atomic.Bool
	once    sync.Once
}

func (h *adversarialCapacityHandle) Direction() audio.Direction { return audio.DirectionOutput }
func (h *adversarialCapacityHandle) DeviceFormat() audio.DeviceFormat {
	return audio.DefaultDeviceFormat()
}

func (h *adversarialCapacityHandle) WaitForPlaybackCapacity(ctx context.Context, _ int) error {
	current := h.current.Add(1)
	defer h.current.Add(-1)
	for {
		maximum := h.maximum.Load()
		if current <= maximum || h.maximum.CompareAndSwap(maximum, current) {
			break
		}
	}
	select {
	case <-h.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *adversarialCapacityHandle) WriteFrame(context.Context, []int16) error {
	h.writes.Add(1)
	return nil
}

func (h *adversarialCapacityHandle) Close() error {
	h.once.Do(func() { h.closed.Store(true) })
	return nil
}
