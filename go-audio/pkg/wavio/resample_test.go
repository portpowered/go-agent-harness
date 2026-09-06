package wavio

import (
	"encoding/binary"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestResampleAllOrderedRatePairs(t *testing.T) {
	tests := []struct {
		name       string
		inputRate  int
		outputRate int
		input      []int16
		want       []int16
	}{
		{name: "16 to 24", inputRate: Rate16kHz, outputRate: Rate24kHz, input: []int16{0, 3000}, want: []int16{0, 2000, 3000}},
		{name: "24 to 16", inputRate: Rate24kHz, outputRate: Rate16kHz, input: []int16{0, 3000, 6000}, want: []int16{0, 4500}},
		{name: "16 to 48", inputRate: Rate16kHz, outputRate: Rate48kHz, input: []int16{0, 3000}, want: []int16{0, 1000, 2000, 3000, 3000, 3000}},
		{name: "48 to 16", inputRate: Rate48kHz, outputRate: Rate16kHz, input: []int16{0, 3000, 6000, 9000}, want: []int16{0, 9000}},
		{name: "24 to 48", inputRate: Rate24kHz, outputRate: Rate48kHz, input: []int16{0, 3000}, want: []int16{0, 1500, 3000, 3000}},
		{name: "48 to 24", inputRate: Rate48kHz, outputRate: Rate24kHz, input: []int16{0, 3000, 6000, 9000}, want: []int16{0, 6000}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := append([]int16(nil), test.input...)
			got, err := Resample(input, test.inputRate, test.outputRate)
			if err != nil {
				t.Fatalf("Resample() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Resample() = %v, want %v", got, test.want)
			}
			if !reflect.DeepEqual(input, test.input) {
				t.Fatalf("Resample() mutated input: got %v, want %v", input, test.input)
			}
		})
	}
}

func TestResampleIdentityCopiesExactSamples(t *testing.T) {
	input := []int16{0, -32768, 32767, 1234, -2345}
	got, err := Resample(input, Rate24kHz, Rate24kHz)
	if err != nil {
		t.Fatalf("Resample() error = %v", err)
	}
	if !reflect.DeepEqual(got, input) {
		t.Fatalf("Resample() = %v, want %v", got, input)
	}
	got[0] = 99
	if input[0] != 0 {
		t.Fatalf("identity result aliases input: input[0] = %d", input[0])
	}

	empty, err := Resample(nil, Rate16kHz, Rate16kHz)
	if err != nil || len(empty) != 0 {
		t.Fatalf("Resample(empty identity) = %#v, %v; want empty result", empty, err)
	}
}

func TestResampleOutputLengthMatrixAndContentEvidence(t *testing.T) {
	rates := []int{Rate16kHz, Rate24kHz, Rate48kHz}
	lengths := []int{0, 1, 2, 3, 4, 5, 7, 10, 11, 17}
	for _, inputRate := range rates {
		for _, outputRate := range rates {
			for _, inputLength := range lengths {
				name := testRateName(inputRate) + "-to-" + testRateName(outputRate) + "-" + testLengthName(inputLength)
				t.Run(name, func(t *testing.T) {
					input := resamplePattern(inputLength)
					got, err := Resample(input, inputRate, outputRate)
					if err != nil {
						t.Fatalf("Resample() error = %v", err)
					}
					wantLength := (inputLength*outputRate + inputRate - 1) / inputRate
					if len(got) != wantLength {
						t.Fatalf("len(Resample()) = %d, want %d", len(got), wantLength)
					}
					if inputLength > 0 && !hasNonZeroSample(got) {
						t.Fatalf("Resample() returned no signal energy for input %v", input)
					}
					if !reflect.DeepEqual(input, resamplePattern(inputLength)) {
						t.Fatalf("Resample() mutated input: %v", input)
					}
				})
			}
		}
	}
}

