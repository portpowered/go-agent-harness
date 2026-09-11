package runtime

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

const maxRTCDevicePlaybackIdentityBytes = 256
const rtcDevicePlaybackIdentityOverflowReason = "playback response identity exceeded bounded diagnostic storage"
const maxRTCDevicePlaybackPendingDiscards = maxRTCDevicePlaybackObservationSegments

// rtcDevicePlaybackIdentity is the bounded representation retained by the
// runtime. Short provider identifiers remain readable; oversized identifiers
// retain only a fixed digest and a bounded display value. The raw response is
// used only at the public call edge and is never kept in queues or spans.
type rtcDevicePlaybackIdentity struct {
	responseID    string
	itemID        string
	responseKey   [sha256.Size]byte
	itemKey       [sha256.Size]byte
	hasResponseID bool
	hasItemID     bool
	contentIndex  int
	precise       bool
}

func newRTCDevicePlaybackIdentity(response audio.PlaybackResponse) rtcDevicePlaybackIdentity {
	identity := rtcDevicePlaybackIdentity{contentIndex: response.ContentIndex, precise: true}
	identity.responseID, identity.responseKey, identity.hasResponseID, identity.precise = boundedRTCDevicePlaybackID(response.ResponseID)
	var itemPrecise bool
	identity.itemID, identity.itemKey, identity.hasItemID, itemPrecise = boundedRTCDevicePlaybackID(response.ItemID)
	identity.precise = identity.precise && itemPrecise
	return identity
}

func boundedRTCDevicePlaybackID(raw string) (string, [sha256.Size]byte, bool, bool) {
	if raw == "" {
		return "", [sha256.Size]byte{}, false, true
	}
	digest := sha256.Sum256([]byte(raw))
	if len(raw) <= maxRTCDevicePlaybackIdentityBytes {
		return string([]byte(raw)), digest, true, true
	}
	return "sha256:" + hex.EncodeToString(digest[:]), digest, true, false
}

func (identity rtcDevicePlaybackIdentity) asPlaybackResponse() audio.PlaybackResponse {
	return audio.PlaybackResponse{
		ResponseID:   identity.responseID,
		ItemID:       identity.itemID,
		ContentIndex: identity.contentIndex,
	}
}

func (identity rtcDevicePlaybackIdentity) equal(other rtcDevicePlaybackIdentity) bool {
	return identity.hasResponseID == other.hasResponseID && identity.responseKey == other.responseKey &&
		identity.hasItemID == other.hasItemID && identity.itemKey == other.itemKey &&
		identity.contentIndex == other.contentIndex
}

func (identity rtcDevicePlaybackIdentity) reason() string {
	if identity.precise {
		return ""
	}
	return rtcDevicePlaybackIdentityOverflowReason
}

func (identity rtcDevicePlaybackIdentity) hasItem() bool {
	return identity.hasItemID
}

func playbackResponseForFrame(current rtcDevicePlaybackIdentity, frame audio.PlaybackResponse, modelAudio bool) rtcDevicePlaybackIdentity {
	if modelAudio && frame.ItemID == "" {
		return current
	}
	return newRTCDevicePlaybackIdentity(frame)
}

func (s *RTCDeviceSink) recordPlaybackSpanAtLocked(response rtcDevicePlaybackIdentity, start uint64, samples int, consumed uint64) {
	if !response.hasItem() || samples <= 0 {
		return
	}
	s.prunePlaybackSpansLocked(consumed)
	end := start + uint64(samples)
	if n := len(s.playbackSpans); n > 0 && s.playbackSpans[n-1].response.equal(response) && s.playbackSpans[n-1].end == start {
		s.playbackSpans[n-1].end = end
		return
	}
	s.playbackSpans = append(s.playbackSpans, rtcDevicePlaybackSpan{response: response, start: start, end: end})
}

func playbackStatsHaveRenderClock(stats audio.PlaybackQueueStats) bool {
	return stats.Format.SampleRate > 0 || stats.CallbackCount > 0 || stats.RenderedSamples > 0 || stats.UnderflowSamples > 0
}

func playbackRenderSampleCount(before, after audio.PlaybackQueueStats, callbackSamples int) (int, bool) {
	if callbackSamples < 0 || !playbackStatsHaveRenderClock(before) || !playbackStatsHaveRenderClock(after) ||
		after.RenderedSamples < before.RenderedSamples || after.UnderflowSamples < before.UnderflowSamples {
		return 0, false
	}
	rendered := after.RenderedSamples - before.RenderedSamples
	underflow := after.UnderflowSamples - before.UnderflowSamples
	if rendered != uint64(callbackSamples) || underflow > rendered {
		return 0, false
	}
	return callbackSamples - int(underflow), true
}

