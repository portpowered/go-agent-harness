package audio

import "github.com/portpowered/go-agent-harness/go-audio/pkg/codec"

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestPlaybackQueueCapacityUsesResolvedFormatAndLatency(t *testing.T) {
	format := PCM16DeviceFormat(24000)
	got, err := PlaybackQueueCapacity(format, DefaultPlaybackLatencyTarget)
	if err != nil {
		t.Fatalf("PlaybackQueueCapacity: %v", err)
	}
	if got != 6000 {
		t.Fatalf("capacity = %d samples, want 6000 for 250 ms at 24 kHz mono", got)
	}

	q, err := NewPlaybackQueue(format)
	if err != nil {
		t.Fatalf("NewPlaybackQueue: %v", err)
	}
	stats := q.Snapshot()
	if stats.CapacitySamples != got || stats.Format != format || stats.LatencyTarget != DefaultPlaybackLatencyTarget {
		t.Fatalf("queue stats = %+v, want format=%v latency=%s capacity=%d", stats, format, DefaultPlaybackLatencyTarget, got)
	}
}

func TestPlaybackQueueWatermarksLeaveCallbackReserveBelowHardCapacity(t *testing.T) {
	for _, rate := range []int{16000, 24000, 48000} {
		format := PCM16DeviceFormat(rate)
		low, high, err := PlaybackQueueWatermarks(format)
		if err != nil {
			t.Fatalf("PlaybackQueueWatermarks(%d Hz): %v", rate, err)
		}
		capacity, err := PlaybackQueueCapacity(format, DefaultPlaybackLatencyTarget)
		if err != nil {
			t.Fatalf("PlaybackQueueCapacity(%d Hz): %v", rate, err)
		}
		if want := rate * int(DefaultPlaybackLowWatermark/time.Millisecond) / 1000; low != want {
			t.Fatalf("%d Hz low watermark = %d, want %d", rate, low, want)
		}
		if want := rate * int(DefaultPlaybackHighWatermark/time.Millisecond) / 1000; high != want {
			t.Fatalf("%d Hz high watermark = %d, want %d", rate, high, want)
		}
		if !(0 < low && low < high && high < capacity) {
			t.Fatalf("%d Hz watermarks = low:%d high:%d capacity:%d, want strict ordering", rate, low, high, capacity)
		}
	}
}

func TestPlaybackQueueSustainedMatchedRatePreservesOrderWithoutDrops(t *testing.T) {
	format := PCM16DeviceFormat(24000)
	q, err := NewPlaybackQueueWithLatency(format, 25*time.Millisecond)
	if err != nil {
		t.Fatalf("NewPlaybackQueueWithLatency: %v", err)
	}

	const frameSamples = 480
	for frameIndex := 0; frameIndex < 100; frameIndex++ {
		frame := make([]int16, frameSamples)
		for sampleIndex := range frame {
			frame[sampleIndex] = int16(frameIndex*frameSamples + sampleIndex)
		}
		if dropped := q.Enqueue(frame); dropped != 0 {
			t.Fatalf("matched-rate enqueue dropped %d samples at frame %d", dropped, frameIndex)
		}
		got := q.Dequeue(frameSamples)
		if !reflect.DeepEqual(got, frame) {
			t.Fatalf("frame %d was reordered or changed", frameIndex)
		}
	}

	stats := q.Snapshot()
	if stats.QueuedSamples != 0 || stats.DroppedSamples != 0 || stats.OverflowEvents != 0 {
		t.Fatalf("matched-rate stats = %+v, want empty queue and zero overflow", stats)
	}
	if stats.PeakQueuedSamples > stats.CapacitySamples {
		t.Fatalf("peak queue depth %d exceeded capacity %d", stats.PeakQueuedSamples, stats.CapacitySamples)
	}
}

