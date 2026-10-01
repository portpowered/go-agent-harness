package audio

import "math"

const (
	// defaultVADEnergyThreshold is the RMS level (in int16 units) above which
	// a frame is considered speech. A value of 300 comfortably separates
	// typical mic noise from voiced speech.
	defaultVADEnergyThreshold = 300.0
	// defaultVADMinSpeechFrames is the number of consecutive high-energy
	// frames required before an utterance is considered to have started
	// (3 × 30 ms = 90 ms).
	defaultVADMinSpeechFrames = 3
	// defaultVADMaxSilenceFrames is the number of consecutive low-energy
	// frames after speech that triggers end-of-utterance (30 × 30 ms = 900 ms).
	defaultVADMaxSilenceFrames = 30
)

// DefaultVADConfig returns sensible defaults for real-time speech detection.
func DefaultVADConfig() VADConfig {
	return VADConfig{
		EnergyThreshold:  defaultVADEnergyThreshold,
		MinSpeechFrames:  defaultVADMinSpeechFrames,
		MaxSilenceFrames: defaultVADMaxSilenceFrames,
	}
}

// VADConfig holds the thresholds used by VoiceActivityDetector.
type VADConfig struct {
	EnergyThreshold  float64
	MinSpeechFrames  int
	MaxSilenceFrames int
}

// VoiceActivityDetector uses a simple energy-based heuristic to identify
// speech frames within a stream of PCM audio.
//
// State machine:
//
//	SILENCE  ─(MinSpeechFrames consecutive speech)→  SPEECH
//	SPEECH   ─(MaxSilenceFrames consecutive silence)→ SILENCE  + complete=true
type VoiceActivityDetector struct {
	cfg          VADConfig
	inSpeech     bool
	speechCount  int
	silenceCount int
}

// NewVAD creates a VoiceActivityDetector with the given config.
func NewVAD(cfg VADConfig) *VoiceActivityDetector {
	return &VoiceActivityDetector{cfg: cfg}
}

// Process evaluates one audio frame and returns:
//
//	include  – whether this frame should be appended to the utterance buffer.
//	complete – whether the utterance is complete and ready to dispatch.
//
// Callers should call Reset before reusing the detector for a new utterance.
func (v *VoiceActivityDetector) Process(frame []int16) (include bool, complete bool) {
	energy := PCM16RMSEnergy(frame)
	isSpeech := energy >= v.cfg.EnergyThreshold

	if isSpeech {
		v.speechCount++
		v.silenceCount = 0
	} else {
		v.silenceCount++
	}

	if !v.inSpeech {
		// Wait for MinSpeechFrames consecutive speech frames before committing.
		if v.speechCount >= v.cfg.MinSpeechFrames {
			v.inSpeech = true
		}
		return v.inSpeech, false
	}

	// We are inside an utterance.
	if !isSpeech && v.silenceCount >= v.cfg.MaxSilenceFrames {
		// Trailing silence exceeded the window – utterance is done.
		v.inSpeech = false
		v.speechCount = 0
		v.silenceCount = 0
		return false, true
	}

	return true, false
}

// Reset clears all internal state so the detector is ready for a new utterance.
func (v *VoiceActivityDetector) Reset() {
	v.inSpeech = false
	v.speechCount = 0
	v.silenceCount = 0
}

// PCM16RMSEnergy returns the root-mean-square energy of signed PCM16 samples
// in the original integer amplitude units. Empty input has zero energy.
func PCM16RMSEnergy(samples []int16) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		f := float64(s)
		sum += f * f
	}
	return math.Sqrt(sum / float64(len(samples)))
}
