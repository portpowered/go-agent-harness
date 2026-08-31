package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

func TestPCM16NormalizerLevelingProfiles(t *testing.T) {
	cases := []struct {
		name      string
		amplitude float64
	}{
		{name: "already on target", amplitude: 464},
		{name: "quiet speech", amplitude: 220},
		{name: "loud speech", amplitude: 5000},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			source := normalizerSine(testCase.amplitude, 320*20)
			got := collectNormalizedSamples(t, source, []int{1, 319, 7, 313, 640, 11})
			if len(got) != len(source) {
				t.Fatalf("output sample count = %d, want %d", len(got), len(source))
			}
			if gotDBFS := normalizerRMSDBFS(got); gotDBFS < -21.5 || gotDBFS > -18.5 {
				t.Fatalf("normalized RMS = %.3f dBFS, want -21.5..-18.5 dBFS", gotDBFS)
			}
		})
	}
}

func TestPCM16NormalizerSilenceAndSilenceFloorDoNotAcquireGain(t *testing.T) {
	normalizer := NewPCM16Normalizer()
	silence := make([]int16, 320)
	got, err := normalizer.Process(context.Background(), silence)
	if err != nil {
		t.Fatalf("silence Process() error = %v", err)
	}
	if !reflect.DeepEqual(got, silence) {
		t.Fatalf("silence output = %v, want exact zero frame", got)
	}
	if got := normalizer.GainDB(); got != 0 {
		t.Fatalf("gain after silence = %.3f dB, want 0 dB", got)
	}

	belowFloor := normalizerSine(100, 320)
	got, err = normalizer.Process(context.Background(), belowFloor)
	if err != nil {
		t.Fatalf("below-floor Process() error = %v", err)
	}
	if gotDBFS := normalizerRMSDBFS(got); gotDBFS >= -40 {
		t.Fatalf("below-floor output RMS = %.3f dBFS, want quiet material preserved", gotDBFS)
	}
	if got := normalizer.GainDB(); got != 0 {
		t.Fatalf("gain after below-floor frame = %.3f dB, want 0 dB", got)
	}

	active := normalizerSine(220, 320)
	got, err = normalizer.Process(context.Background(), active)
	if err != nil {
		t.Fatalf("active Process() error = %v", err)
	}
	if gotDBFS := normalizerRMSDBFS(got); gotDBFS < -21.5 || gotDBFS > -18.5 {
		t.Fatalf("active output RMS = %.3f dBFS, want -21.5..-18.5 dBFS", gotDBFS)
	}
	if normalizer.GainDB() <= 0 {
		t.Fatalf("gain after quiet speech = %.3f dB, want positive acquisition", normalizer.GainDB())
	}

	finished, err := normalizer.Finish(context.Background())
	if err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	if len(finished) != 0 {
		t.Fatalf("Finish() tail = %d samples, want no tail", len(finished))
	}
}

func TestPCM16NormalizerPreservesSampleCountAndFormatBytes(t *testing.T) {
	source := normalizerSine(900, 320*2+17)
	encoded := normalizerPCM16Bytes(source)
	normalizer := NewPCM16Normalizer()
	var got []byte
	for _, chunk := range [][]byte{
		encoded[:2*5],
		encoded[2*5 : 2*327],
		encoded[2*327 : 2*640],
		encoded[2*640:],
	} {
		part, err := normalizer.ProcessPCM16(context.Background(), chunk)
		if err != nil {
			t.Fatalf("ProcessPCM16() error = %v", err)
		}
		got = append(got, part...)
	}
	tail, err := normalizer.FinishPCM16(context.Background())
	if err != nil {
		t.Fatalf("FinishPCM16() error = %v", err)
	}
	got = append(got, tail...)
	if len(got) != len(encoded) {
		t.Fatalf("encoded output length = %d, want %d", len(got), len(encoded))
	}
	if len(got)%2 != 0 {
		t.Fatalf("encoded output length = %d, want even PCM16 byte count", len(got))
	}
}