func TestPlaybackQueueOverflowDropsOldestAndCountsExactSamples(t *testing.T) {
	q, err := NewPlaybackQueueWithLatency(PCM16DeviceFormat(24000), time.Millisecond)
	if err != nil {
		t.Fatalf("NewPlaybackQueueWithLatency: %v", err)
	}
	if got := q.Snapshot().CapacitySamples; got != 24 {
		t.Fatalf("capacity = %d, want 24 samples", got)
	}

	first := int16Samples(0, 16)
	second := int16Samples(16, 16)
	if dropped := q.Enqueue(first); dropped != 0 {
		t.Fatalf("first enqueue dropped %d samples", dropped)
	}
	if dropped := q.Enqueue(second); dropped != 8 {
		t.Fatalf("second enqueue dropped %d samples, want 8", dropped)
	}
	if got, want := q.Dequeue(100), int16Samples(8, 24); !reflect.DeepEqual(got, want) {
		t.Fatalf("after first overflow queue = %v, want %v", got, want)
	}

	oversized := int16Samples(100, 40)
	if dropped := q.Enqueue(oversized); dropped != 16 {
		t.Fatalf("oversized enqueue dropped %d samples, want 16", dropped)
	}
	if got, want := q.Dequeue(100), int16Samples(116, 24); !reflect.DeepEqual(got, want) {
		t.Fatalf("after oversized overflow queue = %v, want %v", got, want)
	}
	stats := q.Snapshot()
	if stats.DroppedSamples != 24 || stats.OverflowEvents != 2 {
		t.Fatalf("overflow stats = %+v, want 24 samples across 2 events", stats)
	}
}

func TestPlaybackQueueDiscardCountsOnlyQueuedSamples(t *testing.T) {
	q, err := NewPlaybackQueueWithLatency(PCM16DeviceFormat(24000), time.Millisecond)
	if err != nil {
		t.Fatalf("NewPlaybackQueueWithLatency: %v", err)
	}
	q.Enqueue(int16Samples(0, 10))
	if got := q.Dequeue(4); !reflect.DeepEqual(got, int16Samples(0, 4)) {
		t.Fatalf("dequeue = %v, want first four samples", got)
	}
	if got := q.Discard(); got != 6 {
		t.Fatalf("Discard removed %d samples, want 6", got)
	}
	stats := q.Snapshot()
	if stats.QueuedSamples != 0 || stats.DiscardedSamples != 6 || stats.DiscardEvents != 1 || stats.DroppedSamples != 0 {
		t.Fatalf("discard stats = %+v, want six discarded and no overflow", stats)
	}
	if got := q.Discard(); got != 0 {
		t.Fatalf("empty Discard removed %d samples, want 0", got)
	}
}

func TestPlaybackQueueHandlesInvalidInputsAndPCM16Callbacks(t *testing.T) {
	if _, err := NewPlaybackQueueWithLatency(DeviceFormat{}, time.Second); !errors.Is(err, ErrInvalidPlaybackQueue) {
		t.Fatalf("invalid queue format error = %v, want ErrInvalidPlaybackQueue", err)
	}
	if _, err := PlaybackQueueCapacity(DefaultDeviceFormat(), 0); !errors.Is(err, ErrInvalidPlaybackQueue) {
		t.Fatalf("zero latency error = %v, want ErrInvalidPlaybackQueue", err)
	}
	if _, err := PlaybackQueueCapacity(PCM16DeviceFormat(24000), time.Duration(1<<62)); !errors.Is(err, ErrInvalidPlaybackQueue) {
		t.Fatalf("overflowing latency error = %v, want ErrInvalidPlaybackQueue", err)
	}

	q, err := NewPlaybackQueueWithLatency(PCM16DeviceFormat(24000), time.Millisecond)
	if err != nil {
		t.Fatalf("single-sample queue: %v", err)
	}
	if q.Enqueue(nil) != 0 || q.Dequeue(0) != nil || q.ReadInto(nil) != 0 || q.ReadPCM16([]byte{0}) != 0 || q.Discard() != 0 {
		t.Fatal("empty and zero-length queue operations changed state")
	}
	q.Enqueue([]int16{101, -202})
	bytes := make([]byte, 4)
	if got := q.ReadPCM16(bytes); got != 2 {
		t.Fatalf("ReadPCM16 count = %d, want 2", got)
	}
	decoded := make([]int16, 2)
	codec.DecodePCM16Into(decoded, bytes)
	if !reflect.DeepEqual(decoded, []int16{101, -202}) {
		t.Fatalf("ReadPCM16 decoded = %v, want [101 -202]", decoded)
	}
	q.Enqueue([]int16{7, 8})
	partial := make([]int16, 1)
	if got := q.ReadInto(partial); got != 1 || partial[0] != 7 {
		t.Fatalf("ReadInto = %d/%v, want one sample [7]", got, partial)
	}
	if got := q.Dequeue(10); !reflect.DeepEqual(got, []int16{8}) {
		t.Fatalf("Dequeue after ReadInto = %v, want [8]", got)
	}

	var nilQueue *PlaybackQueue
	if nilQueue.Enqueue([]int16{1}) != 0 || nilQueue.Dequeue(1) != nil || nilQueue.ReadInto(make([]int16, 1)) != 0 || nilQueue.ReadPCM16(make([]byte, 2)) != 0 || nilQueue.Discard() != 0 || nilQueue.Snapshot() != (PlaybackQueueStats{}) {
		t.Fatal("nil queue operation returned a non-zero result")
	}
	if got := EmptyPlaybackQueueStats(DeviceFormat{}); got.Format != DefaultDeviceFormat() || got.CapacitySamples != 4000 {
		t.Fatalf("empty stats fallback = %+v, want legacy default and 4000 samples", got)
	}
	if fallback, err := PlaybackQueueForFormat(DeviceFormat{}); err != nil || fallback.Snapshot().Format != DefaultDeviceFormat() {
		t.Fatalf("invalid PlaybackQueueForFormat fallback = %+v/%v", fallback.Snapshot(), err)
	}
}

