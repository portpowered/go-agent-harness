package wavio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
)

func FuzzRoundTrip(f *testing.F) {
	f.Add(uint8(0), []byte{})
	f.Add(uint8(1), []byte{0, 128})
	f.Add(uint8(0), []byte{0, 0, 255, 127, 255, 255})
	f.Add(uint8(1), []byte{1, 0, 254, 255, 3, 0, 252, 255, 5, 0})
	f.Add(uint8(0), []byte{0, 0, 1, 0, 255, 255, 255, 127, 0, 128, 232, 3, 24, 252, 42, 0, 249, 255, 7, 0, 249, 255})
	f.Add(uint8(1), bytes.Repeat([]byte{0x34, 0x12}, 1024))

	f.Fuzz(func(t *testing.T, rateSelector uint8, rawSamples []byte) {
		if len(rawSamples) > 8192 {
			rawSamples = rawSamples[:8192]
		}
		rawSamples = rawSamples[:len(rawSamples)/2*2]
		samples := make([]int16, len(rawSamples)/2)
		for index := range samples {
			samples[index] = int16(binary.LittleEndian.Uint16(rawSamples[index*2:]))
		}
		rate := Rate16kHz
		if rateSelector%2 == 1 {
			rate = Rate24kHz
		}

		var encoded bytes.Buffer
		err := Write(&encoded, rate, samples)
		if len(samples) == 0 {
			if err == nil || !errors.Is(err, ErrEmptySamples) {
				t.Fatalf("Write(empty) error = %v, want ErrEmptySamples", err)
			}
			return
		}
		if err != nil {
			t.Fatalf("Write() error = %v", err)
		}

		gotRate, gotSamples, err := Read(bytes.NewReader(encoded.Bytes()))
		if err != nil {
			t.Fatalf("Read(Write()) error = %v", err)
		}
		if gotRate != rate || !reflect.DeepEqual(gotSamples, samples) {
			t.Fatalf("Read(Write()) = rate %d samples %v, want rate %d samples %v", gotRate, gotSamples, rate, samples)
		}
	})
}

func FuzzDecodeArbitraryBytes(f *testing.F) {
	f.Add(canonicalWAV(Rate16kHz, []int16{0, -1, 32767}))
	f.Add(canonicalWAV(Rate24kHz, []int16{-32768, 1, 2, 3}))
	f.Add([]byte("RIFF"))
	f.Add(buildWAV(makeChunk("fmt ", pcmFormatPayload(Rate16kHz)), makeChunk("data", nil)))
	f.Add(buildWAV(makeChunk("data", []byte{1, 2, 3})))

	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 64*1024 {
			input = input[:64*1024]
		}
		rate, samples, err := Read(bytes.NewReader(input))
		if err != nil {
			if rate != 0 || samples != nil {
				t.Fatalf("Read(arbitrary) failure returned rate %d samples %#v with error %v", rate, samples, err)
			}
			return
		}
		if rate != Rate16kHz && rate != Rate24kHz {
			t.Fatalf("successful arbitrary decode returned unsupported rate %d", rate)
		}
		if len(samples) == 0 {
			t.Fatal("successful arbitrary decode returned no samples")
		}
	})
}
