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
	response   rtcDevicePlaybackIdentity
	generation uint64
	remaining  int
	precise    bool
}
type rtcDevicePlaybackReservation struct {
	id         uint64
	kind       RTCDevicePlaybackObservationKind
	response   rtcDevicePlaybackIdentity
	generation uint64
	start      uint64
	length     int
	precise    bool
}
type rtcDevicePlaybackObservationState struct {
	mu                   sync.Mutex
	supported            bool
	closed               bool
	subscription         *RTCDevicePlaybackObservationSubscription
	segments             []rtcDevicePlaybackSegment
	pendingSamples       uint64
	deviceClock          uint64
	sequence             uint64
	nextSegmentID        uint64
	metadataLostSamples  uint64
	lastRenderedSamples  uint64
	lastUnderflowSamples uint64
	pendingDiscards      []rtcDevicePlaybackPendingDiscard
	nextRenderID         uint64
	renderEpoch          uint64
	pendingRenders       []rtcDevicePlaybackRender
}

type rtcDevicePlaybackPendingDiscard struct {
	deviceID devicegw.DeviceID
	rate     int
	segments []rtcDevicePlaybackSegment
	cutover  uint64
	reason   string
}

type rtcDevicePlaybackRender struct {
	id          uint64
	deviceID    devicegw.DeviceID
	rate        int
	samples     []int16
	sampleCount int
	epoch       uint64
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
		DeviceID: deviceID, PlaybackResponse: reservation.response.asPlaybackResponse(), Generation: reservation.generation, SampleRate: rate,
		StartSample: reservation.start,
		EndSample:   reservation.start + uint64(reservation.length),
		SampleCount: reservation.length, Accepted: true, Precise: reservation.precise,
	}
	event.Reason = reservation.response.reason()
	event.Samples = copyObservationSamples(samples, &event.Precise, &event.Reason)
	event.PCM = event.Samples
	s.publishLocked(event)
	s.mu.Unlock()
}

func (s *RTCDeviceSink) observeDeviceRender(rate int, samples []int16) {
	if s == nil {
		return
	}
	renderID := s.playbackObservations.beginRender(s.id, rate, samples)
	if renderID == 0 {
		s.playbackObservations.accountUntrackedRender(s.id, rate, samples)
	} else {
		select {
		case <-s.renderStop:
			s.playbackObservations.dropRender(renderID)
		case s.renderWork <- renderID:
		default:
			s.playbackObservations.dropRender(renderID)
		}
	}
	s.renderObserverMu.RLock()
	observer := s.renderedSamplesObserver
	s.renderObserverMu.RUnlock()
	if observer != nil {
		observer(rate, samples)
	}
}

func (s *RTCDeviceSink) runRenderObservations() {
	defer close(s.renderDone)
	process := func(renderID uint64) {
		stats := s.sink.PlaybackStats()
		s.playbackBoundaryMu.Lock()
		s.playbackObservations.completeRender(renderID, stats)
		s.playbackBoundaryMu.Unlock()
	}
	for {
		select {
		case renderID := <-s.renderWork:
			process(renderID)
		case <-s.renderStop:
			for {
				select {
				case renderID := <-s.renderWork:
					process(renderID)
				default:
					return
				}
			}
		}
	}
}

func (s *RTCDeviceSink) PlaybackConsumptionSupported() bool {
	return s != nil && s.renderBoundarySupported.Load()
}

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

func (s *RTCDeviceSink) discardPlaybackObservations(reason string, generation uint64) int {
	if s == nil || s.sink == nil {
		return 0
	}
	s.playbackBoundaryMu.Lock()
	defer s.playbackBoundaryMu.Unlock()
	before, discarded := s.sink.PlaybackStats(), s.sink.DiscardPlayback()
	if discarded == 0 && s.renderBoundarySupported.Load() {
		s.playbackNoopDiscards.Add(1)
	}
	after := s.sink.PlaybackStats()
	s.playbackObservations.discard(s.id, s.deviceRate, generation, reason, discarded, before, after)
	return discarded
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

func (s *RTCDeviceSink) PlaybackCommand(ctx context.Context, operation audio.PlaybackOperation) error {
	if s == nil || s.commands == nil {
		return ErrRTCDeviceSinkClosed
	}
	return s.commands.Exchange(ctx, operation, audio.PlaybackResponse{}).Err
}

type rtcDevicePlaybackSpan struct {
	response rtcDevicePlaybackIdentity
	start    uint64
	end      uint64
	complete bool
}

func (s *RTCDeviceSink) StartPlayback(response audio.PlaybackResponse) {
	if s == nil || s.sink == nil || response.ItemID == "" {
		return
	}
	identity := newRTCDevicePlaybackIdentity(response)
	s.playbackMu.Lock()
	if s.playbackResponse.equal(identity) && !s.playbackBlocked {
		s.playbackMu.Unlock()
		return
	}
	if s.playbackBlocked {
		s.playbackBlocked = false
		s.playbackGeneration++
		s.snapshotEpoch.Store(s.playbackGeneration)
	}
	s.playbackResponse = identity
	s.playbackMu.Unlock()
}

func (s *RTCDeviceSink) PlaybackController() audio.PlaybackController {
	if s == nil {
		return nil
	}
	return s
}

func (s *RTCDeviceSink) finishPlayback(response audio.PlaybackResponse) {
	if s == nil || response.ItemID == "" {
		return
	}
	identity := newRTCDevicePlaybackIdentity(response)
	s.playbackMu.Lock()
	defer s.playbackMu.Unlock()
	for index := len(s.playbackSpans) - 1; index >= 0; index-- {
		if s.playbackSpans[index].response.equal(identity) {
			s.playbackSpans[index].complete = true
			break
		}
	}
	if !s.playbackResponse.equal(identity) {
		return
	}
	s.playbackGeneration++
	s.snapshotEpoch.Store(s.playbackGeneration)
	s.playbackResponse = rtcDevicePlaybackIdentity{}
}

func consumedPlaybackSamples(stats audio.PlaybackQueueStats) uint64 {
	if stats.RenderedSamples < stats.UnderflowSamples {
		return 0
	}
	return stats.RenderedSamples - stats.UnderflowSamples
}

func (s *RTCDeviceSink) resumePlayback() {
	if s == nil || s.sink == nil {
		return
	}
	s.playbackMu.Lock()
	if !s.playbackBlocked {
		s.playbackMu.Unlock()
		return
	}
	s.playbackBlocked = false
	s.playbackGeneration++
	s.snapshotEpoch.Store(s.playbackGeneration)
	s.playbackMu.Unlock()
}
