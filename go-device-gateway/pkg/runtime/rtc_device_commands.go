package runtime

import (
	"context"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

type rtcDevicePlaybackSegment struct {
	id         uint64
	kind       RTCDevicePlaybackObservationKind
	response   audio.PlaybackResponse
	generation uint64
	remaining  int
	precise    bool
}

type rtcDevicePlaybackReservation struct {
	id         uint64
	kind       RTCDevicePlaybackObservationKind
	response   audio.PlaybackResponse
	generation uint64
	start      uint64
	length     int
	precise    bool
}

type rtcDevicePlaybackObservationState struct {
	mu                  sync.Mutex
	supported           bool
	closed              bool
	subscription        *RTCDevicePlaybackObservationSubscription
	segments            []rtcDevicePlaybackSegment
	pendingSamples      uint64
	deviceClock         uint64
	sequence            uint64
	nextSegmentID       uint64
	metadataLostSamples uint64
}

func playbackResponseForFrame(current, frame audio.PlaybackResponse, modelAudio bool) audio.PlaybackResponse {
	if modelAudio && frame.ItemID == "" {
		return current
	}
	return frame
}

func (s *RTCDeviceSink) writeDeviceSamples(ctx context.Context, samples []int16) error {
	if len(samples) == audio.FrameSize {
		return s.sink.WriteFrame(ctx, samples)
	}
	return s.sink.WriteSamples(ctx, samples)
}

func (s *rtcDevicePlaybackObservationState) detach(subscription *RTCDevicePlaybackObservationSubscription) {
	s.mu.Lock()
	if s.subscription == subscription {
		s.subscription = nil
	}
	s.mu.Unlock()
}

func (s *rtcDevicePlaybackObservationState) closeSubscription() {
	s.mu.Lock()
	s.closed = true
	subscription := s.subscription
	s.subscription = nil
	s.mu.Unlock()
	if subscription != nil {
		subscription.closeOnce.Do(func() {
			subscription.closed.Store(true)
			close(subscription.done)
		})
	}
}

func (s *rtcDevicePlaybackObservationState) publishLocked(event RTCDevicePlaybackObservation) {
	s.sequence++
	event.Sequence = s.sequence
	event.DeviceRange = RTCDeviceSampleRange{StartSample: event.StartSample, EndSample: event.EndSample}
	event.ResponseID = event.PlaybackResponse.ResponseID
	event.ItemID = event.PlaybackResponse.ItemID
	event.ContentIndex = event.PlaybackResponse.ContentIndex
	if event.SampleCount == 0 && len(event.Samples) > 0 {
		event.SampleCount = len(event.Samples)
	}
	if len(event.PCM) == 0 && len(event.Samples) > 0 {
		event.PCM = event.Samples
	}
	if len(event.Samples) == 0 && len(event.PCM) > 0 {
		event.Samples = event.PCM
	}
	subscription := s.subscription
	if subscription == nil || subscription.closed.Load() {
		return
	}
	select {
	case subscription.events <- event:
		subscription.published.Add(1)
	default:
		subscription.dropped.Add(1)
		subscription.droppedSamples.Add(uint64(maxInt(event.SampleCount, 0)))
	}
}

func (s *rtcDevicePlaybackObservationState) commit(reservation rtcDevicePlaybackReservation, samples []int16, deviceID devicegw.DeviceID, rate int) {
	if reservation.length <= 0 {
		return
	}
	s.mu.Lock()
	event := RTCDevicePlaybackObservation{
		Kind: RTCDevicePlaybackAdmission, ContentKind: reservation.kind,
		DeviceID: deviceID, PlaybackResponse: reservation.response, Generation: reservation.generation, SampleRate: rate,
		StartSample: reservation.start,
		EndSample:   reservation.start + uint64(reservation.length),
		SampleCount: reservation.length, Accepted: true, Precise: reservation.precise,
	}
	event.Samples = copyObservationSamples(samples, &event.Precise, &event.Reason)
	event.PCM = event.Samples
	s.publishLocked(event)
	s.mu.Unlock()
}

// observeDeviceRender is installed once on construction. It fans the actual
// callback into the bounded correlation port before invoking the legacy raw
// observer, preserving the old API while keeping the new pull consumer out of
// native callback execution.
func (s *RTCDeviceSink) observeDeviceRender(rate int, samples []int16) {
	if s == nil {
		return
	}
	s.playbackObservations.render(s.id, rate, samples)
	s.renderObserverMu.RLock()
	observer := s.renderedSamplesObserver
	s.renderObserverMu.RUnlock()
	if observer != nil {
		observer(rate, samples)
	}
}

// PlaybackConsumptionSupported reports whether this sink is attached to a
// backend that exposes the physical callback/consumption edge.
func (s *RTCDeviceSink) PlaybackConsumptionSupported() bool {
	return s != nil && s.renderBoundarySupported.Load()
}

// SubscribePlaybackObservations creates the one bounded pull subscription for
// this sink. A replacement closes the prior subscription without waiting for
// its caller. No user callback is run by the device callback.
func (s *RTCDeviceSink) SubscribePlaybackObservations(capacity ...int) (*RTCDevicePlaybackObservationSubscription, error) {
	if s == nil || !s.PlaybackConsumptionSupported() {
		return nil, ErrRTCDevicePlaybackObservationUnsupported
	}
	buffer := DefaultRTCDevicePlaybackObservationCapacity
	if len(capacity) > 1 {
		return nil, ErrInvalidRTCDevicePlaybackObservationCapacity
	}
	if len(capacity) == 1 {
		buffer = capacity[0]
	}
	if buffer <= 0 || buffer > MaxRTCDevicePlaybackObservationCapacity {
		return nil, ErrInvalidRTCDevicePlaybackObservationCapacity
	}
	subscription := &RTCDevicePlaybackObservationSubscription{
		events: make(chan RTCDevicePlaybackObservation, buffer),
		done:   make(chan struct{}),
		state:  &s.playbackObservations,
	}
	s.playbackObservations.mu.Lock()
	if s.playbackObservations.closed {
		s.playbackObservations.mu.Unlock()
		return nil, ErrRTCDevicePlaybackObservationClosed
	}
	previous := s.playbackObservations.subscription
	s.playbackObservations.subscription = subscription
	s.playbackObservations.mu.Unlock()
	if previous != nil {
		if err := previous.Close(); err != nil {
			return nil, fmt.Errorf("close previous playback observation subscription: %w", err)
		}
	}
	return subscription, nil
}

// PlaybackObservationStats returns aggregate correlation/device-clock state
// even when no subscription is currently attached.
func (s *RTCDeviceSink) PlaybackObservationStats() RTCDevicePlaybackObservationStats {
	if s == nil {
		return RTCDevicePlaybackObservationStats{Closed: true}
	}
	s.playbackObservations.mu.Lock()
	stats := RTCDevicePlaybackObservationStats{
		Supported:     s.playbackObservations.supported,
		Closed:        s.playbackObservations.closed,
		DeviceSamples: s.playbackObservations.deviceClock,
		LastSequence:  s.playbackObservations.sequence,
	}
	if subscription := s.playbackObservations.subscription; subscription != nil {
		stats.PublishedObservations = subscription.published.Load()
		stats.DroppedObservations = subscription.dropped.Load()
		stats.DroppedSamples = subscription.droppedSamples.Load()
	}
	stats.MetadataLostSamples = s.playbackObservations.metadataLostSamples
	s.playbackObservations.mu.Unlock()
	return stats
}

func (s *RTCDeviceSink) closePlaybackObservations() {
	if s != nil {
		s.playbackObservations.closeSubscription()
	}
}

func (s *RTCDeviceSink) discardPlaybackObservations(reason string, generation uint64) {
	if s != nil {
		s.playbackObservations.discard(s.id, s.deviceRate, generation, reason)
	}
}

func (s *RTCDeviceSink) reconcilePlaybackObservationDrops(before, after audio.PlaybackQueueStats) {
	if after.DroppedSamples <= before.DroppedSamples {
		return
	}
	delta := after.DroppedSamples - before.DroppedSamples
	max := uint64(^uint(0) >> 1)
	if delta > max {
		delta = max
	}
	s.playbackObservations.dropQueued(int(delta))
}

// runPlaybackCommands is a device worker separate from the PCM pump. An
// interrupt can therefore discard a full output queue and wake its producer.
func (s *RTCDeviceSink) runPlaybackCommands() {
	defer close(s.commandDone)
	for {
		request, err := s.commands.Receive(s.lifeCtx)
		if err != nil {
			return
		}
		receipt := audio.PlaybackReceipt{Applied: true}
		switch request.Operation {
		case audio.PlaybackStart:
			s.StartPlayback(request.Response)
		case audio.PlaybackDiscard:
			if request.Epoch == 0 {
				s.DiscardPlayback()
			} else {
				_, receipt.Applied = s.discardPlaybackAtEpoch(request.Epoch)
				if !receipt.Applied {
					receipt.Err = audio.ErrStalePlaybackCommand
				}
			}
		case audio.PlaybackResume:
			s.resumePlayback()
		case audio.PlaybackInterrupt:
			receipt.Interruption.PlaybackResponse = request.Response
			receipt.Interruption.AudioEndMS, receipt.Applied = s.InterruptPlayback(request.Response)
		case audio.PlaybackInterruptActive:
			receipt.Interruption, receipt.Applied = s.InterruptActivePlayback()
		default:
			receipt.Applied = false
			receipt.Err = fmt.Errorf("unknown playback operation %d", request.Operation)
		}
		request.Complete(receipt)
	}
}

// PlaybackCommand applies one ordered playback control operation and waits
// for its receipt. PCM pumping remains independent from this command worker.
func (s *RTCDeviceSink) PlaybackCommand(ctx context.Context, operation audio.PlaybackOperation) error {
	if s == nil || s.commands == nil {
		return ErrRTCDeviceSinkClosed
	}
	return s.commands.Exchange(ctx, operation, audio.PlaybackResponse{}).Err
}

// rtcDevicePlaybackSpan maps one provider response onto the monotonic count
// of complete samples consumed by the physical device, including underflow
// silence. Multiple spans may be queued at once so a tool continuation stays
// gapless without confusing the latest response with audible speech.
type rtcDevicePlaybackSpan struct {
	response audio.PlaybackResponse
	start    uint64
	end      uint64
	complete bool
}

// StartPlayback opens a provider response on the local device clock. The
// consumed-sample baseline is captured immediately before the first model
// frame is admitted, so idle underflow and hold-tone samples are excluded.
func (s *RTCDeviceSink) StartPlayback(response audio.PlaybackResponse) {
	if s == nil || s.sink == nil || response.ItemID == "" {
		return
	}
	s.playbackMu.Lock()
	if s.playbackResponse == response && !s.playbackBlocked {
		s.playbackMu.Unlock()
		return
	}
	if s.playbackBlocked {
		s.playbackBlocked = false
		s.playbackGeneration++
		s.snapshotEpoch.Store(s.playbackGeneration)
	}
	s.playbackResponse = response
	s.playbackMu.Unlock()
}

// PlaybackController exposes the sink's device-clocked interruption state to
// an owning live session. Callers that only need ordinary PCM pumping can
// ignore this optional capability.
func (s *RTCDeviceSink) PlaybackController() audio.PlaybackController {
	if s == nil {
		return nil
	}
	return s
}

// finishPlayback retires a fully drained provider response from the device
// clock. A later server-VAD event belongs to a new user turn and must not
// truncate the completed response, even if local hold-tone audio played during
// the intervening silence.
func (s *RTCDeviceSink) finishPlayback(response audio.PlaybackResponse) {
	if s == nil || response.ItemID == "" {
		return
	}
	s.playbackMu.Lock()
	defer s.playbackMu.Unlock()
	for index := len(s.playbackSpans) - 1; index >= 0; index-- {
		if s.playbackSpans[index].response == response {
			s.playbackSpans[index].complete = true
			break
		}
	}
	// A continuation may already be the latest prefetched response when the
	// preceding response reaches its provider boundary. Retire only the
	// response that is currently active; completing an older span must not
	// advance the shared generation and invalidate the continuation's queued
	// frames.
	if s.playbackResponse != response {
		return
	}
	s.playbackGeneration++
	s.snapshotEpoch.Store(s.playbackGeneration)
	s.playbackResponse = audio.PlaybackResponse{}
}

func consumedPlaybackSamples(stats audio.PlaybackQueueStats) uint64 {
	if stats.RenderedSamples < stats.UnderflowSamples {
		return 0
	}
	return stats.RenderedSamples - stats.UnderflowSamples
}

// resumePlayback opens a new local response boundary. Frames read under a
// prior generation remain stale even if they race with this transition.
func (s *RTCDeviceSink) resumePlayback() {
	if s == nil || s.sink == nil {
		return
	}
	s.playbackMu.Lock()
	// A normal tool-result continuation can request another response while the
	// preceding response is still draining to the physical device. Playback is
	// already open in that case: advancing the generation would make a frame
	// read just before response.create stale and discard its samples. Only a
	// prior accepted cancellation sets playbackBlocked and requires a new
	// generation boundary.
	if !s.playbackBlocked {
		s.playbackMu.Unlock()
		return
	}
	s.playbackBlocked = false
	s.playbackGeneration++
	s.snapshotEpoch.Store(s.playbackGeneration)
	s.playbackMu.Unlock()
}
