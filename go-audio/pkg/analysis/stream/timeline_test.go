package stream_test

import (
	"math"
	"testing"
	"time"

	stream "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/stream"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/contract"
)

func TestPCM16TimelineConversionsUseExactMediaUnits(t *testing.T) {
	const rate = 24000
	if got := stream.PCM16SamplesToDuration(rate, rate); got != time.Second {
		t.Fatalf("samples to duration = %s, want 1s", got)
	}
	if got := stream.PCM16SamplesToDuration(0, rate); got != 0 {
		t.Fatalf("zero samples to duration = %s, want zero", got)
	}
	if got := stream.PCM16SamplesToDuration(12, 0); got != 0 {
		t.Fatalf("invalid-rate duration = %s, want zero", got)
	}

	if got, err := stream.PCM16DurationToSamples(time.Second, rate); err != nil || got != rate {
		t.Fatalf("duration to samples = %d, %v, want %d", got, err, rate)
	}
	if got, err := stream.PCM16SignedDurationToSamples(-20*time.Millisecond, rate); err != nil || got != -480 {
		t.Fatalf("signed duration to samples = %d, %v, want -480", got, err)
	}
	if got := stream.PCM16SamplesToSignedDuration(-480, rate); got != -20*time.Millisecond {
		t.Fatalf("signed samples to duration = %s, want -20ms", got)
	}

	for _, test := range []struct {
		name string
		call func() error
	}{
		{name: "negative duration", call: func() error {
			_, err := stream.PCM16DurationToSamples(-time.Nanosecond, rate)
			return err
		}},
		{name: "fractional sample", call: func() error {
			_, err := stream.PCM16DurationToSamples(time.Nanosecond, rate)
			return err
		}},
		{name: "invalid rate", call: func() error {
			_, err := stream.PCM16DurationToSamples(time.Second, 0)
			return err
		}},
		{name: "minimum signed duration", call: func() error {
			_, err := stream.PCM16SignedDurationToSamples(time.Duration(math.MinInt64), rate)
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.call() == nil {
				t.Fatal("conversion succeeded, want an error")
			}
		})
	}
}

func TestPCM16TimelineFrameAndCorrelationContracts(t *testing.T) {
	frame := stream.PCM16TimedFrame{Samples: []int16{1, 2, 3}, SampleRate: 1000, Start: 2 * time.Second}
	if got, want := frame.End(), 2*time.Second+3*time.Millisecond; got != want {
		t.Fatalf("frame end = %s, want %s", got, want)
	}
	if got := (stream.PCM16CorrelationMeasurement{ComparedSamples: 1}).HasEvidence(); !got {
		t.Fatal("one compared sample should count as evidence")
	}
	if got := (stream.PCM16CorrelationMeasurement{}).HasEvidence(); got {
		t.Fatal("empty correlation should not count as evidence")
	}

	correlation, compared := stream.PCM16NormalizedCorrelationAtLag(
		[]int16{100, 200, 300}, nil, []int16{100, 200, 300}, 0, 0,
	)
	if correlation < 0.999 || compared != 3 {
		t.Fatalf("identity correlation = %.3f/%d, want approximately 1/3", correlation, compared)
	}
	correlation, compared = stream.PCM16NormalizedCorrelationAtLag(
		[]int16{100, 100}, nil, []int16{200, 200}, 0, 0,
	)
	if correlation != 1 || compared != 2 {
		t.Fatalf("constant correlation = %.3f/%d, want 1/2", correlation, compared)
	}
	correlation, compared = stream.PCM16NormalizedCorrelationAtLag(
		[]int16{100, 200, 300}, []bool{false, true, true}, []int16{100, 200, 300}, 0, 150,
	)
	if correlation < 0.999 || compared != 2 {
		t.Fatalf("masked correlation = %.3f/%d, want approximately 1/2", correlation, compared)
	}
	if _, compared = stream.PCM16NormalizedCorrelationAtLag([]int16{0}, nil, []int16{0}, 0, 0); compared != 0 {
		t.Fatalf("silent correlation compared = %d, want zero", compared)
	}
}

func TestPCM16ActivityMaskHonorsTimeAnnotationsAndBounds(t *testing.T) {
	input := stream.PCM16Input{
		SampleRate: 1000,
		Samples:    make([]int16, 10),
		ExpectedSpeech: []stream.SpeechAnnotation{{
			Label: "word", Start: 2 * time.Millisecond, End: 6 * time.Millisecond,
		}},
	}
	mask, err := stream.PCM16ActivityMask(input, 1, 8)
	if err != nil {
		t.Fatalf("activity mask: %v", err)
	}
	want := []bool{false, true, true, true, true, false, false}
	if len(mask) != len(want) {
		t.Fatalf("mask length = %d, want %d", len(mask), len(want))
	}
	for index := range want {
		if mask[index] != want[index] {
			t.Fatalf("mask[%d] = %t, want %t", index, mask[index], want[index])
		}
	}
	if _, err := stream.PCM16ActivityMask(input, -1, 2); err == nil {
		t.Fatal("negative activity start succeeded")
	}
	if _, err := stream.PCM16ActivityMask(input, 2, 11); err == nil {
		t.Fatal("activity end beyond samples succeeded")
	}
	invalid := input
	invalid.ExpectedSpeech = []stream.SpeechAnnotation{{Start: time.Millisecond, End: 2 * time.Millisecond, StartSample: 1}}
	if _, err := stream.PCM16ActivityMask(invalid, 0, len(invalid.Samples)); err == nil {
		t.Fatal("mixed annotation units succeeded")
	}
	if _, err := stream.PCM16ActivityMask(stream.PCM16Input{SampleRate: 1000, Samples: []int16{1, 2}}, 0, 2); err != nil {
		t.Fatalf("unannotated activity mask: %v", err)
	}
}

func TestPCM16TimelineScalarHelpersHandleEdges(t *testing.T) {
	if got := stream.PCM16AmplitudeForDBFS(math.Inf(-1)); got != 0 {
		t.Fatalf("negative infinity amplitude = %f, want zero", got)
	}
	if got := stream.PCM16AmplitudeForDBFS(-6); got <= 0 {
		t.Fatalf("finite amplitude = %f, want positive", got)
	}
	if got := stream.PCM16AbsoluteSample(-32768); got != 32768 {
		t.Fatalf("absolute minimum sample = %d, want 32768", got)
	}
	if !stream.PCM16IsFinite(1.0) || stream.PCM16IsFinite(math.NaN()) || stream.PCM16IsFinite(math.Inf(1)) {
		t.Fatal("finite helper classified scalar values incorrectly")
	}
	if _, err := stream.NormalizePCM16AnalysisConfig(stream.PCM16AnalysisConfig{FrameDuration: -time.Millisecond}); err == nil {
		t.Fatal("invalid config normalized successfully")
	}
	if _, err := stream.NormalizePCM16AnalysisConfig(stream.PCM16AnalysisConfig{}); err != nil {
		t.Fatalf("zero config normalization: %v", err)
	}
	if contract.ErrClosed == nil {
		t.Fatal("neutral lifecycle contract must provide a closed error")
	}
}
