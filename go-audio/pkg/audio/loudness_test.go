package audio_test

import (
	"math"
	"testing"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// syntheticVoiceChunks builds deterministic pseudo-speech content at a
// requested RMS dBFS level, split into 20 ms chunks (the room mixer cadence)
// so tests exercise the normalizer the same way a live delta stream would.
// The signal sums two incommensurate tones so it is not a pure sine (whose
// crest factor would be unrealistically low) while remaining fully
// deterministic across test runs.
func syntheticVoiceChunks(rmsDBFS float64, seconds float64, sampleRate int) [][]int16 {
	totalSamples := int(seconds * float64(sampleRate))
	amplitude := 32768.0 * math.Pow(10, rmsDBFS/20) * math.Sqrt(2)
	samples := make([]int16, totalSamples)
	for index := range samples {
		phase := float64(index) / float64(sampleRate)
		value := amplitude * 0.6 * math.Sin(2*math.Pi*220*phase)
		value += amplitude * 0.4 * math.Sin(2*math.Pi*370*phase+0.7)
		if value > 32767 {
			value = 32767
		}
		if value < -32768 {
			value = -32768
		}
		samples[index] = int16(math.Round(value))
	}
	chunkSamples := sampleRate / 50 // 20 ms
	var chunks [][]int16
	for offset := 0; offset < len(samples); offset += chunkSamples {
		end := offset + chunkSamples
		if end > len(samples) {
			end = len(samples)
		}
		chunks = append(chunks, samples[offset:end])
	}
	return chunks
}

func measureRMSDBFS(samples []int16) float64 {
	if len(samples) == 0 {
		return math.Inf(-1)
	}
	var sum float64
	for _, s := range samples {
		f := float64(s)
		sum += f * f
	}
	rms := math.Sqrt(sum / float64(len(samples)))
	if rms <= 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(rms/32768.0)
}

func countClipped(samples []int16, threshold int) int {
	count := 0
	for _, s := range samples {
		v := int(s)
		if v < 0 {
			v = -v
		}
		if v >= threshold {
			count++
		}
	}
	return count
}

func processAll(n *audio.LoudnessNormalizer, chunks [][]int16) []int16 {
	var out []int16
	for _, chunk := range chunks {
		out = append(out, n.Process(chunk)...)
	}
	return out
}

// TestLoudnessNormalizerConvergesDifferentVoicesToSharedTarget is the
// required regression: two synthetic streams at deliberately different RMS
// levels (modeling the measured ~7.3 dB alloy/verse gap) must land within
// the probe's stated ~3 dB band of each other after each is normalized by
// its own voice's fixed gain -- 0 dB for the "alloy" stand-in, +7.3 dB
// (the measured deficit) for the "verse" stand-in.
func TestLoudnessNormalizerConvergesDifferentVoicesToSharedTarget(t *testing.T) {
	const sampleRate = 24000
	loudChunks := syntheticVoiceChunks(-19.0, 1.0, sampleRate)  // "alloy"
	quietChunks := syntheticVoiceChunks(-26.3, 1.0, sampleRate) // "verse"

	loudNorm := audio.NewLoudnessNormalizer(audio.LoudnessNormalizerConfig{GainDB: 0})
	quietNorm := audio.NewLoudnessNormalizer(audio.LoudnessNormalizerConfig{GainDB: 7.3})

	loudOut := processAll(loudNorm, loudChunks)
	quietOut := processAll(quietNorm, quietChunks)

	loudRMS := measureRMSDBFS(loudOut)
	quietRMS := measureRMSDBFS(quietOut)
	gap := loudRMS - quietRMS
	if gap < 0 {
		gap = -gap
	}
	if gap > 3.0 {
		t.Fatalf("post-normalization RMS gap = %.2f dB (loud=%.2f dBFS, quiet=%.2f dBFS), want <= 3.0 dB", gap, loudRMS, quietRMS)
	}
}

// TestLoudnessNormalizerZeroGainIsExactNoOp confirms the design invariant
// that makes this safe to wire into every audio path unconditionally: an
// unset or unmeasured voice (0 dB) must reproduce its input bit-for-bit, so
// none of the many existing tests that assert exact PCM equality for
// unrelated reasons (fan-out routing, replay determinism, device
// round-trips) are disturbed by normalization being present.
func TestLoudnessNormalizerZeroGainIsExactNoOp(t *testing.T) {
	norm := audio.NewLoudnessNormalizer(audio.LoudnessNormalizerConfig{GainDB: 0})
	chunks := syntheticVoiceChunks(-12.0, 0.25, 24000)
	// Include a chunk that already touches full scale, to confirm the
	// no-boost path never engages the peak ceiling either.
	hot := make([]int16, 480)
	for i := range hot {
		if i%2 == 0 {
			hot[i] = 32767
		} else {
			hot[i] = -32768
		}
	}
	chunks = append(chunks, hot)

	for _, chunk := range chunks {
		out := norm.Process(chunk)
		if len(out) != len(chunk) {
			t.Fatalf("0 dB output length = %d, want %d", len(out), len(chunk))
		}
		for i := range chunk {
			if out[i] != chunk[i] {
				t.Fatalf("0 dB gain changed sample %d: got %d, want %d (exact passthrough)", i, out[i], chunk[i])
			}
		}
	}
}

// TestLoudnessNormalizerDoesNotClipAlreadyHotInput is the required
// regression: normalization must not introduce clipping, including when the
// input is already loud, using the production +7.3 dB verse gain.
func TestLoudnessNormalizerDoesNotClipAlreadyHotInput(t *testing.T) {
	chunks := syntheticVoiceChunks(-1.0, 1.0, 24000)
	norm := audio.NewLoudnessNormalizer(audio.LoudnessNormalizerConfig{GainDB: 7.3})

	out := processAll(norm, chunks)
	if clipped := countClipped(out, audio.PCM16AnalysisClipSampleThreshold); clipped != 0 {
		t.Fatalf("hot input produced %d clipped samples after +7.3 dB normalization, want 0", clipped)
	}
}

// TestLoudnessNormalizerPeakSafetyOnFullScaleTransient covers a worst-case
// input that is already at digital full scale on every sample; even the
// production boost must not push it past int16 range or the documented
// clip threshold.
func TestLoudnessNormalizerPeakSafetyOnFullScaleTransient(t *testing.T) {
	norm := audio.NewLoudnessNormalizer(audio.LoudnessNormalizerConfig{GainDB: 7.3})
	hot := make([]int16, 480)
	for i := range hot {
		if i%2 == 0 {
			hot[i] = 32767
		} else {
			hot[i] = -32768
		}
	}
	out := norm.Process(hot)
	if clipped := countClipped(out, audio.PCM16AnalysisClipSampleThreshold); clipped != 0 {
		t.Fatalf("full-scale transient produced %d clipped samples after +7.3 dB normalization, want 0", clipped)
	}
}

// TestLoudnessNormalizerPeakSafetyAtLargestProductionGain repeats the
// full-scale-transient safety check at +15.1 dB, the largest entry in the
// production per-voice table (measured for "sage"; see
// services.VoiceLoudnessGainDB). A boost this large is exactly the case
// where the peak ceiling is most likely to engage, so it is the sharpest
// test of the "must not clip" guarantee.
func TestLoudnessNormalizerPeakSafetyAtLargestProductionGain(t *testing.T) {
	norm := audio.NewLoudnessNormalizer(audio.LoudnessNormalizerConfig{GainDB: 15.1})
	hot := make([]int16, 480)
	for i := range hot {
		if i%2 == 0 {
			hot[i] = 32767
		} else {
			hot[i] = -32768
		}
	}
	out := norm.Process(hot)
	if clipped := countClipped(out, audio.PCM16AnalysisClipSampleThreshold); clipped != 0 {
		t.Fatalf("full-scale transient produced %d clipped samples after +15.1 dB normalization, want 0", clipped)
	}
}

// TestLoudnessNormalizerDoesNotAmplifySilence is the required regression: a
// genuinely quiet passage must not be amplified into audible noise floor. A
// fixed, bounded gain applied uniformly cannot turn near-silence into
// something audible; this pins that a generous boost still leaves a quiet
// passage far below any speech-audible level.
func TestLoudnessNormalizerDoesNotAmplifySilence(t *testing.T) {
	norm := audio.NewLoudnessNormalizer(audio.LoudnessNormalizerConfig{GainDB: 7.3})

	silence := make([]int16, 24000) // 1 second at 24 kHz
	for i := range silence {
		// Deterministic tiny alternating dither, RMS well below -70 dBFS.
		if i%2 == 0 {
			silence[i] = 2
		} else {
			silence[i] = -2
		}
	}

	out := norm.Process(silence)
	outRMS := measureRMSDBFS(out)
	if outRMS > -50.0 {
		t.Fatalf("near-silent passage measured %.2f dBFS after +7.3 dB normalization, want it to remain near the noise floor", outRMS)
	}
}

// TestLoudnessNormalizerProcessBytesRoundTrips confirms the little-endian
// PCM16 byte spelling used at the room/session wiring boundary matches the
// []int16 API and never changes length.
func TestLoudnessNormalizerProcessBytesRoundTrips(t *testing.T) {
	norm := audio.NewLoudnessNormalizer(audio.LoudnessNormalizerConfig{GainDB: 7.3})
	chunks := syntheticVoiceChunks(-20.0, 0.5, 24000)

	for _, chunk := range chunks {
		pcm := make([]byte, len(chunk)*2)
		for i, s := range chunk {
			pcm[i*2] = byte(uint16(s))
			pcm[i*2+1] = byte(uint16(s) >> 8)
		}
		out := norm.ProcessBytes(pcm)
		if len(out) != len(pcm) {
			t.Fatalf("ProcessBytes changed length: got %d bytes, want %d", len(out), len(pcm))
		}
	}
}

// TestLoudnessNormalizerGainDBReportsConfiguredGain pins the reporting seam
// used for diagnostics and by these tests.
func TestLoudnessNormalizerGainDBReportsConfiguredGain(t *testing.T) {
	norm := audio.NewLoudnessNormalizer(audio.LoudnessNormalizerConfig{GainDB: 7.3})
	if got := norm.GainDB(); math.Abs(got-7.3) > 0.01 {
		t.Fatalf("GainDB() = %.4f, want 7.3", got)
	}
	zero := audio.NewLoudnessNormalizer(audio.LoudnessNormalizerConfig{})
	if got := zero.GainDB(); got != 0 {
		t.Fatalf("GainDB() for zero-value config = %.4f, want 0", got)
	}
}

// TestLoudnessNormalizerNilIsSafeNoOp confirms a nil *LoudnessNormalizer
// (the state of any struct field that embeds one before construction, and
// of call sites that intentionally leave normalization unwired) behaves as
// a no-op rather than panicking.
func TestLoudnessNormalizerNilIsSafeNoOp(t *testing.T) {
	var norm *audio.LoudnessNormalizer
	if got := norm.Process([]int16{1, 2, 3}); got != nil {
		t.Fatalf("nil normalizer Process() = %v, want nil", got)
	}
	pcm := []byte{1, 2, 3, 4}
	if got := norm.ProcessBytes(pcm); string(got) != string(pcm) {
		t.Fatalf("nil normalizer ProcessBytes() = %v, want unchanged %v", got, pcm)
	}
	if got := norm.GainDB(); got != 0 {
		t.Fatalf("nil normalizer GainDB() = %v, want 0", got)
	}
}
