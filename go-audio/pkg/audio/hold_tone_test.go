package audio

import "testing"

func TestHoldTonePulseIsZeroAtBothEdgesAndBelowFullScale(t *testing.T) {
	cfg := DefaultHoldToneConfig()
	pulse := holdTonePulse(cfg, 24000)
	if len(pulse) < 2 {
		t.Fatalf("pulse length = %d, want at least 2 samples", len(pulse))
	}
	if pulse[0] != 0 {
		t.Fatalf("pulse[0] = %d, want 0 (raised-cosine window must start at zero)", pulse[0])
	}
	if pulse[len(pulse)-1] != 0 {
		t.Fatalf("pulse[last] = %d, want 0 (raised-cosine window must end at zero)", pulse[len(pulse)-1])
	}

	peak := 0
	nonZero := 0
	for _, sample := range pulse {
		if sample != 0 {
			nonZero++
		}
		abs := int(sample)
		if abs < 0 {
			abs = -abs
		}
		if abs > peak {
			peak = abs
		}
	}
	if nonZero == 0 {
		t.Fatal("pulse is entirely silent, want audible content")
	}
	if peak == 0 {
		t.Fatal("pulse peak amplitude = 0, want a sane audible level")
	}
	if int16(peak) > cfg.Amplitude {
		t.Fatalf("pulse peak = %d, want at most the configured amplitude %d", peak, cfg.Amplitude)
	}
	// The cue must be well below full scale and below ordinary speech
	// level, so it reads as a background signal and is never mistaken for
	// the assistant talking.
	const fullScale = 32767
	if peak > fullScale/2 {
		t.Fatalf("pulse peak = %d, want comfortably below full scale (%d)", peak, fullScale)
	}
}

func TestHoldTonePulseScalesWithSampleRate(t *testing.T) {
	cfg := DefaultHoldToneConfig()
	pulse16k := holdTonePulse(cfg, 16000)
	pulse24k := holdTonePulse(cfg, 24000)
	wantRatio := 24000.0 / 16000.0
	gotRatio := float64(len(pulse24k)) / float64(len(pulse16k))
	if gotRatio < wantRatio-0.05 || gotRatio > wantRatio+0.05 {
		t.Fatalf("pulse length ratio = %.3f, want ~%.3f (same PulseDuration at both rates)", gotRatio, wantRatio)
	}
}

func TestHoldToneConfigDefaultsFillZeroFields(t *testing.T) {
	cfg := HoldToneConfig{Amplitude: 500}.withDefaults()
	d := DefaultHoldToneConfig()
	if cfg.GapThreshold != d.GapThreshold {
		t.Fatalf("GapThreshold = %v, want default %v", cfg.GapThreshold, d.GapThreshold)
	}
	if cfg.PulseInterval != d.PulseInterval {
		t.Fatalf("PulseInterval = %v, want default %v", cfg.PulseInterval, d.PulseInterval)
	}
	if cfg.PulseDuration != d.PulseDuration {
		t.Fatalf("PulseDuration = %v, want default %v", cfg.PulseDuration, d.PulseDuration)
	}
	if cfg.Amplitude != 500 {
		t.Fatalf("Amplitude = %d, want the explicitly supplied 500", cfg.Amplitude)
	}
}