func TestPCM16NormalizerEmitsWithinOneTwentyMillisecondFrame(t *testing.T) {
	normalizer := NewPCM16Normalizer()
	if got, want := normalizer.FrameSamples(), int(float64(SampleRate)*PCM16NormalizerFrameDuration.Seconds()); got != want {
		t.Fatalf("normalizer frame size = %d samples, want %d", got, want)
	}
	first, err := normalizer.Process(context.Background(), normalizerSine(500, normalizer.FrameSamples()-1))
	if err != nil {
		t.Fatalf("partial Process() error = %v", err)
	}
	if len(first) != 0 {
		t.Fatalf("partial Process() emitted %d samples, want bounded tail to remain buffered", len(first))
	}
	second, err := normalizer.Process(context.Background(), []int16{100})
	if err != nil {
		t.Fatalf("frame-completing Process() error = %v", err)
	}
	if len(second) != normalizer.FrameSamples() {
		t.Fatalf("frame-completing Process() emitted %d samples, want %d", len(second), normalizer.FrameSamples())
	}
}

func TestPCM16NormalizerPeakSafetyHandlesLateTransientAndDC(t *testing.T) {
	const frameCount = 12
	source := make([]int16, 0, 320*frameCount)
	for frameIndex := 0; frameIndex < frameCount; frameIndex++ {
		frame := normalizerSine(220, 320)
		for index := range frame {
			frame[index] += 750
		}
		if frameIndex == 7 {
			frame[137] = 32767
		}
		source = append(source, frame...)
	}

	got := collectNormalizedSamples(t, source, []int{320 * 2, 320 * 3, 320 * 7, 1})
	if len(got) != len(source) {
		t.Fatalf("output sample count = %d, want %d", len(got), len(source))
	}
	maxAbs := 0
	var sum int64
	for index, sample := range got {
		abs := int(sample)
		if abs < 0 {
			abs = -abs
		}
		if abs >= PCM16NormalizerClipSampleThreshold {
			t.Fatalf("output sample %d = %d, violates |sample| < %d", index, sample, PCM16NormalizerClipSampleThreshold)
		}
		if abs > maxAbs {
			maxAbs = abs
		}
		sum += int64(sample)
	}
	if float64(maxAbs) > dbfsToLinear(PCM16NormalizerPeakCeilingDBFS, 1<<15) {
		t.Fatalf("output peak = %d, exceeds -1 dBFS ceiling", maxAbs)
	}
	if mean := math.Abs(float64(sum) / float64(len(got))); mean > 100 {
		t.Fatalf("output DC mean = %.3f counts, want <=100", mean)
	}
	if maxAbs == 0 {
		t.Fatal("transient output is silent")
	}
}

func TestPCM16NormalizerIsChunkBoundaryInvariant(t *testing.T) {
	source := make([]int16, 0, 320*18+19)
	for frameIndex := 0; frameIndex < 18; frameIndex++ {
		amplitude := 220.0
		if frameIndex >= 6 && frameIndex < 12 {
			amplitude = 500
		}
		if frameIndex >= 12 {
			amplitude = 1000
		}
		source = append(source, normalizerSine(amplitude, 320)...)
	}
	source = append(source, normalizerSine(350, 19)...)

	one := collectNormalizedSamples(t, source, []int{len(source)})
	two := collectNormalizedSamples(t, source, []int{1, 2, 17, 319, 7, 640, 13, 997, 5, 401, 1234})
	if !reflect.DeepEqual(one, two) {
		t.Fatalf("chunked output differs from one-chunk output at sample boundary")
	}
}

