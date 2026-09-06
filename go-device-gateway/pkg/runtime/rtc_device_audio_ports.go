package runtime

import "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

// PlaybackBufferPort is the observation-only capability for a sink's live
// queue. It is intentionally separate from RTCDeviceSink so loop code cannot
// accidentally call a device operation while taking a tick snapshot.
type PlaybackBufferPort struct {
	Sink *RTCDeviceSink
}

func (p PlaybackBufferPort) Snapshot() audio.BufferStats {
	if p.Sink == nil {
		return audio.BufferStats{Closed: true}
	}
	return p.Sink.PlaybackBufferSnapshot()
}

// PlaybackBufferSnapshot adapts the live device playback queue to the
// loop's memory-only observation shape. The queue remains owned by the sink;
// this method only takes its synchronized snapshot and never performs a
// playback operation.
func (s *RTCDeviceSink) PlaybackBufferSnapshot() audio.BufferStats {
	if s == nil {
		return audio.BufferStats{Closed: true}
	}
	result := audio.BufferStats{Epoch: s.snapshotEpoch.Load(), Closed: s.snapshotClosed.Load()}
	if stats := s.snapshotStats.Load(); stats != nil {
		result.CapacitySamples = stats.CapacitySamples
		result.QueuedSamples = stats.QueuedSamples
		result.ConsumedSamples = consumedPlaybackSamples(*stats)
		result.AdmittedSamples = result.ConsumedSamples + uint64(stats.QueuedSamples) + stats.DiscardedSamples + stats.DroppedSamples
		result.DiscardedSamples = stats.DiscardedSamples
	}
	return result
}

// PlaybackCommands returns the bounded command queue consumed by the sink's
// independent playback worker. Loop admission must target this port so an
// interrupt is queued even when PCM production or device rendering is slow.
func (s *RTCDeviceSink) PlaybackCommands() *audio.PlaybackCommands {
	if s == nil {
		return nil
	}
	return s.commands
}

// PlaybackBuffer returns the observation-only capability for the sink's
// production playback queue.
func (s *RTCDeviceSink) PlaybackBuffer() PlaybackBufferPort {
	return PlaybackBufferPort{Sink: s}
}

// discardPlaybackAtEpoch applies an admitted loop interrupt only when it is
// newer than the generation already observed by the sink. It is called by the
// playback command worker, so admission remains a bounded queue operation and
// never performs device mutation on a loop tick.
func (s *RTCDeviceSink) discardPlaybackAtEpoch(epoch uint64) (int, bool) {
	if s == nil || s.sink == nil {
		return 0, false
	}
	s.playbackMu.Lock()
	defer s.playbackMu.Unlock()
	if epoch <= s.playbackGeneration {
		return 0, false
	}
	s.playbackBlocked = true
	s.playbackGeneration = epoch
	s.snapshotEpoch.Store(s.playbackGeneration)
	return s.sink.DiscardPlayback(), true
}

// CaptureBufferSnapshot returns the live capture handoff observation. It is
// kept as a helper for callers that retain only the runtime binding seam.
func (b *BufferedCapture) CaptureBufferSnapshot() audio.BufferStats {
	if b == nil {
		return audio.BufferStats{Closed: true}
	}
	return b.control.Snapshot()
}
