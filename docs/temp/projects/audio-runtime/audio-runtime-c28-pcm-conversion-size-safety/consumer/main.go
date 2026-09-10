package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

const schema = "audio-runtime-c28-pcm-consumer.v1"

type report struct {
	Schema       string            `json:"schema"`
	Case         string            `json:"case"`
	Source       string            `json:"source"`
	GOOS         string            `json:"goos"`
	GOARCH       string            `json:"goarch"`
	IntBits      int               `json:"int_bits"`
	StartedAt    string            `json:"started_at"`
	DurationMS   int64             `json:"duration_ms"`
	Cases        []caseReport      `json:"cases,omitempty"`
	Ownership    []ownershipReport `json:"ownership,omitempty"`
	Dimensions   []dimensionReport `json:"dimensions,omitempty"`
	CleanShutoff bool              `json:"clean_shutdown"`
	Error        string            `json:"error,omitempty"`
}

type caseReport struct {
	Name            string  `json:"name"`
	API             string  `json:"api"`
	InputSamples    []int16 `json:"input_samples,omitempty"`
	InputBytes      int     `json:"input_bytes,omitempty"`
	InputRate       int     `json:"input_rate,omitempty"`
	OutputRate      int     `json:"output_rate,omitempty"`
	SourceChannels  int     `json:"source_channels,omitempty"`
	TargetChannels  int     `json:"target_channels,omitempty"`
	ExpectedSamples []int16 `json:"expected_samples,omitempty"`
	ActualSamples   []int16 `json:"actual_samples,omitempty"`
	ExpectedBytes   int     `json:"expected_bytes,omitempty"`
	ActualBytes     int     `json:"actual_bytes,omitempty"`
	ExpectedError   string  `json:"expected_error,omitempty"`
	ActualError     string  `json:"actual_error,omitempty"`
	ErrorIsExpected bool    `json:"error_is_expected"`
	Panicked        bool    `json:"panicked"`
	Panic           string  `json:"panic,omitempty"`
	ReturnedNil     bool    `json:"returned_nil"`
	Passed          bool    `json:"passed"`
}

type ownershipReport struct {
	Name                           string `json:"name"`
	InputUnchangedAfterSuccess     bool   `json:"input_unchanged_after_success"`
	OutputIndependentAfterInputMut bool   `json:"output_independent_after_input_mutation"`
	Passed                         bool   `json:"passed"`
}

type dimensionReport struct {
	Name                   string `json:"name"`
	WordBits               int    `json:"word_bits"`
	SourceChannels         int    `json:"source_channels"`
	DoubledSourceChannels  int    `json:"doubled_source_channels"`
	DoubledSourceRepresent bool   `json:"doubled_source_representable"`
	PositiveZeroWrap       bool   `json:"positive_zero_wrap_reachable"`
	ZeroWrapProof          string `json:"zero_wrap_proof"`
	TargetChannels         int    `json:"target_channels"`
	OutputFrames           int    `json:"output_frames"`
	OutputSamplesBytes     string `json:"output_sample_bytes"`
	OutputBytesRepresent   bool   `json:"output_bytes_representable"`
	PublicCall             bool   `json:"public_call"`
	ResourceBounded        bool   `json:"resource_bounded"`
}

type callResult struct {
	Samples    []int16
	Bytes      []byte
	Err        error
	Panicked   bool
	PanicValue string
}