func TestPCM16NormalizerGainEnvelopeRateAndImmediateSafety(t *testing.T) {
	normalizer := NewPCM16Normalizer()
	first, err := normalizer.Process(context.Background(), normalizerSine(220, 320))
	if err != nil {
		t.Fatalf("first Process() error = %v", err)
	}
	firstGain := normalizer.GainDB()
	if len(first) != 320 {
		t.Fatalf("first output length = %d, want 320", len(first))
	}

	for index, amplitude := range []float64{440, 440, 440, 220, 220, 220} {
		if _, err := normalizer.Process(context.Background(), normalizerSine(amplitude, 320)); err != nil {
			t.Fatalf("level-change frame %d Process() error = %v", index, err)
		}
		gain := normalizer.GainDB()
		if delta := math.Abs(gain - firstGain); delta > float64(index+1)*0.2+0.002 {
			t.Fatalf("gain after frame %d = %.4f dB, first %.4f dB, delta %.4f dB exceeds 1 dB/100ms envelope", index, gain, firstGain, delta)
		}
	}

	normalizer.Reset()
	quiet := normalizerSine(220, 320*6)
	if _, err := normalizer.Process(context.Background(), quiet[:320]); err != nil {
		t.Fatalf("quiet acquisition Process() error = %v", err)
	}
	transient := append([]int16(nil), quiet[320:640]...)
	transient[91] = 32767
	output, err := normalizer.Process(context.Background(), transient)
	if err != nil {
		t.Fatalf("transient Process() error = %v", err)
	}
	for index, sample := range output {
		abs := int(sample)
		if abs < 0 {
			abs = -abs
		}
		if abs >= PCM16NormalizerClipSampleThreshold {
			t.Fatalf("transient output sample %d = %d, violates clipping guard", index, sample)
		}
	}
	if normalizer.GainDB() >= firstGain {
		t.Fatalf("safety attenuation gain = %.4f dB, want immediate attenuation below acquired gain %.4f dB", normalizer.GainDB(), firstGain)
	}
}

func TestPCM16NormalizerLifecycleMalformedPCMAndCancellation(t *testing.T) {
	normalizer := NewPCM16Normalizer()
	if got, err := normalizer.Process(context.Background(), nil); err != nil || len(got) != 0 {
		t.Fatalf("empty Process() = %v, %v, want empty success", got, err)
	}
	if got, err := normalizer.ProcessPCM16(context.Background(), []byte{1}); got != nil || !errors.Is(err, ErrPCM16NormalizerInvalidPCM) {
		t.Fatalf("odd ProcessPCM16() = %v, %v, want invalid PCM and no output", got, err)
	}
	if _, err := normalizer.Process(context.Background(), make([]int16, 320)); !errors.Is(err, ErrPCM16NormalizerLifecycle) {
		t.Fatalf("process after malformed PCM = %v, want lifecycle error", err)
	}
	if _, err := normalizer.Finish(context.Background()); !errors.Is(err, ErrPCM16NormalizerLifecycle) {
		t.Fatalf("finish after malformed PCM = %v, want lifecycle error", err)
	}

	if err := normalizer.Reset(); err != nil {
		t.Fatalf("Reset() after malformed PCM: %v", err)
	}
	if _, err := normalizer.Process(context.Background(), make([]int16, 7)); err != nil {
		t.Fatalf("partial Process() = %v", err)
	}
	if err := normalizer.Cancel(); err != nil {
		t.Fatalf("Cancel() = %v", err)
	}
	if _, err := normalizer.Finish(context.Background()); !errors.Is(err, ErrPCM16NormalizerLifecycle) {
		t.Fatalf("finish after cancel = %v, want lifecycle error", err)
	}
	if err := normalizer.Reset(); err != nil {
		t.Fatalf("Reset() after cancel: %v", err)
	}

	source := normalizerSine(220, 320+7)
	got := collectNormalizedSamples(t, source, []int{3, 317, 7})
	want := collectNormalizedSamples(t, source, []int{len(source)})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reset output differs from fresh normalizer")
	}

	if _, err := normalizer.Finish(context.Background()); err != nil {
		t.Fatalf("Finish() after reset = %v", err)
	}
	if _, err := normalizer.Finish(context.Background()); !errors.Is(err, ErrPCM16NormalizerLifecycle) {
		t.Fatalf("second Finish() = %v, want lifecycle error", err)
	}
}

