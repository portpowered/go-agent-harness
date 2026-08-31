package audio

import (
	"context"
	"math"
	"testing"
)

func TestPCM16NormalizerPeakConstrainedControlReportsTargetShortfall(t *testing.T) {
	normalizer := NewPCM16Normalizer()
	source := normalizerSine(900, normalizer.FrameSamples())
	source[len(source)/2] = math.MaxInt16

	output, err := normalizer.Process(context.Background(), source)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if len(output) != len(source) {
		t.Fatalf("output samples = %d, want %d", len(output), len(source))
	}

	stats := normalizerSignalStats(output)
	if stats.rmsDBFS >= PCM16NormalizerTargetRMSDBFS-1.5 {
		t.Fatalf("peak-constrained RMS = %.3f dBFS, want observable shortfall below %.3f dBFS", stats.rmsDBFS, PCM16NormalizerTargetRMSDBFS-1.5)
	}
	ceiling := int(math.Floor(dbfsToLinear(PCM16NormalizerPeakCeilingDBFS, 1<<15)))
	if stats.peak > ceiling {
		t.Fatalf("peak = %d, want no higher than -1 dBFS sample ceiling %d", stats.peak, ceiling)
	}
	if stats.clipCount != 0 {
		t.Fatalf("clipped samples = %d, want zero", stats.clipCount)
	}
	if stats.meanAbs > 0.001*float64(1<<15) {
		t.Fatalf("absolute DC offset = %.3f counts, want <= %.3f", stats.meanAbs, 0.001*float64(1<<15))
	}
}

func TestPCM16NormalizerSteadyAndTransitionControls(t *testing.T) {
	normalizer := NewPCM16Normalizer()
	frameSamples := normalizer.FrameSamples()
	const steadyFrames = 6
	const transitionFrames = 8

	steadyRMS := make([]float64, 0, steadyFrames)
	previousGain := normalizer.GainDB()
	transitionGains := make([]float64, 0, transitionFrames)
	for frameIndex := 0; frameIndex < steadyFrames+transitionFrames; frameIndex++ {
		amplitude := 500.0
		if frameIndex >= steadyFrames {
			amplitude = 1200
		}
		output, err := normalizer.Process(context.Background(), normalizerSine(amplitude, frameSamples))
		if err != nil {
			t.Fatalf("frame %d Process() error = %v", frameIndex, err)
		}
		if len(output) != frameSamples {
			t.Fatalf("frame %d output samples = %d, want %d", frameIndex, len(output), frameSamples)
		}

		gain := normalizer.GainDB()
		if frameIndex > 0 {
			if delta := math.Abs(gain - previousGain); delta > PCM16NormalizerMaxGainChangeDBPer100MS*0.2+0.002 {
				t.Fatalf("frame %d gain delta = %.4f dB, exceeds the 20 ms slew bound", frameIndex, delta)
			}
		}
		previousGain = gain
		if frameIndex < steadyFrames {
			steadyRMS = append(steadyRMS, normalizerRMSDBFS(output))
		} else {
			transitionGains = append(transitionGains, gain)
		}
	}

	if spread := maxFloat(steadyRMS) - minFloat(steadyRMS); spread > 0.05 {
		t.Fatalf("steady-level RMS spread = %.4f dB, want no short-window pumping", spread)
	}
	for index := 1; index < len(transitionGains); index++ {
		if transitionGains[index] > transitionGains[index-1]+0.000001 {
			t.Fatalf("transition gain increased at frame %d: previous %.4f dB, current %.4f dB", index, transitionGains[index-1], transitionGains[index])
		}
	}

	// A gain change at a semantic level transition is allowed to move the
	// envelope, but the emitted PCM must remain free of a quiet-boundary click.
	normalizer = NewPCM16Normalizer()
	var output []int16
	for frameIndex := 0; frameIndex < steadyFrames+transitionFrames; frameIndex++ {
		amplitude := 500.0
		if frameIndex >= steadyFrames {
			amplitude = 1200
		}
		frame, err := normalizer.Process(context.Background(), normalizerSine(amplitude, frameSamples))
		if err != nil {
			t.Fatalf("boundary frame %d Process() error = %v", frameIndex, err)
		}
		output = append(output, frame...)
	}
	for boundary := frameSamples; boundary < len(output); boundary += frameSamples {
		if delta := normalizerAbsDifference(output[boundary-1], output[boundary]); delta > 6000 {
			t.Fatalf("normalized boundary at sample %d delta = %d, want <= 6000", boundary, delta)
		}
	}
}

type normalizerSignalStatistics struct {
	rmsDBFS   float64
	peak      int
	clipCount int
	meanAbs   float64
}

func normalizerSignalStats(samples []int16) normalizerSignalStatistics {
	if len(samples) == 0 {
		return normalizerSignalStatistics{rmsDBFS: math.Inf(-1)}
	}
	var energy float64
	var sum int64
	peak := 0
	clipCount := 0
	for _, sample := range samples {
		value := float64(sample)
		energy += value * value
		sum += int64(sample)
		absValue := int(sample)
		if absValue < 0 {
			absValue = -absValue
		}
		if absValue > peak {
			peak = absValue
		}
		if absValue >= PCM16NormalizerClipSampleThreshold {
			clipCount++
		}
	}
	return normalizerSignalStatistics{
		rmsDBFS:   20 * math.Log10(math.Sqrt(energy/float64(len(samples)))/float64(1<<15)),
		peak:      peak,
		clipCount: clipCount,
		meanAbs:   math.Abs(float64(sum) / float64(len(samples))),
	}
}

func maxFloat(values []float64) float64 {
	maximum := math.Inf(-1)
	for _, value := range values {
		if value > maximum {
			maximum = value
		}
	}
	return maximum
}

func minFloat(values []float64) float64 {
	minimum := math.Inf(1)
	for _, value := range values {
		if value < minimum {
			minimum = value
		}
	}
	return minimum
}

func normalizerAbsDifference(first, second int16) int {
	difference := int(first) - int(second)
	if difference < 0 {
		return -difference
	}
	return difference
}