func TestRenderCallbackReportsExactZeroFilledUnderrun(t *testing.T) {
	q, err := NewPlaybackQueueWithLatency(PCM16DeviceFormat(48000), time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	q.Enqueue([]int16{11, 22, 33})
	rendered := []int16{9, 9, 9, 9, 9}
	if got := q.RenderInto(rendered); got != 3 {
		t.Fatalf("rendered source samples = %d, want 3", got)
	}
	if want := []int16{11, 22, 33, 0, 0}; !reflect.DeepEqual(rendered, want) {
		t.Fatalf("rendered callback = %v, want %v", rendered, want)
	}
	stats := q.Snapshot()
	if stats.CallbackCount != 1 || stats.RenderedSamples != 5 || stats.UnderflowEvents != 1 || stats.UnderflowSamples != 2 || stats.ZeroFilledSamples != 2 || stats.MinimumQueuedSamples != 3 {
		t.Fatalf("callback stats = %+v", stats)
	}
}

func TestPlaybackRenderObserverIncludesCallbackSilenceOutsideQueueLock(t *testing.T) {
	q, err := NewPlaybackQueueWithLatency(PCM16DeviceFormat(16000), time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	var observed []int16
	q.SetRenderObserver(func(rate int, samples []int16) {
		if rate != 16000 {
			t.Fatalf("observer rate = %d", rate)
		}
		// Snapshot taking the queue mutex proves callbacks run outside it.
		_ = q.Snapshot()
		observed = append(observed, samples...)
	})
	q.Enqueue([]int16{4, 5, 6})
	rendered := make([]int16, 5)
	if got := q.RenderInto(rendered); got != 3 {
		t.Fatalf("RenderInto = %d", got)
	}
	if !reflect.DeepEqual(observed, []int16{4, 5, 6, 0, 0}) {
		t.Fatalf("observer omitted callback silence or changed PCM: %v", observed)
	}

	observed = nil
	q.Enqueue([]int16{-7, 8})
	raw := make([]byte, 6)
	if got := q.ReadPCM16(raw); got != 2 {
		t.Fatalf("ReadPCM16 = %d", got)
	}
	if !reflect.DeepEqual(observed, []int16{-7, 8, 0}) {
		t.Fatalf("native observer PCM = %v", observed)
	}
	observed = nil
	q.RenderInto(make([]int16, 4))
	if !reflect.DeepEqual(observed, []int16{0, 0, 0, 0}) {
		t.Fatalf("empty-device callback omitted: %v", observed)
	}
}

func TestPlaybackQueueRingWrapPreservesFIFO(t *testing.T) {
	q, err := NewPlaybackQueueWithLatency(PCM16DeviceFormat(16000), time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	q.Enqueue(int16Samples(0, 12))
	if got := q.Dequeue(9); !reflect.DeepEqual(got, int16Samples(0, 9)) {
		t.Fatalf("prefix = %v", got)
	}
	q.Enqueue(int16Samples(12, 13))
	if got, want := q.Dequeue(16), int16Samples(9, 16); !reflect.DeepEqual(got, want) {
		t.Fatalf("wrapped FIFO = %v, want %v", got, want)
	}
}

func int16Samples(start, count int) []int16 {
	samples := make([]int16, count)
	for index := range samples {
		samples[index] = int16(start + index)
	}
	return samples
}