func TestResampleUnsupportedRatesReturnTypedErrorsWithoutPartialSamples(t *testing.T) {
	tests := []struct {
		name       string
		inputRate  int
		outputRate int
		wantRate   int
		wantPart   string
	}{
		{name: "unsupported input", inputRate: 44100, outputRate: Rate16kHz, wantRate: 44100, wantPart: "input"},
		{name: "unsupported output", inputRate: Rate16kHz, outputRate: 44100, wantRate: 44100, wantPart: "output"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Resample([]int16{1, -2, 3}, test.inputRate, test.outputRate)
			if got != nil {
				t.Fatalf("Resample() samples = %v, want nil on error", got)
			}
			if err == nil || !errors.Is(err, ErrUnsupportedResampleRate) || !errors.Is(err, ErrUnsupportedRate) {
				t.Fatalf("Resample() error = %T %v, want unsupported-rate sentinels", err, err)
			}
			var typed *UnsupportedResampleRateError
			if !errors.As(err, &typed) || typed.Rate != test.wantRate || typed.Direction != test.wantPart {
				t.Fatalf("Resample() error = %T %v, want typed %s rate error", err, err, test.wantPart)
			}
			if !strings.Contains(err.Error(), "44100") {
				t.Fatalf("Resample() error = %q, want rejected rate", err)
			}
		})
	}
}

func TestResampleLengthOverflowIsRejectedBeforeAllocation(t *testing.T) {
	maximumInt := int(^uint(0) >> 1)
	if _, err := resampleOutputLength(maximumInt, Rate16kHz, Rate48kHz); err == nil || !errors.Is(err, ErrResampleSize) {
		t.Fatalf("resampleOutputLength(max int) error = %v, want ErrResampleSize", err)
	}
}

func TestResampleBoundarySignalsStayInPCM16Range(t *testing.T) {
	tests := []struct {
		name       string
		input      []int16
		wantPolari int
	}{
		{name: "positive DC", input: constantSamples(13, 12345), wantPolari: 1},
		{name: "negative DC", input: constantSamples(13, -12345), wantPolari: -1},
		{name: "full-scale positive", input: constantSamples(13, 32767), wantPolari: 1},
		{name: "full-scale negative", input: constantSamples(13, -32768), wantPolari: -1},
		{name: "alternating extremes", input: alternatingSamples(19), wantPolari: 0},
	}
	rates := []int{Rate16kHz, Rate24kHz, Rate48kHz}
	for _, test := range tests {
		for _, inputRate := range rates {
			for _, outputRate := range rates {
				t.Run(test.name+"/"+testRateName(inputRate)+"-to-"+testRateName(outputRate), func(t *testing.T) {
					got, err := Resample(test.input, inputRate, outputRate)
					if err != nil {
						t.Fatalf("Resample() error = %v", err)
					}
					energy := 0
					for index, sample := range got {
						value := int64(sample)
						if value < pcm16Minimum || value > pcm16Maximum {
							t.Fatalf("sample %d = %d, outside PCM16 range", index, sample)
						}
						if sample != 0 {
							energy++
						}
						if test.wantPolari > 0 && sample <= 0 {
							t.Fatalf("sample %d = %d, crossed positive polarity", index, sample)
						}
						if test.wantPolari < 0 && sample >= 0 {
							t.Fatalf("sample %d = %d, crossed negative polarity", index, sample)
						}
					}
					if energy == 0 {
						t.Fatal("boundary signal lost all energy")
					}
				})
			}
		}
	}
}