func TestPCM16NormalizerContextAndConfigErrors(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	normalizer := NewPCM16Normalizer()
	if _, err := normalizer.Process(canceled, make([]int16, 320)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Process() = %v, want context.Canceled", err)
	}
	if _, err := normalizer.Finish(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Finish() = %v, want context.Canceled", err)
	}

	cases := []struct {
		name   string
		mutate func(*PCM16NormalizerConfig)
	}{
		{name: "negative sample rate", mutate: func(config *PCM16NormalizerConfig) { config.SampleRate = -1 }},
		{name: "too much frame delay", mutate: func(config *PCM16NormalizerConfig) { config.FrameDuration = 21 * time.Millisecond }},
		{name: "target above zero", mutate: func(config *PCM16NormalizerConfig) { config.TargetRMSDBFS = 1 }},
		{name: "silence floor above target", mutate: func(config *PCM16NormalizerConfig) { config.SilenceFloorDBFS = -10 }},
		{name: "peak below target", mutate: func(config *PCM16NormalizerConfig) { config.PeakCeilingDBFS = -30 }},
		{name: "clip threshold above repository guard", mutate: func(config *PCM16NormalizerConfig) { config.ClipSampleThreshold = 32701 }},
		{name: "negative gain rate", mutate: func(config *PCM16NormalizerConfig) { config.MaxGainChangeDBPer100MS = -1 }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			config := DefaultPCM16NormalizerConfig
			testCase.mutate(&config)
			if _, err := NewPCM16NormalizerWithConfig(config); !errors.Is(err, ErrPCM16NormalizerConfig) {
				t.Fatalf("NewPCM16NormalizerWithConfig() = %v, want configuration error", err)
			}
		})
	}
}

func collectNormalizedSamples(t *testing.T, source []int16, chunkSizes []int) []int16 {
	t.Helper()
	normalizer := NewPCM16Normalizer()
	var output []int16
	position := 0
	chunkIndex := 0
	for position < len(source) {
		size := len(source) - position
		if len(chunkSizes) > 0 {
			size = chunkSizes[chunkIndex%len(chunkSizes)]
			if size <= 0 {
				t.Fatalf("invalid test chunk size %d", size)
			}
			if size > len(source)-position {
				size = len(source) - position
			}
		}
		part, err := normalizer.Process(context.Background(), source[position:position+size])
		if err != nil {
			t.Fatalf("Process() at sample %d: %v", position, err)
		}
		output = append(output, part...)
		position += size
		chunkIndex++
	}
	tail, err := normalizer.Finish(context.Background())
	if err != nil {
		t.Fatalf("Finish(): %v", err)
	}
	return append(output, tail...)
}

func normalizerSine(amplitude float64, sampleCount int) []int16 {
	samples := make([]int16, sampleCount)
	for index := range samples {
		value := amplitude * math.Sin(2*math.Pi*200*float64(index)/float64(SampleRate))
		if value > 32767 {
			value = 32767
		} else if value < -32768 {
			value = -32768
		}
		samples[index] = int16(math.Round(value))
	}
	return samples
}

func normalizerPCM16Bytes(samples []int16) []byte {
	encoded := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(encoded[index*2:], uint16(sample))
	}
	return encoded
}

func normalizerRMSDBFS(samples []int16) float64 {
	if len(samples) == 0 {
		return math.Inf(-1)
	}
	var energy float64
	for _, sample := range samples {
		value := float64(sample)
		energy += value * value
	}
	return 20 * math.Log10(math.Sqrt(energy/float64(len(samples)))/float64(1<<15))
}

func TestPCM16NormalizerByteAndSampleAPIsAgree(t *testing.T) {
	source := normalizerSine(500, 320+3)
	bytesNormalizer := NewPCM16Normalizer()
	gotBytes, err := bytesNormalizer.ProcessPCM16(context.Background(), normalizerPCM16Bytes(source))
	if err != nil {
		t.Fatalf("ProcessPCM16() = %v", err)
	}
	tailBytes, err := bytesNormalizer.FinishPCM16(context.Background())
	if err != nil {
		t.Fatalf("FinishPCM16() = %v", err)
	}
	gotBytes = append(gotBytes, tailBytes...)

	sampleNormalizer := NewPCM16Normalizer()
	gotSamples, err := sampleNormalizer.Process(context.Background(), source)
	if err != nil {
		t.Fatalf("Process() = %v", err)
	}
	tailSamples, err := sampleNormalizer.Finish(context.Background())
	if err != nil {
		t.Fatalf("Finish() = %v", err)
	}
	gotSamples = append(gotSamples, tailSamples...)
	if !bytes.Equal(gotBytes, normalizerPCM16Bytes(gotSamples)) {
		t.Fatalf("byte and sample APIs produced different PCM16")
	}
}
