package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const schema = "audio-runtime-c30-frame-dimension-consumer.v1"

type report struct {
	Schema          string                 `json:"schema"`
	Mode            string                 `json:"mode"`
	Source          string                 `json:"source"`
	GOOS            string                 `json:"goos"`
	GOARCH          string                 `json:"goarch"`
	IntBits         int                    `json:"int_bits"`
	StartedAt       string                 `json:"started_at"`
	DurationMS      int64                  `json:"duration_ms"`
	Cases           []frameCaseReport      `json:"cases,omitempty"`
	NegativeControl *negativeControlReport `json:"negative_control,omitempty"`
	CleanShutdown   bool                   `json:"clean_shutdown"`
	Passed          bool                   `json:"passed"`
	Error           string                 `json:"error,omitempty"`
}

type frameCaseReport struct {
	Name            string `json:"name"`
	API             string `json:"api"`
	SampleRate      int    `json:"sample_rate"`
	Channels        int    `json:"channels"`
	Duration        string `json:"duration"`
	ExpectedSamples int    `json:"expected_samples,omitempty"`
	ActualSamples   int    `json:"actual_samples"`
	ExpectedBytes   int    `json:"expected_bytes,omitempty"`
	ActualBytes     int    `json:"actual_bytes"`
	ExpectedError   string `json:"expected_error,omitempty"`
	ActualError     string `json:"actual_error,omitempty"`
	ErrorIsExpected bool   `json:"error_is_expected"`
	Passed          bool   `json:"passed"`
}

type negativeControlReport struct {
	Name            string `json:"name"`
	ActualSamples   int    `json:"actual_samples"`
	MutatedExpected int    `json:"mutated_expected"`
	ActualError     string `json:"actual_error,omitempty"`
	Passed          bool   `json:"passed"`
}

type knownFrame struct {
	name     string
	rate     int
	channels int
	duration time.Duration
	samples  int
	bytes    int
}

var compiledSourceRevision = "working-tree"

func main() {
	mode := flag.String("mode", "positive", "positive or negative-control")
	flag.Parse()

	started := time.Now()
	result := report{
		Schema:        schema,
		Mode:          *mode,
		Source:        sourceRevision(),
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		IntBits:       strconv.IntSize,
		StartedAt:     started.UTC().Format(time.RFC3339Nano),
		CleanShutdown: true,
	}

	var runErr error
	switch *mode {
	case "positive":
		runErr = runPositive(&result)
	case "negative-control":
		runErr = runNegativeControl(&result)
	default:
		runErr = fmt.Errorf("unsupported mode %q", *mode)
	}
	result.DurationMS = time.Since(started).Milliseconds()
	result.Passed = runErr == nil
	if *mode == "negative-control" && result.NegativeControl != nil && result.NegativeControl.Passed {
		result.Passed = true
	}
	if runErr != nil {
		result.Error = runErr.Error()
	}

	encoded, marshalErr := json.MarshalIndent(result, "", "  ")
	if marshalErr != nil {
		fmt.Fprintln(os.Stderr, marshalErr)
		os.Exit(1)
	}
	if _, writeErr := os.Stdout.Write(append(encoded, '\n')); writeErr != nil {
		fmt.Fprintln(os.Stderr, writeErr)
		os.Exit(1)
	}
	if runErr != nil {
		fmt.Fprintln(os.Stderr, runErr)
		os.Exit(1)
	}
}

func sourceRevision() string {
	return compiledSourceRevision
}

