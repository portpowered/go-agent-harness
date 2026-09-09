package runtime

import (
	"context"
	"io"
	"sync"
	"sync/atomic"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

// CaptureFilter is the narrow feedback gate seam used by the capture worker.
type CaptureFilter interface {
	FilterCapture(context.Context, []int16) ([][]int16, error)
	DiscardHeld()
}

// PlaybackObserver is the narrow feedback gate seam used by the playback
// worker. It keeps device runtime independent from the feedback implementation.
type PlaybackObserver interface {
	WritePlayback(context.Context, []int16, func() error) error
	FeedbackConfirmed() bool
}

var _ CaptureFilter = (*audio.PCM16FeedbackGate)(nil)
var _ PlaybackObserver = (*audio.PCM16FeedbackGate)(nil)

type rtcDevicePlaybackObservationError string

func (e rtcDevicePlaybackObservationError) Error() string { return string(e) }

const (
	ErrRTCDevicePlaybackObservationUnsupported     = rtcDevicePlaybackObservationError("RTC device playback consumption observation is unsupported")
	ErrRTCDevicePlaybackObservationClosed          = rtcDevicePlaybackObservationError("RTC device playback observation is closed")
	ErrInvalidRTCDevicePlaybackObservationCapacity = rtcDevicePlaybackObservationError("invalid RTC device playback observation capacity")
	ErrInvalidRTCDevicePlaybackObservationContext  = rtcDevicePlaybackObservationError("RTC device playback observation context is nil")
)

const (
	DefaultRTCDevicePlaybackObservationCapacity = 128
	MaxRTCDevicePlaybackObservationCapacity     = 4096
	maxRTCDevicePlaybackObservationSegments     = 256
	maxRTCDevicePlaybackRetainedSamples         = 8192
	maxRTCDevicePlaybackEventSamples            = 4096
)

// RTCDevicePlaybackObservationKind identifies the phase represented by one
// observation. Admission and discard ranges are projected queue ranges;
// consumed, underflow and hold-tone ranges are actual device callback ranges.
type RTCDevicePlaybackObservationKind string

const (
	RTCDevicePlaybackAdmission    RTCDevicePlaybackObservationKind = "admission"
	RTCDevicePlaybackConsumed     RTCDevicePlaybackObservationKind = "consumed"
	RTCDevicePlaybackUnderflow    RTCDevicePlaybackObservationKind = "underflow"
	RTCDevicePlaybackHoldTone     RTCDevicePlaybackObservationKind = "hold_tone"
	RTCDevicePlaybackCue          RTCDevicePlaybackObservationKind = "cue"
	RTCDevicePlaybackDiscard      RTCDevicePlaybackObservationKind = "discard"
	RTCDevicePlaybackUnattributed RTCDevicePlaybackObservationKind = "unattributed"
)

// RTCDeviceSampleRange is a half-open range in the selected device's native
// sample-clock domain. It is also exposed directly for consumers that do not
// want to reconstruct the range from an observation's convenience fields.
type RTCDeviceSampleRange struct {
	StartSample uint64
	EndSample   uint64
}

// RTCDevicePlaybackObservation is a bounded, copied receipt from the public
// device boundary. StartSample and EndSample are a half-open range in the
// native device sample clock, never provider-rate or wall-clock units.
// SampleCount remains authoritative when PCM is omitted after a bounded-loss
// condition. PlaybackResponse is empty for silence, cues and unknown ranges.
type RTCDevicePlaybackObservation struct {
	Sequence         uint64
	Kind             RTCDevicePlaybackObservationKind
	ContentKind      RTCDevicePlaybackObservationKind
	DeviceID         devicegw.DeviceID
	PlaybackResponse audio.PlaybackResponse
	ResponseID       string
	ItemID           string
	ContentIndex     int
	Generation       uint64
	SampleRate       int
	DeviceRange      RTCDeviceSampleRange
	StartSample      uint64
	EndSample        uint64
	SampleCount      int
	Consumed         bool
	Precise          bool
	Accepted         bool
	PCM              []int16
	Samples          []int16
	Reason           string
}

// RTCDevicePlaybackObservationStats reports delivery and correlation loss
// independently from audio loss. DroppedObservations counts events rejected
// by a full subscriber queue; MetadataLostSamples counts queued samples whose
// response identity could no longer be retained. LastSequence lets a consumer
// detect gaps even when the final event itself was dropped.
type RTCDevicePlaybackObservationStats struct {
	Supported             bool
	Closed                bool
	PublishedObservations uint64
	DroppedObservations   uint64
	DroppedSamples        uint64
	MetadataLostSamples   uint64
	DeviceSamples         uint64
	LastSequence          uint64
}

// RTCDevicePlaybackObservationSubscription is a pull-only diagnostic port.
// Native callbacks perform a nonblocking send into its finite queue; caller
// code never runs on the callback and a stalled caller cannot grow a worker
// or queue without limit.
type RTCDevicePlaybackObservationSubscription struct {
	events         chan RTCDevicePlaybackObservation
	done           chan struct{}
	closeOnce      sync.Once
	closed         atomic.Bool
	state          *rtcDevicePlaybackObservationState
	published      atomic.Uint64
	dropped        atomic.Uint64
	droppedSamples atomic.Uint64
}

// Next receives the next observation, preserving queue order and sequence
// gaps. After Close or sink shutdown it drains already queued observations and
// then returns io.EOF.
func (s *RTCDevicePlaybackObservationSubscription) Next(ctx context.Context) (RTCDevicePlaybackObservation, error) {
	if s == nil {
		return RTCDevicePlaybackObservation{}, ErrRTCDevicePlaybackObservationClosed
	}
	if ctx == nil {
		return RTCDevicePlaybackObservation{}, ErrInvalidRTCDevicePlaybackObservationContext
	}
	for {
		select {
		case event := <-s.events:
			return event, nil
		default:
		}
		if s.closed.Load() {
			return RTCDevicePlaybackObservation{}, io.EOF
		}
		select {
		case event := <-s.events:
			return event, nil
		case <-s.done:
			continue
		case <-ctx.Done():
			return RTCDevicePlaybackObservation{}, ctx.Err()
		}
	}
}

// Receive is an alias for Next for consumers that model the subscription as
// a bounded receipt stream.
func (s *RTCDevicePlaybackObservationSubscription) Receive(ctx context.Context) (RTCDevicePlaybackObservation, error) {
	return s.Next(ctx)
}

// Stats returns loss counters even when the last diagnostic event was dropped.
func (s *RTCDevicePlaybackObservationSubscription) Stats() RTCDevicePlaybackObservationStats {
	if s == nil {
		return RTCDevicePlaybackObservationStats{Closed: true}
	}
	stats := RTCDevicePlaybackObservationStats{
		PublishedObservations: s.published.Load(),
		DroppedObservations:   s.dropped.Load(),
		DroppedSamples:        s.droppedSamples.Load(),
		Closed:                s.closed.Load(),
	}
	if s.state == nil {
		return stats
	}
	s.state.mu.Lock()
	stats.Supported = s.state.supported
	stats.MetadataLostSamples = s.state.metadataLostSamples
	stats.DeviceSamples = s.state.deviceClock
	stats.LastSequence = s.state.sequence
	if s.state.closed {
		stats.Closed = true
	}
	s.state.mu.Unlock()
	return stats
}

// Close detaches the subscription without waiting for a caller-owned reader.
func (s *RTCDevicePlaybackObservationSubscription) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		close(s.done)
		if s.state != nil {
			s.state.detach(s)
		}
	})
	return nil
}

