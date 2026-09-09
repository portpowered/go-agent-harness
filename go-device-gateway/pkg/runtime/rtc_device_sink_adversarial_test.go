package runtime

import devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
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
			results <- sink.WritePlayback(context.Background(), frame)
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
	device devicegw.Device
	handle *adversarialCapacityHandle
}

func newAdversarialCapacityRegistry(t *testing.T, handle *adversarialCapacityHandle) *adversarialCapacityRegistry {
	t.Helper()
	device, err := devicegw.NewDevice("adversarial", "output", "Adversarial Output", devicegw.DirectionOutput)
	if err != nil {
		t.Fatal(err)
	}
	return &adversarialCapacityRegistry{device: device, handle: handle}
}

func (r *adversarialCapacityRegistry) List() ([]devicegw.Device, error) {
	return []devicegw.Device{r.device}, nil
}

func (r *adversarialCapacityRegistry) Default(direction devicegw.Direction) (devicegw.Device, error) {
	return r.device, nil
}

func (r *adversarialCapacityRegistry) Open(devicegw.DeviceID) (devicegw.OpenedDevice, error) {
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

func (h *adversarialCapacityHandle) Direction() devicegw.Direction { return devicegw.DirectionOutput }
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

type c21DelayedPlaybackRegistry struct {
	device devicegw.Device
	handle *c21DelayedPlaybackHandle
}

func newC21DelayedPlaybackRegistry(t *testing.T, handle *c21DelayedPlaybackHandle) *c21DelayedPlaybackRegistry {
	t.Helper()
	device, err := devicegw.NewDevice("c21-delayed", "output", "C21 Delayed Output", devicegw.DirectionOutput)
	if err != nil {
		t.Fatal(err)
	}
	return &c21DelayedPlaybackRegistry{device: device, handle: handle}
}

func (r *c21DelayedPlaybackRegistry) List() ([]devicegw.Device, error) {
	return []devicegw.Device{r.device}, nil
}

func (r *c21DelayedPlaybackRegistry) Default(devicegw.Direction) (devicegw.Device, error) {
	return r.device, nil
}

func (r *c21DelayedPlaybackRegistry) Open(devicegw.DeviceID) (devicegw.OpenedDevice, error) {
	return r.handle, nil
}

type c21DelayedPlaybackHandle struct {
	deviceID        devicegw.DeviceID
	format          audio.DeviceFormat
	queue           *audio.PlaybackQueue
	callbackStarted chan struct{}
	release         chan struct{}
	callbackOnce    sync.Once
	releaseOnce     sync.Once
	closeOnce       sync.Once
}

func newC21DelayedPlaybackHandle(t *testing.T, rate int) *c21DelayedPlaybackHandle {
	t.Helper()
	format := audio.PCM16DeviceFormat(rate)
	queue, err := audio.NewPlaybackQueue(format)
	if err != nil {
		t.Fatal(err)
	}
	return &c21DelayedPlaybackHandle{
		deviceID:        "c21-delayed:output",
		format:          format,
		queue:           queue,
		callbackStarted: make(chan struct{}),
		release:         make(chan struct{}),
	}
}

func (h *c21DelayedPlaybackHandle) DeviceDirection() devicegw.Direction {
	return devicegw.DirectionOutput
}

func (h *c21DelayedPlaybackHandle) DeviceFormat() audio.DeviceFormat { return h.format }

func (h *c21DelayedPlaybackHandle) WaitForPlaybackCapacity(context.Context, int) error {
	return nil
}

func (h *c21DelayedPlaybackHandle) WriteFrame(ctx context.Context, samples []int16) error {
	if err := audio.ContextError(ctx); err != nil {
		return err
	}
	h.queue.Enqueue(samples)
	return nil
}

func (h *c21DelayedPlaybackHandle) WriteSamples(ctx context.Context, samples []int16) error {
	return h.WriteFrame(ctx, samples)
}

func (h *c21DelayedPlaybackHandle) SetPlaybackRenderObserver(observer audio.PlaybackRenderObserver) {
	h.queue.SetRenderObserver(func(rate int, samples []int16) {
		h.callbackOnce.Do(func() { close(h.callbackStarted) })
		<-h.release
		observer(rate, samples)
	})
}

func (h *c21DelayedPlaybackHandle) releaseObserver() {
	h.releaseOnce.Do(func() { close(h.release) })
}

func (h *c21DelayedPlaybackHandle) PlaybackStats() audio.PlaybackQueueStats {
	return h.queue.Snapshot()
}

func (h *c21DelayedPlaybackHandle) DiscardPlayback() int { return h.queue.Discard() }

func (h *c21DelayedPlaybackHandle) render(samples int) int {
	return h.queue.RenderInto(make([]int16, samples))
}

func (h *c21DelayedPlaybackHandle) Close() error {
	h.closeOnce.Do(h.releaseObserver)
	return nil
}

var _ devicegw.DeviceRegistry = (*c21DelayedPlaybackRegistry)(nil)
var _ devicegw.OpenedDevice = (*c21DelayedPlaybackHandle)(nil)
var _ audio.PlaybackStatsProvider = (*c21DelayedPlaybackHandle)(nil)
var _ audio.PlaybackDiscarder = (*c21DelayedPlaybackHandle)(nil)

func c21StartDelayedRender(handle *c21DelayedPlaybackHandle) chan struct{} {
	done := make(chan struct{})
	go func() {
		handle.render(4)
		close(done)
	}()
	return done
}

func c21WaitDelayedCallback(t *testing.T, handle *c21DelayedPlaybackHandle) {
	t.Helper()
	select {
	case <-handle.callbackStarted:
	case <-time.After(time.Second):
		t.Fatal("delayed callback did not reach observer gate")
	}
}

func c21WaitDelayedRender(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("delayed callback did not finish")
	}
}

func assertC21DelayedConsumed(t *testing.T, got RTCDevicePlaybackObservation, response audio.PlaybackResponse, samples []int16) {
	t.Helper()
	if got.Kind != RTCDevicePlaybackConsumed || got.PlaybackResponse != response || got.StartSample != 0 || got.EndSample != 4 || got.SampleCount != 4 || !got.Consumed || !got.Precise || !reflect.DeepEqual(got.Samples, samples[:4]) {
		t.Fatalf("delayed callback consumed = %+v, want one callback-owned prefix", got)
	}
}

func assertC21DelayedDiscard(t *testing.T, got RTCDevicePlaybackObservation, response audio.PlaybackResponse) {
	t.Helper()
	if got.Kind != RTCDevicePlaybackDiscard || got.PlaybackResponse != response || got.StartSample != 4 || got.EndSample != 8 || got.SampleCount != 4 || got.Consumed || !got.Precise {
		t.Fatalf("delayed callback discard = %+v, want only native tail [4,8)", got)
	}
}

func assertC21NoDelayedDuplicate(t *testing.T, sub *RTCDevicePlaybackObservationSubscription) {
	t.Helper()
	select {
	case event := <-sub.events:
		t.Fatalf("delayed callback produced duplicate event: %+v", event)
	default:
	}
}

func c21WaitForDeviceSamples(t *testing.T, sub *RTCDevicePlaybackObservationSubscription, want uint64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		if sub.Stats().DeviceSamples >= want {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("device sample clock = %d, want at least %d after callback drain", sub.Stats().DeviceSamples, want)
		}
		time.Sleep(time.Millisecond)
	}
}
