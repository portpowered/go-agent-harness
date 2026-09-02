package services

import (
	"context"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
)

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
	if s == nil {
		return func() {}
	}
	rate := s.SampleRate()
	if rate <= 0 {
		rate = audio.SampleRate
	}
	filler := audio.NewHoldToneFiller(s.holdToneConfig, rate, time.Now())
	s.setHoldToneFiller(filler)

	tick := s.holdToneTick
	if tick <= 0 {
		tick = defaultRTCDeviceHoldToneTick
	}

	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			case now := <-ticker.C:
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
	}
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
	tail := s.holdToneObserveRealAudio(time.Now())
	if len(tail) == 0 {
		return nil
	}
	return s.observedWritePlayback(ctx, tail, generation, blocked, false)
}