func main() {
	caseName := flag.String("case", "verify", "verify or characterize")
	outputPath := flag.String("output", "", "optional JSON report path")
	flag.Parse()

	started := time.Now()
	result := &report{
		Schema:       schema,
		Case:         *caseName,
		Source:       sourceRevision(),
		GOOS:         runtime.GOOS,
		GOARCH:       runtime.GOARCH,
		IntBits:      32 << (^uint(0) >> 63),
		StartedAt:    started.UTC().Format(time.RFC3339Nano),
		CleanShutoff: true,
	}

	var err error
	switch *caseName {
	case "verify":
		err = runVerify(result)
	case "characterize":
		err = runCharacterize(result)
	default:
		err = fmt.Errorf("unsupported case %q", *caseName)
	}
	result.DurationMS = time.Since(started).Milliseconds()
	if err != nil {
		result.Error = err.Error()
	}

	encoded, marshalErr := json.MarshalIndent(result, "", "  ")
	if marshalErr != nil {
		fmt.Fprintln(os.Stderr, marshalErr)
		os.Exit(1)
	}
	encoded = append(encoded, '\n')
	if *outputPath != "" {
		if writeErr := os.WriteFile(*outputPath, encoded, 0o600); writeErr != nil {
			fmt.Fprintln(os.Stderr, writeErr)
			os.Exit(1)
		}
	}
	if _, writeErr := os.Stdout.Write(encoded); writeErr != nil {
		fmt.Fprintln(os.Stderr, writeErr)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func sourceRevision() string {
	if source := os.Getenv("C28_SOURCE_REVISION"); source != "" {
		return source
	}
	return "working-tree"
}

func runVerify(result *report) error {
	maximumInt := int(^uint(0) >> 1)
	result.Cases = append(result.Cases,
		validResampleCase("supported-rate-known-answer", []int16{0, 3000}, 16000, 24000, []int16{0, 2000, 3000}),
		validResampleCase("arbitrary-rate-known-answer", []int16{0, 1000}, 1000, 1500, []int16{0, 667, 1000}),
		validResampleCase("negative-half-rounding", []int16{0, -1001}, 2, 4, []int16{0, -501, -1001, -1001}),
		validResampleCase("identity-copy", []int16{1, -2, 3}, 1000, 1000, []int16{1, -2, 3}),
		validConvertCase("stereo-downmix", []int16{0, 1000, 2000, 3000}, 2, 1000, 1, 1000, []int16{500, 2500}),
		validConvertCase("mono-stereo-arbitrary-rate", []int16{0, 3000}, 1, 1000, 2, 1500, []int16{0, 0, 2000, 2000, 3000, 3000}),
		validConvertCase("extra-channel-repeat", []int16{1, 2}, 2, 16000, 3, 16000, []int16{1, 2, 2}),
		validConvertCase("signed-extrema-identity", []int16{-32768, 32767}, 1, 16000, 1, 16000, []int16{-32768, 32767}),
		validConvertCase("empty-input", nil, 1, 16000, 2, 24000, []int16{}),
		invalidConvertCase("source-frame-first-invalid", nil, maximumInt/2+1, 16000, 1, 16000, "ErrPCM16ConversionSize"),
		invalidConvertCase("source-frame-max-int", codec.EncodePCM16([]int16{0}), maximumInt, 16000, maximumInt, 16000, "ErrPCM16ConversionSize"),
		invalidConvertCase("target-sample-byte-overflow", codec.EncodePCM16([]int16{0}), 1, 16000, maximumInt/2+1, 16000, "ErrPCM16ConversionSize"),
		invalidResampleCase("float-to-int-rounded-boundary", []int16{1}, 1, maximumInt, "ErrPCM16ConversionSize"),
	)

	result.Ownership = append(result.Ownership, resampleOwnership(), convertOwnership())
	result.Dimensions = append(result.Dimensions, dimensionControls(maximumInt)...)

	for _, test := range result.Cases {
		if !test.Passed {
			return fmt.Errorf("consumer case %s failed", test.Name)
		}
	}
	for _, test := range result.Ownership {
		if !test.Passed {
			return fmt.Errorf("consumer ownership case %s failed", test.Name)
		}
	}
	for _, test := range result.Dimensions {
		if test.DoubledSourceRepresent || test.OutputBytesRepresent {
			return fmt.Errorf("consumer dimension oracle %s failed", test.Name)
		}
	}
	return nil
}

func runCharacterize(result *report) error {
	maximumInt := int(^uint(0) >> 1)
	firstInvalid := maximumInt/2 + 1
	result.Cases = append(result.Cases, characterizeConvertCase("empty-max-source-channels", nil, maximumInt, 16000, 1, 16000), characterizeConvertCase("tiny-max-source-channels", codec.EncodePCM16([]int16{0}), maximumInt, 16000, maximumInt, 16000), characterizeConvertCase("tiny-first-wrapped-frame-size", codec.EncodePCM16([]int16{0}), firstInvalid, 16000, 1, 16000), characterizeResampleCase("float-length-boundary", []int16{1}, 1, maximumInt))
	result.Dimensions = dimensionControls(maximumInt)
	return nil
}

func validResampleCase(name string, input []int16, inputRate, outputRate int, expected []int16) caseReport {
	got := callResample(input, inputRate, outputRate)
	report := caseReport{Name: name, API: "ResamplePCM16", InputSamples: input, InputRate: inputRate, OutputRate: outputRate, ExpectedSamples: expected, ActualSamples: got.Samples, ActualError: errorText(got.Err), Panicked: got.Panicked, Panic: got.PanicValue, ReturnedNil: got.Samples == nil}
	report.Passed = !got.Panicked && got.Err == nil && equalSamples(got.Samples, expected)
	return report
}

func invalidResampleCase(name string, input []int16, inputRate, outputRate int, expectedError string) caseReport {
	got := callResample(input, inputRate, outputRate)
	report := caseReport{Name: name, API: "ResamplePCM16", InputSamples: input, InputRate: inputRate, OutputRate: outputRate, ExpectedError: expectedError, ActualError: errorText(got.Err), ErrorIsExpected: errors.Is(got.Err, audio.ErrPCM16ConversionSize), Panicked: got.Panicked, Panic: got.PanicValue, ReturnedNil: got.Samples == nil}
	report.Passed = !got.Panicked && got.Err != nil && report.ErrorIsExpected && got.Samples == nil
	return report
}

func validConvertCase(name string, input []int16, sourceChannels, sourceRate, targetChannels, targetRate int, expected []int16) caseReport {
	pcm := codec.EncodePCM16(input)
	got := callConvert(pcm, sourceChannels, sourceRate, targetChannels, targetRate)
	actualSamples := decodeSamples(got.Bytes)
	report := caseReport{Name: name, API: "ConvertPCM16Bytes", InputSamples: input, InputBytes: len(pcm), InputRate: sourceRate, OutputRate: targetRate, SourceChannels: sourceChannels, TargetChannels: targetChannels, ExpectedSamples: expected, ActualSamples: actualSamples, ActualBytes: len(got.Bytes), ExpectedBytes: len(expected) * 2, ActualError: errorText(got.Err), Panicked: got.Panicked, Panic: got.PanicValue, ReturnedNil: got.Bytes == nil}
	report.Passed = !got.Panicked && got.Err == nil && equalSamples(actualSamples, expected) && len(got.Bytes) == len(expected)*2
	return report
}

func invalidConvertCase(name string, pcm []byte, sourceChannels, sourceRate, targetChannels, targetRate int, expectedError string) caseReport {
	got := callConvert(pcm, sourceChannels, sourceRate, targetChannels, targetRate)
	report := caseReport{Name: name, API: "ConvertPCM16Bytes", InputBytes: len(pcm), InputRate: sourceRate, OutputRate: targetRate, SourceChannels: sourceChannels, TargetChannels: targetChannels, ExpectedError: expectedError, ActualBytes: len(got.Bytes), ActualError: errorText(got.Err), ErrorIsExpected: errors.Is(got.Err, audio.ErrPCM16ConversionSize), Panicked: got.Panicked, Panic: got.PanicValue, ReturnedNil: got.Bytes == nil}
	report.Passed = !got.Panicked && got.Err != nil && report.ErrorIsExpected && got.Bytes == nil
	return report
}

func characterizeResampleCase(name string, input []int16, inputRate, outputRate int) caseReport {
	got := callResample(input, inputRate, outputRate)
	return caseReport{Name: name, API: "ResamplePCM16", InputSamples: input, InputRate: inputRate, OutputRate: outputRate, ActualSamples: got.Samples, ActualError: errorText(got.Err), ErrorIsExpected: errors.Is(got.Err, audio.ErrPCM16ConversionSize), Panicked: got.Panicked, Panic: got.PanicValue, ReturnedNil: got.Samples == nil}
}

func characterizeConvertCase(name string, pcm []byte, sourceChannels, sourceRate, targetChannels, targetRate int) caseReport {
	got := callConvert(pcm, sourceChannels, sourceRate, targetChannels, targetRate)
	return caseReport{Name: name, API: "ConvertPCM16Bytes", InputBytes: len(pcm), InputRate: sourceRate, OutputRate: targetRate, SourceChannels: sourceChannels, TargetChannels: targetChannels, ActualBytes: len(got.Bytes), ActualError: errorText(got.Err), ErrorIsExpected: errors.Is(got.Err, audio.ErrPCM16ConversionSize), Panicked: got.Panicked, Panic: got.PanicValue, ReturnedNil: got.Bytes == nil}
}

func callResample(input []int16, inputRate, outputRate int) (result callResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result.Panicked = true
			result.PanicValue = fmt.Sprint(recovered)
		}
	}()
	result.Samples, result.Err = audio.ResamplePCM16(input, inputRate, outputRate)
	return result
}