func playbackDiscardCutover(before, after audio.PlaybackQueueStats) (uint64, bool) {
	if !playbackStatsHaveRenderClock(before) || !playbackStatsHaveRenderClock(after) ||
		after.RenderedSamples < before.RenderedSamples || after.UnderflowSamples < before.UnderflowSamples {
		return 0, false
	}
	rendered := after.RenderedSamples - before.RenderedSamples
	underflow := after.UnderflowSamples - before.UnderflowSamples
	if underflow > rendered {
		return 0, false
	}
	return before.RenderedSamples + rendered, true
}

func boundedObservationCount(count, available int) int {
	if count < 0 || count > available {
		return 0
	}
	return count
}

func (s *rtcDevicePlaybackObservationState) consumeModelChunkLocked(deviceID devicegw.DeviceID, rate int, start uint64, samples []int16, sampleCount, offset, remaining int) int {
	if len(s.segments) == 0 {
		return s.consumeUnattributedChunkLocked(deviceID, rate, start, samples, sampleCount, offset, remaining)
	}
	segment := &s.segments[0]
	take := minInt(segment.remaining, remaining)
	kind := playbackObservationKind(segment)
	s.publishRangeLocked(deviceID, rate, kind, segment.kind, segment.response.asPlaybackResponse(), segment.generation, start+uint64(offset), take, observationPCM(samples, sampleCount, offset, take), true, segment.precise && kind == RTCDevicePlaybackConsumed, segment.response.reason())
	segment.remaining -= take
	s.pendingSamples -= uint64(take)
	if segment.remaining == 0 {
		s.segments = s.segments[1:]
	}
	return take
}

func (s *rtcDevicePlaybackObservationState) consumeUnattributedChunkLocked(deviceID devicegw.DeviceID, rate int, start uint64, samples []int16, sampleCount, offset, remaining int) int {
	take := minInt(remaining, int(s.pendingSamples))
	if take <= 0 {
		return 0
	}
	s.publishRangeLocked(deviceID, rate, RTCDevicePlaybackUnattributed, RTCDevicePlaybackUnattributed, audio.PlaybackResponse{}, 0, start+uint64(offset), take, observationPCM(samples, sampleCount, offset, take), true, false, "device consumed samples without retained admission metadata")
	s.pendingSamples -= uint64(take)
	return take
}

func playbackObservationKind(segment *rtcDevicePlaybackSegment) RTCDevicePlaybackObservationKind {
	if segment.kind == RTCDevicePlaybackHoldTone || segment.kind == RTCDevicePlaybackCue {
		return segment.kind
	}
	if segment.kind == RTCDevicePlaybackUnattributed || !segment.precise {
		return RTCDevicePlaybackUnattributed
	}
	return RTCDevicePlaybackConsumed
}

func (s *rtcDevicePlaybackObservationState) consumeModelLocked(deviceID devicegw.DeviceID, rate int, start uint64, samples []int16, sampleCount, count int) {
	count = boundedObservationCount(count, sampleCount)
	offset := 0
	for offset < count {
		take := s.consumeModelChunkLocked(deviceID, rate, start, samples, sampleCount, offset, count-offset)
		if take == 0 {
			return
		}
		offset += take
	}
}

func (s *rtcDevicePlaybackObservationState) beginRender(deviceID devicegw.DeviceID, rate int, samples []int16) uint64 {
	if len(samples) == 0 {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pendingRenders) >= maxRTCDevicePlaybackObservationSegments {
		return 0
	}
	s.nextRenderID++
	render := rtcDevicePlaybackRender{id: s.nextRenderID, deviceID: deviceID, rate: rate, sampleCount: len(samples), epoch: s.renderEpoch}
	if len(samples) <= maxRTCDevicePlaybackEventSamples {
		render.samples = append([]int16(nil), samples...)
	}
	s.pendingRenders = append(s.pendingRenders, render)
	return render.id
}

func (s *rtcDevicePlaybackObservationState) completeRender(id uint64, stats audio.PlaybackQueueStats) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := -1
	for candidate := range s.pendingRenders {
		if s.pendingRenders[candidate].id == id {
			index = candidate
			break
		}
	}
	if index < 0 {
		return
	}
	render := s.pendingRenders[index]
	s.pendingRenders = append(s.pendingRenders[:index], s.pendingRenders[index+1:]...)
	modelSamples, exact := playbackRenderSampleCount(audio.PlaybackQueueStats{
		RenderedSamples: s.lastRenderedSamples, UnderflowSamples: s.lastUnderflowSamples,
		Format:        stats.Format,
		CallbackCount: stats.CallbackCount,
	}, stats, render.sampleCount)
	if exact {
		s.lastRenderedSamples = stats.RenderedSamples
		s.lastUnderflowSamples = stats.UnderflowSamples
	} else if render.epoch > s.renderEpoch {
		modelSamples = 0
	} else {
		modelSamples = minInt(render.sampleCount, int(s.pendingSamples))
	}
	start := s.deviceClock
	s.deviceClock += uint64(render.sampleCount)
	s.consumeModelLocked(render.deviceID, render.rate, start, render.samples, render.sampleCount, modelSamples)
	if modelSamples < render.sampleCount {
		offset := modelSamples
		s.publishRangeLocked(render.deviceID, render.rate, RTCDevicePlaybackUnderflow, RTCDevicePlaybackUnderflow, audio.PlaybackResponse{}, 0, start+uint64(offset), render.sampleCount-offset, observationPCM(render.samples, render.sampleCount, offset, render.sampleCount-offset), true, false, "device callback zero-filled an unavailable queue range")
	}
	s.flushPendingDiscardsLocked()
}

