package audio

import (
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

func int16Samples(start, count int) []int16 {
	samples := make([]int16, count)
	for index := range samples {
		samples[index] = int16(start + index)
	}
	return samples
}
