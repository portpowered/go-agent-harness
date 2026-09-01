package services

import (
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
)

// applyRoomHoldTone decides what bytes should actually reach a human room
// participant's speaker for one mixed cadence frame. The room mixer always
// ticks in real time and emits an exact all-zero frame whenever no active
// input contributed samples (see pcm16HasSignal's doc comment), so unlike
// the bare local-device path (rtc_device_hold_tone.go) this never needs its
// own timer or background goroutine: every call to pumpRoomHumanOutput
// already lands on the mixer's own real-time cadence, so substituting a
// hold-tone frame for a silent one here is enough to cover both a
// turn-transition gap and a tool-call round trip -- the mixer does not
// distinguish the two, it just reports "no active input right now" either
// way.
//
// filler may be nil (hold tone disabled for this participant); the frame is
// returned unchanged in that case.
func applyRoomHoldTone(filler *audio.HoldToneFiller, now time.Time, frame []byte) []byte {
	if filler == nil {
		return frame
	}
	if pcm16HasSignal(frame) {
		// Real audio: tell the filler immediately so it cannot fire again
		// until a fresh gap builds up, and flush any short fade-out tail
		// ahead of the real frame so the cue never overlaps or clicks
		// against it (see HoldToneFiller.ObserveRealAudio).
		if tail := filler.ObserveRealAudio(now); len(tail) > 0 {
			return append(encodeRoomPCM16(tail), frame...)
		}
		return frame
	}
	sampleCount := len(frame) / 2
	if sampleCount <= 0 {
		return frame
	}
	fill := filler.NextFrame(now, sampleCount)
	if len(fill) == 0 {
		// Either still within an ordinary conversational pause, or between
		// pulses: leave the true silent frame untouched.
		return frame
	}
	return encodeRoomPCM16(fill)
}
