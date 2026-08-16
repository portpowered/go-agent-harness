package wavio

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

var updateGoldens = flag.Bool("update", false, "rewrite pkg/wavio golden WAV fixtures")

func TestGoldenFixtures(t *testing.T) {
	tests := []struct {
		name     string
		rate     int
		samples  []int16
		filename string
	}{
		{
			name:     "16khz",
			rate:     Rate16kHz,
			samples:  []int16{0, 1, -1, 32767, -32768, 1234, -2345},
			filename: "pcm16-mono-16000.wav",
		},
		{
			name:     "24khz",
			rate:     Rate24kHz,
			samples:  []int16{-32768, 32767, -7, 42, 2048},
			filename: "pcm16-mono-24000.wav",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var encoded bytes.Buffer
			if err := Write(&encoded, test.rate, test.samples); err != nil {
				t.Fatalf("Write() error = %v", err)
			}

			path := filepath.Join("testdata", test.filename)
			if *updateGoldens {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("MkdirAll() error = %v", err)
				}
				if err := os.WriteFile(path, encoded.Bytes(), 0o644); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
			}

			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden %s: %v; run go test -run TestGoldenFixtures -update to create it", path, err)
			}
			if !bytes.Equal(encoded.Bytes(), want) {
				t.Fatalf("encoded bytes differ from %s", path)
			}

			gotRate, gotSamples, err := Read(bytes.NewReader(want))
			if err != nil {
				t.Fatalf("Read(golden) error = %v", err)
			}
			if gotRate != test.rate || !reflect.DeepEqual(gotSamples, test.samples) {
				t.Fatalf("Read(golden) = rate %d samples %v, want rate %d samples %v", gotRate, gotSamples, test.rate, test.samples)
			}

			var reencoded bytes.Buffer
			if err := Write(&reencoded, gotRate, gotSamples); err != nil {
				t.Fatalf("Write(decoded golden) error = %v", err)
			}
			if !bytes.Equal(reencoded.Bytes(), want) {
				t.Fatalf("re-encoded golden bytes differ from %s", path)
			}
		})
	}
}

func TestWriteCanonicalAndDeterministic(t *testing.T) {
	samples := []int16{0, 1, -1, 32767, -32768, 99}
	for _, rate := range []int{Rate16kHz, Rate24kHz} {
		var first, second bytes.Buffer
		if err := Write(&first, rate, samples); err != nil {
			t.Fatalf("Write(%d) error = %v", rate, err)
		}
		if err := Write(&second, rate, samples); err != nil {
			t.Fatalf("second Write(%d) error = %v", rate, err)
		}
		want := canonicalWAV(rate, samples)
		if !bytes.Equal(first.Bytes(), want) {
			t.Fatalf("canonical bytes for %d do not match expected WAV", rate)
		}
		if !bytes.Equal(first.Bytes(), second.Bytes()) {
			t.Fatal("writing identical input twice produced different bytes")
		}
	}
}

func TestReadRoundTripsBoundarySamples(t *testing.T) {
	tests := []struct {
		name string
		rate int
		data []int16
	}{
		{name: "one sample at 16khz", rate: Rate16kHz, data: []int16{-32768}},
		{name: "odd samples at 24khz", rate: Rate24kHz, data: []int16{32767, -1, 123}},
		{name: "mixed extremes", rate: Rate16kHz, data: []int16{0, -32768, 32767, -42, 42}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var encoded bytes.Buffer
			if err := Write(&encoded, test.rate, test.data); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
			gotRate, gotSamples, err := Read(&encoded)
			if err != nil {
				t.Fatalf("Read() error = %v", err)
			}
			if gotRate != test.rate || !reflect.DeepEqual(gotSamples, test.data) {
				t.Fatalf("Read() = rate %d samples %v, want rate %d samples %v", gotRate, gotSamples, test.rate, test.data)
			}
		})
	}
}

func TestReadAcceptsPaddedAndExtendedChunks(t *testing.T) {
	samples := []int16{10, -20, 30}
	format := append(pcmFormatPayload(Rate16kHz), 0xab)
	largeJunk := bytes.Repeat([]byte{0x7f}, readBufferSize+1)
	input := buildWAV(
		makeChunk("JUNK", largeJunk),
		makeChunk("fmt ", format),
		makeChunk("data", pcmSamples(samples)),
	)

	gotRate, gotSamples, err := Read(bytes.NewReader(input))
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if gotRate != Rate16kHz || !reflect.DeepEqual(gotSamples, samples) {
		t.Fatalf("Read() = rate %d samples %v, want rate %d samples %v", gotRate, gotSamples, Rate16kHz, samples)
	}
}

func TestSupportedSampleRatesReturnsFreshValues(t *testing.T) {
	rates := SupportedSampleRates()
	if !reflect.DeepEqual(rates, []int{Rate16kHz, Rate24kHz}) {
		t.Fatalf("SupportedSampleRates() = %v", rates)
	}
	rates[0] = 0
	if got := SupportedSampleRates()[0]; got != Rate16kHz {
		t.Fatalf("SupportedSampleRates() returned shared storage with first value %d", got)
	}
}

func TestEncodeAndDecodeAliases(t *testing.T) {
	var encoded bytes.Buffer
	samples := []int16{4, -5, 6}
	if err := Encode(&encoded, Rate24kHz, samples); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	rate, got, err := Decode(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if rate != Rate24kHz || !reflect.DeepEqual(got, samples) {
		t.Fatalf("Decode() = rate %d samples %v, want rate %d samples %v", rate, got, Rate24kHz, samples)
	}
}