func (s *rtcDevicePlaybackObservationState) dropRender(id uint64) {
	s.completeRender(id, audio.PlaybackQueueStats{})
}

func observationPCM(samples []int16, sampleCount, offset, count int) []int16 {
	if count <= 0 || len(samples) != sampleCount || offset < 0 || offset+count > len(samples) {
		return nil
	}
	return samples[offset : offset+count]
}

func (s *rtcDevicePlaybackObservationState) removeTailLocked(samples int) []rtcDevicePlaybackSegment {
	if samples <= 0 {
		return nil
	}
	remaining := samples
	reversed := make([]rtcDevicePlaybackSegment, 0, minInt(len(s.segments), samples))
	for index := len(s.segments) - 1; index >= 0 && remaining > 0; index-- {
		segment := &s.segments[index]
		take := minInt(segment.remaining, remaining)
		copy := *segment
		copy.remaining = take
		reversed = append(reversed, copy)
		segment.remaining -= take
		remaining -= take
		s.pendingSamples -= uint64(take)
		if segment.remaining == 0 {
			s.segments = s.segments[:index]
		}
	}
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	if remaining > 0 {
		s.metadataLostSamples += uint64(remaining)
	}
	return reversed
}

func (s *rtcDevicePlaybackObservationState) publishDiscardLocked(pending rtcDevicePlaybackPendingDiscard) {
	offset := uint64(0)
	for _, segment := range pending.segments {
		event := RTCDevicePlaybackObservation{
			Kind: RTCDevicePlaybackDiscard, ContentKind: segment.kind,
			DeviceID: pending.deviceID, PlaybackResponse: segment.response.asPlaybackResponse(),
			Generation: segment.generation, SampleRate: pending.rate,
			StartSample: pending.cutover + offset, EndSample: pending.cutover + offset + uint64(segment.remaining),
			SampleCount: segment.remaining, Precise: segment.precise,
			Reason: appendReason(segment.response.reason(), pending.reason),
		}
		s.publishLocked(event)
		offset += uint64(segment.remaining)
	}
}

func (s *rtcDevicePlaybackObservationState) flushPendingDiscardsLocked() {
	for len(s.pendingDiscards) > 0 && s.deviceClock >= s.pendingDiscards[0].cutover {
		s.publishDiscardLocked(s.pendingDiscards[0])
		s.pendingDiscards = s.pendingDiscards[1:]
	}
}

func (s *rtcDevicePlaybackObservationState) discard(deviceID devicegw.DeviceID, rate int, generation uint64, reason string, nativeDiscarded int, before, after audio.PlaybackQueueStats) {
	s.mu.Lock()
	cutover := s.deviceClock
	if exact, ok := playbackDiscardCutover(before, after); ok {
		cutover = exact
	}
	segments := s.removeTailLocked(nativeDiscarded)
	if len(segments) > 0 {
		pending := rtcDevicePlaybackPendingDiscard{deviceID: deviceID, rate: rate, segments: segments, cutover: cutover, reason: reason}
		if s.deviceClock >= cutover {
			s.publishDiscardLocked(pending)
		} else if len(s.pendingDiscards) < maxRTCDevicePlaybackPendingDiscards {
			s.pendingDiscards = append(s.pendingDiscards, pending)
		} else {
			for _, segment := range segments {
				s.metadataLostSamples += uint64(segment.remaining)
			}
		}
	}
	s.renderEpoch++
	s.flushPendingDiscardsLocked()
	_ = generation
	s.mu.Unlock()
}

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
	discarded := s.discardPlaybackObservations("epoch discard", s.playbackGeneration)
	s.playbackBlocked = true
	s.playbackGeneration = epoch
	s.snapshotEpoch.Store(s.playbackGeneration)
	return discarded, true
}

// CaptureBufferSnapshot returns the live capture handoff observation. It is
// kept as a helper for callers that retain only the runtime binding seam.
func (b *BufferedCapture) CaptureBufferSnapshot() audio.BufferStats {
	if b == nil {
		return audio.BufferStats{Closed: true}
	}
	return b.control.Snapshot()
}
