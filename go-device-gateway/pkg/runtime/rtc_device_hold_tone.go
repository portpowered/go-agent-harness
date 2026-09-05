package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type timingClockContextKey struct{}

type timingClockValue struct {
	source platformclock.Source
}

// ErrInvalidSessionTimingClock identifies an explicit timing injection that
// cannot schedule timers. A timestamp-only source is sufficient for metadata
// but cannot safely drive a pacing worker.
var ErrInvalidSessionTimingClock = errors.New("session timing clock does not provide timers")

// WithTimingClock attaches the application-owned timing domain to a
// session context. The RTC hold-tone worker uses this source for its start
// timestamp, pacing timers, and real-audio observations. A source that does
// not implement TimerSource is treated as an invalid explicit injection and
// does not silently fall back to host time.
func WithTimingClock(ctx context.Context, source platformclock.Source) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, timingClockContextKey{}, timingClockValue{source: source})
}

func sessionTimingClock(ctx context.Context) (platformclock.TimerSource, bool) {
	if ctx != nil {
		if value, ok := ctx.Value(timingClockContextKey{}).(timingClockValue); ok {
			if value.source == nil {
				return nil, false
			}
			source, ok := value.source.(platformclock.TimerSource)
			return source, ok
		}
	}
	return platformclock.Real{}, true
}

// ValidateTimingClock verifies an explicitly injected source without
// substituting host time. A nil source means the caller did not inject a
// timing domain and is valid for the live default path.
func ValidateTimingClock(source platformclock.Source) error {
	if source == nil {
		return nil
	}
	if _, ok := source.(platformclock.TimerSource); !ok {
		return fmt.Errorf("%w: %T", ErrInvalidSessionTimingClock, source)
	}
	return nil
}

// defaultRTCDeviceHoldToneTick is how often the background filler checks
// whether a hold-tone pulse is due. It is much shorter than the shortest
// meaningful gap threshold or pulse, so the cue starts and stops close to
// its configured boundaries without needing a high-resolution scheduler of
// its own.
const defaultRTCDeviceHoldToneTick = 20 * time.Millisecond

// startHoldTone launches the background filler that keeps a customer from
// hearing true digital silence during a long gap on this sink's local
// device -- a turn-transition pause or a tool call in flight. It has to run
// on its own timer, independent of Pump's blocking inbound read: unlike a
// room mixer (see session_room_hold_tone.go), the provider's raw media
// reader (rtc.InboundMedia) never itself produces a "next expected frame"
// during a gap, so nothing calls observedWritePlayback again until the next
// real assistant frame arrives, however long that takes (see Pump and
// writeProviderFrame in rtc_device_sink.go).
//
// The returned stop function blocks until the goroutine has fully exited,
// so a caller can defer it ahead of releasing the underlying device without
// racing a last hold-tone write against device teardown.
func (s *RTCDeviceSink) startHoldTone(ctx context.Context) func() {
	stop, _ := s.startHoldToneChecked(ctx)
	return stop
}

// startHoldToneChecked is the initialization seam for owners that need to
// reject an invalid deterministic timing domain explicitly. The compatibility
// startHoldTone wrapper retains the historical stop-only signature used by
// the existing sink pump and live callers.
func (s *RTCDeviceSink) startHoldToneChecked(ctx context.Context) (func(), error) {
	if s == nil {
		return func() {}, nil
	}
	rate := s.SampleRate()
	if rate <= 0 {
		rate = audio.SampleRate
	}
	scheduler, validClock := sessionTimingClock(ctx)
	if !validClock || scheduler == nil {
		return func() {}, fmt.Errorf("%w: explicit source", ErrInvalidSessionTimingClock)
	}
	filler := audio.NewHoldToneFiller(s.holdToneConfig, rate, scheduler.Now())
	s.setHoldToneFiller(filler)

	tick := s.holdToneTick
	if tick <= 0 {
		tick = defaultRTCDeviceHoldToneTick
	}

	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			timer := scheduler.NewTimer(tick)
			if timer == nil {
				return
			}
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-stopCh:
				timer.Stop()
				return
			case now := <-timer.C():
				timer.Stop()
				if s.holdToneFeedbackConfirmed() {
					// Local hardware has already demonstrated real
					// speaker->mic coupling from this cue (see
					// rtcDevicePlaybackObserver.FeedbackConfirmed). Stop
					// generating more of it permanently for this Pump
					// lifetime: another pulse would only hand the feedback
					// gate another self-correlated event to reclassify
					// against, which can otherwise keep discarding a
					// genuinely independent, concurrent customer utterance
					// before it accumulates enough evidence to release (see
					// classifySuppressedCaptureLocked). The customer is
					// better served by silence again than by a cue that
					// risks masking their own barge-in.
					return
				}
				s.tickHoldTone(ctx, now, rate, tick)
			}
		}
	}()

	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() { close(stopCh) })
		<-done
		s.setHoldToneFiller(nil)
	}, nil
}