func (s *rtcDevicePlaybackObservationState) cancel(reservation rtcDevicePlaybackReservation) {
	if reservation.length <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	remaining := reservation.length
	for index := len(s.segments) - 1; index >= 0 && remaining > 0; index-- {
		segment := &s.segments[index]
		if segment.id != reservation.id {
			continue
		}
		removed := minInt(segment.remaining, remaining)
		segment.remaining -= removed
		remaining -= removed
		s.pendingSamples -= uint64(removed)
		if segment.remaining == 0 {
			s.segments = append(s.segments[:index], s.segments[index+1:]...)
		}
	}
}

func (s *rtcDevicePlaybackObservationState) reserve(kind RTCDevicePlaybackObservationKind, response rtcDevicePlaybackIdentity, generation uint64, samples int) rtcDevicePlaybackReservation {
	s.mu.Lock()
	defer s.mu.Unlock()
	if samples <= 0 {
		return rtcDevicePlaybackReservation{}
	}
	s.nextSegmentID++
	reservation := rtcDevicePlaybackReservation{id: s.nextSegmentID, kind: kind, response: response, generation: generation, start: s.deviceClock + s.pendingSamples, length: samples, precise: kind != RTCDevicePlaybackUnattributed && response.hasItem() && response.precise}
	lossy := len(s.segments) >= maxRTCDevicePlaybackObservationSegments || s.pendingSamples+uint64(samples) > maxRTCDevicePlaybackRetainedSamples
	if !lossy {
		s.segments = append(s.segments, rtcDevicePlaybackSegment{id: reservation.id, kind: kind, response: response, generation: generation, remaining: samples, precise: reservation.precise})
	} else {
		reservation.id, reservation.kind, reservation.response, reservation.precise = s.nextSegmentID, RTCDevicePlaybackUnattributed, rtcDevicePlaybackIdentity{}, false
		s.metadataLostSamples += uint64(samples)
		if len(s.segments) > 0 && s.segments[len(s.segments)-1].kind == RTCDevicePlaybackUnattributed {
			tail := &s.segments[len(s.segments)-1]
			tail.remaining += samples
			tail.generation = 0
			reservation.id = tail.id
		} else if len(s.segments) < maxRTCDevicePlaybackObservationSegments {
			s.segments = append(s.segments, rtcDevicePlaybackSegment{id: reservation.id, kind: RTCDevicePlaybackUnattributed, remaining: samples})
		} else {
			tail := &s.segments[len(s.segments)-1]
			s.metadataLostSamples += uint64(tail.remaining)
			tail.kind, tail.response, tail.generation, tail.precise = RTCDevicePlaybackUnattributed, rtcDevicePlaybackIdentity{}, 0, false
			tail.remaining += samples
			reservation.id = tail.id
		}
	}
	s.pendingSamples += uint64(samples)
	return reservation
}

