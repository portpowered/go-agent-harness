package audio

import (
	"encoding/binary"
	"math"
)

// This file implements output-loudness normalization. Voice selection on a
// realtime provider (e.g. --voice verse vs --voice alloy) can render at a
// materially different RMS level with no compensating gain, which makes one
// room participant sound dramatically quieter than another on the same
// hardware. LoudnessNormalizer corrects that with a fixed, per-voice gain
// (owned by the services package's voice registry) plus a peak-safe ceiling
// that guarantees normalization can never itself introduce clipping.
//
// A static per-voice gain was chosen over live-measured adaptive
// normalization after evaluating both against this codebase's audio test
// suite. Two things point at a static table specifically:
//
//  1. The measured evidence for the underlying defect (alloy vs verse) is a
//     systematic, content-independent offset: it reproduced identically
//     across independent live runs with non-overlapping distributions, not a
//     one-off content artifact. A fixed correction is the right shape of fix
//     for a fixed offset.
//  2. A great many existing tests -- fan-out routing, replay determinism,
//     device round-trips, turn-latency, feedback-gate correlation -- assert
//     byte-exact PCM equality as a convenient way to check an unrelated
//     property (the right bytes reached the right place), independent of
//     loudness. An adaptive normalizer that re-measures and adjusts gain for
//     every stream changes those bytes for every voice, including ones that
//     were never reported as a problem. A static table that defaults unlisted
//     or unset voices to exactly 0 dB leaves every one of those tests
//     bit-for-bit unchanged, while still correcting the two voices with
//     actual measurements.
//
// Only alloy (the reference/target level) and verse (the measured ~7.3 dB
// deficit) have independent loudness measurements today. The remaining
// built-in voices default to 0 dB: that is deliberately conservative --
// it neither fabricates a correction from no evidence nor makes an
// unmeasured voice's level worse -- rather than guessed.
const (
	// LoudnessPeakCeilingDBFS is the hard output peak ceiling enforced when a
	// voice's gain boosts a chunk, so normalization can never itself
	// introduce clipping. It sits comfortably below full scale and keeps a
	// boosted voice's peaks from exceeding the neighborhood of the
	// already-clean reference voice's peaks, which matters because the room
	// mixer sums participant streams without independent headroom management
	// (tracked separately; see room-mix-clipping notes in the PR).
	// Normalizing a quiet voice up to, not past, the reference voice's
	// existing envelope avoids making that separate issue worse.
	LoudnessPeakCeilingDBFS = -1.0

	loudnessFullScale = 32768.0
)

// LoudnessNormalizerConfig controls one fixed-gain PCM16 stream processor.
type LoudnessNormalizerConfig struct {
	// GainDB is the constant gain applied to every sample. 0 (the zero
	// value) is a documented, exact no-op: Process and ProcessBytes return
	// unmodified copies of their input.
	GainDB float64
	// PeakCeilingDBFS is the safety ceiling enforced only while GainDB boosts
	// the signal (GainDB > 0). Zero selects LoudnessPeakCeilingDBFS.
	PeakCeilingDBFS float64
}

func (c LoudnessNormalizerConfig) normalized() LoudnessNormalizerConfig {
	if c.PeakCeilingDBFS == 0 {
		c.PeakCeilingDBFS = LoudnessPeakCeilingDBFS
	}
	return c
}

// LoudnessNormalizer applies a constant, peak-safe gain to a PCM16 stream. A
// fixed gain cannot pump, breathe, or duck: it does not adapt to content, so
// it cannot cause the artifacts a live-measured normalizer would need
// explicit smoothing to avoid.
type LoudnessNormalizer struct {
	gainLinear    float64
	boost         bool
	ceilingLinear float64
}

// NewLoudnessNormalizer creates a normalizer that applies config.GainDB to
// every sample it processes.
func NewLoudnessNormalizer(config LoudnessNormalizerConfig) *LoudnessNormalizer {
	cfg := config.normalized()
	return &LoudnessNormalizer{
		gainLinear:    dbToLinear(cfg.GainDB),
		boost:         cfg.GainDB > 0,
		ceilingLinear: dbToLinear(cfg.PeakCeilingDBFS) * loudnessFullScale,
	}
}

// GainDB reports the normalizer's constant gain in dB.
func (n *LoudnessNormalizer) GainDB() float64 {
	if n == nil || n.gainLinear <= 0 {
		return 0
	}
	return 20 * math.Log10(n.gainLinear)
}

// Process applies the configured gain to samples and returns a new slice;
// the input is never mutated. An empty input returns nil. A normalizer with
// exactly 0 dB gain returns a plain copy without any floating-point
// round-trip, so unmodified voices are guaranteed bit-identical.
func (n *LoudnessNormalizer) Process(samples []int16) []int16 {
	if n == nil || len(samples) == 0 {
		return nil
	}
	if n.gainLinear == 1.0 {
		out := make([]int16, len(samples))
		copy(out, samples)
		return out
	}
	return n.applyGain(samples)
}

// ProcessBytes is the little-endian PCM16 byte spelling of Process, matching
// the wire representation carried on StreamMessage AUDIO.DELTA and room
// mixer inputs. An odd-length input is a wire-format error owned by the
// caller and is returned unchanged.
func (n *LoudnessNormalizer) ProcessBytes(pcm []byte) []byte {
	if n == nil || len(pcm) < 2 || len(pcm)%2 != 0 {
		return pcm
	}
	samples := make([]int16, len(pcm)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(pcm[index*2:])) //nolint:gosec // PCM16 bit pattern is intentional
	}
	out := n.Process(samples)
	encoded := make([]byte, len(out)*2)
	for index, sample := range out {
		binary.LittleEndian.PutUint16(encoded[index*2:], uint16(sample)) //nolint:gosec // PCM16 bit pattern is intentional
	}
	return encoded
}

// applyGain scales samples by the constant gain. When the gain boosts the
// signal (n.boost), it also enforces the peak ceiling with one proportional
// per-chunk safety scale-down so a boosted voice can never clip even on
// already-peaky content -- this is the only case normalization can
// introduce clipping, since a unity or attenuating gain can only leave peak
// amplitude the same or lower than the input already was.
func (n *LoudnessNormalizer) applyGain(samples []int16) []int16 {
	scaled := make([]float64, len(samples))
	peak := 0.0
	for index, sample := range samples {
		value := float64(sample) * n.gainLinear
		scaled[index] = value
		if n.boost {
			if absValue := math.Abs(value); absValue > peak {
				peak = absValue
			}
		}
	}
	limit := 1.0
	if n.boost && peak > n.ceilingLinear && peak > 0 {
		limit = n.ceilingLinear / peak
	}
	out := make([]int16, len(samples))
	for index, value := range scaled {
		out[index] = clampInt16(value * limit)
	}
	return out
}

func dbToLinear(db float64) float64 {
	return math.Pow(10, db/20)
}

func clampInt16(value float64) int16 {
	switch {
	case value >= 32767:
		return 32767
	case value <= -32768:
		return -32768
	default:
		return int16(math.Round(value))
	}
}