// holdToneFeedbackConfirmed reports whether this sink's paired observer (the
// local acoustic-feedback gate, when one is configured) has ever confirmed
// that captured audio from this device pairing is this sink's own played
// content looping back. A nil observer (no local feedback gate configured,
// e.g. a bare playback-only sink or a room's output device) reports false:
// there is nothing to detect a loop against, so the cue behaves exactly as
// it did before this safeguard existed.
func (s *RTCDeviceSink) holdToneFeedbackConfirmed() bool {
	if s == nil || s.observer == nil {
		return false
	}
	return s.observer.FeedbackConfirmed()
}

// tickHoldTone writes one hold-tone chunk if, and only if, the filler
// decides a pulse is due at now. It always routes through
// observedWritePlayback so a hold-tone chunk is subject to exactly the same
// self-hearing-feedback observation, playback generation, and
// barge-in-driven discard as real assistant audio: a concurrent
// RESPONSE.CANCEL (rtc_device_runtime.go -> DiscardPlayback) always wins,
// because a stale generation or a blocked playback boundary makes the write
// a no-op the same way it would for a real frame (see writePlayback).
func (s *RTCDeviceSink) tickHoldTone(ctx context.Context, now time.Time, rate int, tick time.Duration) {
	n := int(tick.Seconds() * float64(rate))
	if n <= 0 {
		return
	}
	frame := s.holdToneNextFrame(now, n)
	if len(frame) == 0 {
		return
	}
	generation, blocked := s.playbackState()
	_ = s.observedWritePlayback(ctx, frame, generation, blocked, false)
}

// holdToneMu-guarded accessors below serialize every interaction with the
// active *audio.HoldToneFiller. The filler itself keeps no internal
// synchronization (it is designed as a single-threaded, deterministically
// testable core, see hold_tone_filler.go) because it is genuinely driven by
// two independent goroutines here: the background ticker (tickHoldTone) and
// Pump's own goroutine observing real provider frames
// (observeHoldToneRealFrame). holdToneMu is that seam.

func (s *RTCDeviceSink) setHoldToneFiller(filler *audio.HoldToneFiller) {
	if s == nil {
		return
	}
	s.holdToneMu.Lock()
	s.holdToneFiller = filler
	s.holdToneMu.Unlock()
}

func (s *RTCDeviceSink) holdToneNextFrame(now time.Time, n int) []int16 {
	if s == nil {
		return nil
	}
	s.holdToneMu.Lock()
	defer s.holdToneMu.Unlock()
	if s.holdToneFiller == nil {
		return nil
	}
	return s.holdToneFiller.NextFrame(now, n)
}

func (s *RTCDeviceSink) holdToneObserveRealAudio(now time.Time) []int16 {
	if s == nil {
		return nil
	}
	s.holdToneMu.Lock()
	defer s.holdToneMu.Unlock()
	if s.holdToneFiller == nil {
		return nil
	}
	return s.holdToneFiller.ObserveRealAudio(now)
}

// observeHoldToneRealFrame tells the active hold-tone filler that genuine
// assistant audio is about to be written, and flushes any short fade-out
// tail it returns (see HoldToneFiller.ObserveRealAudio) ahead of that real
// frame so the cue never overlaps, or clicks against, real audio. Called
// once per accepted provider frame, before that frame reaches the device.
func (s *RTCDeviceSink) observeHoldToneRealFrame(ctx context.Context, generation uint64, blocked bool) error {
	scheduler, validClock := sessionTimingClock(ctx)
	if !validClock || scheduler == nil {
		return nil
	}
	tail := s.holdToneObserveRealAudio(scheduler.Now())
	if len(tail) == 0 {
		return nil
	}
	return s.observedWritePlayback(ctx, tail, generation, blocked, false)
}

// SetHoldToneConfig updates the filler profile used by subsequent hold-tone
// ticks. It is intended for session composition and deterministic tests.
func (s *RTCDeviceSink) SetHoldToneConfig(config audio.HoldToneConfig) {
	if s == nil {
		return
	}
	s.holdToneMu.Lock()
	s.holdToneConfig = config
	s.holdToneMu.Unlock()
}

// SetHoldToneTick updates the scheduler interval used by the hold-tone worker.
func (s *RTCDeviceSink) SetHoldToneTick(tick time.Duration) {
	if s == nil || tick <= 0 {
		return
	}
	s.holdToneMu.Lock()
	s.holdToneTick = tick
	s.holdToneMu.Unlock()
}

// TimingClock resolves the scheduler-backed timing source attached to ctx.
func TimingClock(ctx context.Context) (platformclock.TimerSource, bool) {
	return sessionTimingClock(ctx)
}

// StartHoldToneChecked starts the hold-tone worker and reports an invalid
// explicitly injected timing source before launching it.
func (s *RTCDeviceSink) StartHoldToneChecked(ctx context.Context) (func(), error) {
	return s.startHoldToneChecked(ctx)
}