func runPositive(result *report) error {
	known := []knownFrame{
		{name: "8000-mono", rate: 8000, channels: 1, duration: 20 * time.Millisecond, samples: 160, bytes: 320},
		{name: "8000-stereo", rate: 8000, channels: 2, duration: 20 * time.Millisecond, samples: 320, bytes: 640},
		{name: "16000-mono", rate: 16000, channels: 1, duration: 20 * time.Millisecond, samples: 320, bytes: 640},
		{name: "16000-stereo", rate: 16000, channels: 2, duration: 20 * time.Millisecond, samples: 640, bytes: 1280},
		{name: "24000-mono", rate: 24000, channels: 1, duration: 20 * time.Millisecond, samples: 480, bytes: 960},
		{name: "24000-stereo", rate: 24000, channels: 2, duration: 20 * time.Millisecond, samples: 960, bytes: 1920},
		{name: "44100-mono", rate: 44100, channels: 1, duration: 20 * time.Millisecond, samples: 882, bytes: 1764},
		{name: "44100-stereo", rate: 44100, channels: 2, duration: 20 * time.Millisecond, samples: 1764, bytes: 3528},
		{name: "48000-mono", rate: 48000, channels: 1, duration: 20 * time.Millisecond, samples: 960, bytes: 1920},
		{name: "48000-stereo", rate: 48000, channels: 2, duration: 20 * time.Millisecond, samples: 1920, bytes: 3840},
		{name: "1hz-one-second", rate: 1, channels: 1, duration: time.Second, samples: 1, bytes: 2},
	}
	for _, test := range known {
		result.Cases = append(result.Cases, checkKnownFrame(test))
	}
	for _, test := range []knownFrame{
		{name: "zero-rate", rate: 0, channels: 1, duration: time.Second},
		{name: "negative-rate", rate: -1, channels: 1, duration: time.Second},
		{name: "zero-channels", rate: 24000, channels: 0, duration: 20 * time.Millisecond},
		{name: "negative-channels", rate: 24000, channels: -1, duration: 20 * time.Millisecond},
		{name: "zero-duration", rate: 24000, channels: 1},
		{name: "negative-duration", rate: 24000, channels: 1, duration: -time.Nanosecond},
		{name: "fractional-44100-millisecond", rate: 44100, channels: 1, duration: time.Millisecond},
	} {
		result.Cases = append(result.Cases, checkInvalidFrame(test))
	}

	if strconv.IntSize >= 64 {
		rate := int((uint64(1) << 61) + 24000)
		actualSamples, sampleErr := audio.PCM16FrameSamples(rate, 1, time.Second)
		actualBytes, byteErr := audio.PCM16FrameBytes(rate, 1, time.Second)
		result.Cases = append(result.Cases, frameCaseReport{
			Name: "reduced-rate-duration-product", API: "PCM16FrameSamples/PCM16FrameBytes",
			SampleRate: rate, Channels: 1, Duration: time.Second.String(),
			ExpectedSamples: rate, ActualSamples: actualSamples,
			ExpectedBytes: rate * 2, ActualBytes: actualBytes,
			ActualError: joinErrors(sampleErr, byteErr),
			Passed:      sampleErr == nil && byteErr == nil && actualSamples == rate && actualBytes == rate*2 && actualSamples != 24000,
		})

		channels := int((uint64(1) << 62) + 1)
		actualSamples, sampleErr = audio.PCM16FrameSamples(4, channels, time.Second)
		actualBytes, byteErr = audio.PCM16FrameBytes(4, channels, time.Second)
		result.Cases = append(result.Cases, frameCaseReport{
			Name: "channel-product-overflow", API: "PCM16FrameSamples/PCM16FrameBytes",
			SampleRate: 4, Channels: channels, Duration: time.Second.String(),
			ActualSamples: actualSamples, ActualBytes: actualBytes,
			ActualError:     joinErrors(sampleErr, byteErr),
			ErrorIsExpected: errors.Is(sampleErr, audio.ErrInvalidPCM16FrameSize) && errors.Is(byteErr, audio.ErrInvalidPCM16FrameSize),
			Passed:          actualSamples == 0 && actualBytes == 0 && errors.Is(sampleErr, audio.ErrInvalidPCM16FrameSize) && errors.Is(byteErr, audio.ErrInvalidPCM16FrameSize),
		})
	}

	maximumInt := int(^uint(0) >> 1)
	byteBoundary, boundaryErr := audio.PCM16ByteCapacity(maximumInt/2, 2)
	overflowCapacity, overflowErr := audio.PCM16ByteCapacity(maximumInt/2+1, 2)
	result.Cases = append(result.Cases, frameCaseReport{
		Name: "byte-capacity-boundaries", API: "PCM16ByteCapacity",
		ExpectedSamples: maximumInt / 2, ActualSamples: byteBoundary,
		ExpectedBytes: (maximumInt / 2) * 2, ActualBytes: overflowCapacity,
		ActualError:     joinErrors(boundaryErr, overflowErr),
		ErrorIsExpected: errors.Is(overflowErr, audio.ErrInvalidPCM16FrameSize),
		Passed:          boundaryErr == nil && byteBoundary == (maximumInt/2)*2 && overflowCapacity == 0 && errors.Is(overflowErr, audio.ErrInvalidPCM16FrameSize),
	})

	for _, test := range result.Cases {
		if !test.Passed {
			return fmt.Errorf("consumer case %s failed", test.Name)
		}
	}
	return nil
}

