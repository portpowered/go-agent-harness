package runtime

import (
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"testing"
	"time"
)

func TestPlaybackBufferSnapshotUsesOnlyCachedMemory(t *testing.T) {
	sink := &RTCDeviceSink{}
	sink.snapshotStats.Store(&audio.PlaybackQueueStats{CapacitySamples: 4000, QueuedSamples: 120, RenderedSamples: 300, UnderflowSamples: 60, DiscardedSamples: 10})
	sink.snapshotEpoch.Store(7)
	// Both locks may be held while a device operation waits for its backend.
	// Loop snapshots must remain independent from those operations.
	sink.mu.Lock()
	sink.playbackMu.Lock()
	defer sink.mu.Unlock()
	defer sink.playbackMu.Unlock()
	result := make(chan audio.BufferStats, 1)
	go func() { result <- sink.PlaybackBufferSnapshot() }()
	select {
	case got := <-result:
		if got.Epoch != 7 || got.QueuedSamples != 120 || got.ConsumedSamples != 240 || got.AdmittedSamples != 370 || got.CapacitySamples != 4000 {
			t.Fatalf("cached snapshot=%+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("loop snapshot waited for a device worker lock")
	}
	sink.snapshotClosed.Store(true)
	if !sink.PlaybackBufferSnapshot().Closed {
		t.Fatal("closed snapshot not visible")
	}
}