func FuzzResampleRoundTrip(f *testing.F) {
	f.Add([]byte{})
	f.Add(pcm16Bytes(constantSamples(8, 0)))
	f.Add(pcm16Bytes([]int16{0, 32767, 0, 0, 0, 0, 0, 0}))
	f.Add(pcm16Bytes(resamplePattern(17)))
	f.Add(pcm16Bytes(constantSamples(9, 8192)))
	f.Add(pcm16Bytes(alternatingSamples(18)))
	f.Add(pcm16Bytes([]int16{0, 1200, -2400, 3600, -4800, 6000, -7200, 8400}))

	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 8192 {
			raw = raw[:8192]
		}
		raw = raw[:len(raw)/2*2]
		input := make([]int16, len(raw)/2)
		for index := range input {
			input[index] = int16(binary.LittleEndian.Uint16(raw[index*2:]))
		}

		upsampled, err := Resample(input, Rate16kHz, Rate48kHz)
		if err != nil {
			t.Fatalf("16-to-48 Resample() error = %v", err)
		}
		roundTripped, err := Resample(upsampled, Rate48kHz, Rate16kHz)
		if err != nil {
			t.Fatalf("48-to-16 Resample() error = %v", err)
		}
		if len(roundTripped) != len(input) {
			t.Fatalf("round-trip length = %d, want %d", len(roundTripped), len(input))
		}
		for index, sample := range input {
			difference := int64(roundTripped[index]) - int64(sample)
			if difference < 0 {
				difference = -difference
			}
			if difference > MaxResampleRoundTripErrorLSB {
				t.Fatalf("round-trip sample %d error = %d LSBs, want <= %d", index, difference, MaxResampleRoundTripErrorLSB)
			}
		}
	})
}

const resampleFrameAllocationBudget = 1

var resampleAllocationSink int16

func BenchmarkResampleFrameAllocationBudget(b *testing.B) {
	frame := resamplePattern(Rate16kHz / 100)
	b.ReportAllocs()

	measured := testing.AllocsPerRun(100, func() {
		converted, err := Resample(frame, Rate16kHz, Rate48kHz)
		if err != nil {
			b.Fatalf("Resample() error = %v", err)
		}
		resampleAllocationSink ^= converted[len(converted)-1]
	})
	if !resampleWithinAllocationBudget(measured, resampleFrameAllocationBudget) {
		b.Fatalf("Resample() allocations/op = %v, want <= committed budget %d", measured, resampleFrameAllocationBudget)
	}

	b.ResetTimer()
	for range b.N {
		converted, err := Resample(frame, Rate16kHz, Rate48kHz)
		if err != nil {
			b.Fatalf("Resample() error = %v", err)
		}
		resampleAllocationSink ^= converted[len(converted)-1]
	}
}

func TestResampleAllocationBudget(t *testing.T) {
	frame := resamplePattern(Rate16kHz / 100)
	measured := testing.AllocsPerRun(100, func() {
		converted, err := Resample(frame, Rate16kHz, Rate48kHz)
		if err != nil {
			t.Fatalf("Resample() error = %v", err)
		}
		resampleAllocationSink ^= converted[len(converted)-1]
	})
	if !resampleWithinAllocationBudget(measured, resampleFrameAllocationBudget) {
		t.Fatalf("Resample() allocations/op = %v, want <= committed budget %d", measured, resampleFrameAllocationBudget)
	}
	if resampleWithinAllocationBudget(measured, measured-1) {
		t.Fatalf("allocation gate accepted measured value %v with budget %v", measured, measured-1)
	}
}

func resampleWithinAllocationBudget(measured, budget float64) bool {
	return measured <= budget
}

func resamplePattern(length int) []int16 {
	pattern := make([]int16, length)
	for index := range pattern {
		pattern[index] = int16((index+1)*1000 - 7000)
	}
	return pattern
}

func constantSamples(length int, value int16) []int16 {
	samples := make([]int16, length)
	for index := range samples {
		samples[index] = value
	}
	return samples
}

func alternatingSamples(length int) []int16 {
	samples := make([]int16, length)
	for index := range samples {
		if index%2 == 0 {
			samples[index] = -32768
		} else {
			samples[index] = 32767
		}
	}
	return samples
}

func hasNonZeroSample(samples []int16) bool {
	for _, sample := range samples {
		if sample != 0 {
			return true
		}
	}
	return false
}

func pcm16Bytes(samples []int16) []byte {
	encoded := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(encoded[index*2:], uint16(sample))
	}
	return encoded
}

func testRateName(rate int) string {
	switch rate {
	case Rate16kHz:
		return "16k"
	case Rate24kHz:
		return "24k"
	case Rate48kHz:
		return "48k"
	default:
		return "unknown"
	}
}

func testLengthName(length int) string {
	return strconv.Itoa(length)
}