func runNegativeControl(result *report) error {
	actual, err := audio.PCM16FrameSamples(24000, 1, 20*time.Millisecond)
	const mutatedExpected = 481
	result.NegativeControl = &negativeControlReport{
		Name: "mutated-24000-mono-20ms", ActualSamples: actual,
		MutatedExpected: mutatedExpected, ActualError: errorText(err),
		Passed: err == nil && actual == 480 && actual != mutatedExpected,
	}
	if !result.NegativeControl.Passed {
		return fmt.Errorf("negative-control accepted mutated expected: actual_samples=%d mutated_expected=%d error=%v", actual, mutatedExpected, err)
	}
	return fmt.Errorf("negative-control causal mismatch: actual_samples=%d mutated_expected=%d; child rejects mutated answer", actual, mutatedExpected)
}

func checkKnownFrame(test knownFrame) frameCaseReport {
	samples, sampleErr := audio.PCM16FrameSamples(test.rate, test.channels, test.duration)
	bytes, byteErr := audio.PCM16FrameBytes(test.rate, test.channels, test.duration)
	return frameCaseReport{
		Name: test.name, API: "PCM16FrameSamples/PCM16FrameBytes", SampleRate: test.rate, Channels: test.channels,
		Duration: test.duration.String(), ExpectedSamples: test.samples, ActualSamples: samples,
		ExpectedBytes: test.bytes, ActualBytes: bytes, ActualError: joinErrors(sampleErr, byteErr),
		Passed: sampleErr == nil && byteErr == nil && samples == test.samples && bytes == test.bytes,
	}
}

func checkInvalidFrame(test knownFrame) frameCaseReport {
	samples, sampleErr := audio.PCM16FrameSamples(test.rate, test.channels, test.duration)
	bytes, byteErr := audio.PCM16FrameBytes(test.rate, test.channels, test.duration)
	return frameCaseReport{
		Name: test.name, API: "PCM16FrameSamples/PCM16FrameBytes", SampleRate: test.rate, Channels: test.channels,
		Duration: test.duration.String(), ExpectedError: "ErrInvalidPCM16FrameSize", ActualSamples: samples,
		ActualBytes: bytes, ActualError: joinErrors(sampleErr, byteErr),
		ErrorIsExpected: errors.Is(sampleErr, audio.ErrInvalidPCM16FrameSize) && errors.Is(byteErr, audio.ErrInvalidPCM16FrameSize),
		Passed:          samples == 0 && bytes == 0 && errors.Is(sampleErr, audio.ErrInvalidPCM16FrameSize) && errors.Is(byteErr, audio.ErrInvalidPCM16FrameSize),
	}
}

func joinErrors(left, right error) string {
	leftText := errorText(left)
	rightText := errorText(right)
	if leftText == rightText {
		return leftText
	}
	return leftText + "; " + rightText
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