func (s *rtcDevicePlaybackObservationState) dropQueued(samples int) {
	if samples <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	remaining := samples
	for len(s.segments) > 0 && remaining > 0 {
		segment := &s.segments[0]
		removed := minInt(segment.remaining, remaining)
		segment.remaining -= removed
		remaining -= removed
		s.pendingSamples -= uint64(removed)
		if segment.remaining == 0 {
			s.segments = s.segments[1:]
		}
	}
	if remaining > 0 {
		removed := minInt(remaining, int(s.pendingSamples))
		s.pendingSamples -= uint64(removed)
		s.metadataLostSamples += uint64(removed)
	}
}

func (s *rtcDevicePlaybackObservationState) accountUntrackedRender(deviceID devicegw.DeviceID, rate int, samples []int16) {
	if len(samples) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	start := s.deviceClock
	modelSamples := minInt(len(samples), int(s.pendingSamples))
	s.deviceClock += uint64(len(samples))
	s.consumeModelLocked(deviceID, rate, start, samples, len(samples), modelSamples)
	if modelSamples < len(samples) {
		s.publishRangeLocked(deviceID, rate, RTCDevicePlaybackUnderflow, RTCDevicePlaybackUnderflow, audio.PlaybackResponse{}, 0, start+uint64(modelSamples), len(samples)-modelSamples, observationPCM(samples, len(samples), modelSamples, len(samples)-modelSamples), true, false, "device callback correlation queue saturated; remaining range was zero-filled")
	}
	s.flushPendingDiscardsLocked()
}

func (s *rtcDevicePlaybackObservationState) publishRangeLocked(deviceID devicegw.DeviceID, rate int, kind, contentKind RTCDevicePlaybackObservationKind, response audio.PlaybackResponse, generation uint64, start uint64, sampleCount int, samples []int16, actual, precise bool, reason string) {
	event := RTCDevicePlaybackObservation{
		Kind: kind, ContentKind: contentKind, DeviceID: deviceID,
		PlaybackResponse: response, Generation: generation, SampleRate: rate,
		StartSample: start, EndSample: start + uint64(sampleCount),
		SampleCount: sampleCount, Consumed: actual, Precise: precise, Reason: reason,
	}
	event.Samples = copyObservationSamplesForCount(samples, sampleCount, &event.Precise, &event.Reason)
	event.PCM = event.Samples
	s.publishLocked(event)
}

func copyObservationSamples(samples []int16, precise *bool, reason *string) []int16 {
	if len(samples) == 0 {
		return nil
	}
	if len(samples) > maxRTCDevicePlaybackEventSamples {
		*precise = false
		*reason = appendReason(*reason, "PCM omitted after event-size bound")
		return nil
	}
	return append([]int16(nil), samples...)
}

func copyObservationSamplesForCount(samples []int16, sampleCount int, precise *bool, reason *string) []int16 {
	if sampleCount <= 0 {
		return nil
	}
	if len(samples) != sampleCount {
		*precise = false
		*reason = appendReason(*reason, "PCM omitted after callback-size bound")
		return nil
	}
	return copyObservationSamples(samples, precise, reason)
}

func appendReason(current, addition string) string {
	if current == "" {
		return addition
	}
	return current + "; " + addition
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(value, floor int) int {
	if value < floor {
		return floor
	}
	return value
}