func callConvert(pcm []byte, sourceChannels, sourceRate, targetChannels, targetRate int) (result callResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result.Panicked = true
			result.PanicValue = fmt.Sprint(recovered)
		}
	}()
	result.Bytes, result.Err = audio.ConvertPCM16Bytes(pcm, sourceChannels, sourceRate, targetChannels, targetRate)
	return result
}

func resampleOwnership() ownershipReport {
	input := []int16{1, -2, 3}
	got, err := audio.ResamplePCM16(input, 1000, 1000)
	if err != nil {
		return ownershipReport{Name: "ResamplePCM16 identity", Passed: false}
	}
	got[0] = 99
	input[1] = 88
	return ownershipReport{Name: "ResamplePCM16 identity", InputUnchangedAfterSuccess: input[0] == 1 && input[2] == 3, OutputIndependentAfterInputMut: got[1] == -2, Passed: got[0] == 99 && input[1] == 88 && got[1] == -2}
}

func convertOwnership() ownershipReport {
	input := codec.EncodePCM16([]int16{1, -2})
	got, err := audio.ConvertPCM16Bytes(input, 1, 1000, 1, 1000)
	if err != nil {
		return ownershipReport{Name: "ConvertPCM16Bytes identity", Passed: false}
	}
	got[0] = 99
	input[1] = 88
	return ownershipReport{Name: "ConvertPCM16Bytes identity", InputUnchangedAfterSuccess: input[0] == 1 && input[2] == 254 && input[3] == 255, OutputIndependentAfterInputMut: got[1] == 0, Passed: got[0] == 99 && input[1] == 88 && got[1] == 0}
}

func dimensionControls(maximumInt int) []dimensionReport {
	firstInvalid := maximumInt/2 + 1
	maximumSamples := maximumInt / 2
	wordBits := 32 << (^uint(0) >> 63)
	return []dimensionReport{
		{Name: "source-frame-boundary", WordBits: wordBits, SourceChannels: firstInvalid, DoubledSourceChannels: 2 * firstInvalid, DoubledSourceRepresent: uint64(firstInvalid) <= uint64(maximumInt)/2, PositiveZeroWrap: false, ZeroWrapProof: fmt.Sprintf("positive int channels are at most 2^(%d-1)-1; doubling them is at most 2^%d-2, so zero modulo 2^%d is unreachable", wordBits, wordBits, wordBits), ResourceBounded: true},
		{Name: "target-sample-byte-boundary", WordBits: wordBits, TargetChannels: firstInvalid, OutputFrames: 1, OutputSamplesBytes: fmt.Sprintf("%d", uint64(firstInvalid)*2), OutputBytesRepresent: uint64(firstInvalid) <= uint64(maximumSamples), ResourceBounded: true},
	}
}

func decodeSamples(pcm []byte) []int16 {
	samples, err := codec.DecodePCM16(pcm)
	if err != nil {
		return nil
	}
	return samples
}

func equalSamples(left, right []int16) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
