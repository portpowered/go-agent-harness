package wavio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadValidationErrors(t *testing.T) {
	valid := canonicalWAV(Rate16kHz, []int16{1, -2, 3})
	truncatedData := valid[:len(valid)-1]

	stereo := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint16(stereo[22:24], 2)

	eightBit := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint16(eightBit[34:36], 8)

	fortyFourOne := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint32(fortyFourOne[24:28], 44100)
	binary.LittleEndian.PutUint32(fortyFourOne[28:32], 88200)

	badContainer := append([]byte(nil), valid...)
	copy(badContainer[0:4], "RIFX")

	oddData := buildWAV(
		makeChunk("fmt ", pcmFormatPayload(Rate16kHz)),
		makeChunk("data", []byte{1}),
	)
	missingFormat := buildWAV(makeChunk("data", pcmSamples([]int16{1})))
	shortChunkHeader := append([]byte("RIFF"), 5, 0, 0, 0, 'W', 'A', 'V', 'E', 1)

	tests := []struct {
		name      string
		input     []byte
		want      func(error) bool
		fragments []string
	}{
		{
			name:  "truncated header",
			input: valid[:11],
			want: func(err error) bool {
				var typed *TruncatedError
				return errors.As(err, &typed) && errors.Is(err, ErrTruncated)
			},
			fragments: []string{"RIFF header", "11", "12"},
		},
		{
			name:  "truncated data chunk",
			input: truncatedData,
			want: func(err error) bool {
				var typed *TruncatedError
				return errors.As(err, &typed) && errors.Is(err, ErrTruncated)
			},
			fragments: []string{"data chunk", "5", "6"},
		},
		{
			name:  "stereo input",
			input: stereo,
			want: func(err error) bool {
				var typed *UnsupportedError
				return errors.As(err, &typed) && errors.Is(err, ErrUnsupportedChannels)
			},
			fragments: []string{"channels", "2", "mono"},
		},
		{
			name:  "8-bit input",
			input: eightBit,
			want: func(err error) bool {
				var typed *UnsupportedError
				return errors.As(err, &typed) && errors.Is(err, ErrUnsupportedBitDepth)
			},
			fragments: []string{"bit depth", "8", "16"},
		},
		{
			name:  "44100 Hz input",
			input: fortyFourOne,
			want: func(err error) bool {
				var typed *UnsupportedError
				return errors.As(err, &typed) && errors.Is(err, ErrUnsupportedRate)
			},
			fragments: []string{"sample rate", "44100", "16000"},
		},
		{
			name:  "zero-length data",
			input: buildWAV(makeChunk("fmt ", pcmFormatPayload(Rate16kHz)), makeChunk("data", nil)),
			want: func(err error) bool {
				var typed *EmptyError
				return errors.As(err, &typed) && errors.Is(err, ErrEmptyData)
			},
			fragments: []string{"data", "0", "read"},
		},
		{
			name:  "malformed container",
			input: badContainer,
			want: func(err error) bool {
				var typed *MalformedError
				return errors.As(err, &typed) && errors.Is(err, ErrMalformed)
			},
			fragments: []string{"container", "RIFX", "RIFF"},
		},
		{
			name:  "odd PCM data length",
			input: oddData,
			want: func(err error) bool {
				var typed *MalformedError
				return errors.As(err, &typed) && errors.Is(err, ErrMalformed)
			},
			fragments: []string{"data length", "1", "even"},
		},
		{
			name:  "missing format chunk",
			input: missingFormat,
			want: func(err error) bool {
				var typed *MalformedError
				return errors.As(err, &typed) && errors.Is(err, ErrMalformed)
			},
			fragments: []string{"fmt chunk", "missing"},
		},
		{
			name:  "incomplete chunk header inside RIFF",
			input: shortChunkHeader,
			want: func(err error) bool {
				var typed *MalformedError
				return errors.As(err, &typed) && errors.Is(err, ErrMalformed)
			},
			fragments: []string{"chunk header", "fewer than 8"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotRate, gotSamples, err := Read(bytes.NewReader(test.input))
			if err == nil {
				t.Fatal("Read() error = nil, want validation error")
			}
			if gotRate != 0 || gotSamples != nil {
				t.Fatalf("Read() failure returned rate %d samples %#v, want zero and nil", gotRate, gotSamples)
			}
			if !test.want(err) {
				t.Fatalf("Read() error = %T %v, did not match expected typed error", err, err)
			}
			for _, fragment := range test.fragments {
				if !strings.Contains(err.Error(), fragment) {
					t.Errorf("Read() error %q does not contain %q", err, fragment)
				}
			}
		})
	}
}

func TestReadRejectsAdditionalMalformedShapes(t *testing.T) {
	valid := canonicalWAV(Rate16kHz, []int16{1, -2})
	badRIFFSize := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint32(badRIFFSize[4:8], 3)

	dataTooLarge := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint32(dataTooLarge[40:44], 99)

	unsupportedFormat := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint16(unsupportedFormat[20:22], 3)

	badBlockAlignment := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint16(badBlockAlignment[32:34], 4)

	badByteRate := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint32(badByteRate[28:32], 1)

	duplicateFormat := buildWAV(
		makeChunk("fmt ", pcmFormatPayload(Rate16kHz)),
		makeChunk("fmt ", pcmFormatPayload(Rate16kHz)),
		makeChunk("data", pcmSamples([]int16{1})),
	)
	duplicateData := buildWAV(
		makeChunk("fmt ", pcmFormatPayload(Rate16kHz)),
		makeChunk("data", pcmSamples([]int16{1})),
		makeChunk("data", pcmSamples([]int16{2})),
	)
	shortFormat := buildWAV(
		makeChunk("fmt ", make([]byte, 15)),
		makeChunk("data", pcmSamples([]int16{1})),
	)
	missingData := buildWAV(makeChunk("fmt ", pcmFormatPayload(Rate16kHz)))
	truncatedPadding := buildWAV(makeChunk("JUNK", []byte{1, 2, 3}))
	truncatedPadding = truncatedPadding[:len(truncatedPadding)-1]

	tests := []struct {
		name      string
		input     []byte
		fragments []string
	}{
		{name: "RIFF size below form", input: badRIFFSize, fragments: []string{"RIFF size", "3"}},
		{name: "data chunk exceeds RIFF", input: dataTooLarge, fragments: []string{"data chunk size", "99"}},
		{name: "unsupported format", input: unsupportedFormat, fragments: []string{"audio format", "3", "PCM"}},
		{name: "bad block alignment", input: badBlockAlignment, fragments: []string{"block alignment", "4", "2"}},
		{name: "bad byte rate", input: badByteRate, fragments: []string{"byte rate", "1", "32000"}},
		{name: "duplicate format", input: duplicateFormat, fragments: []string{"fmt chunk", "duplicate"}},
		{name: "duplicate data", input: duplicateData, fragments: []string{"data chunk", "duplicate"}},
		{name: "short format chunk", input: shortFormat, fragments: []string{"fmt chunk size", "15"}},
		{name: "missing data chunk", input: missingData, fragments: []string{"data chunk", "missing"}},
		{name: "truncated chunk padding", input: truncatedPadding, fragments: []string{"JUNK padding", "0", "1"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rate, samples, err := Read(bytes.NewReader(test.input))
			if err == nil {
				t.Fatal("Read() error = nil, want malformed error")
			}
			switch test.name {
			case "unsupported format":
				var unsupported *UnsupportedError
				if !errors.As(err, &unsupported) {
					t.Fatalf("Read() error = %T %v, want UnsupportedError", err, err)
				}
			case "truncated chunk padding":
				var truncated *TruncatedError
				if !errors.As(err, &truncated) {
					t.Fatalf("Read() error = %T %v, want TruncatedError", err, err)
				}
			default:
				var typed *MalformedError
				if !errors.As(err, &typed) {
					t.Fatalf("Read() error = %T %v, want MalformedError", err, err)
				}
			}
			if rate != 0 || samples != nil {
				t.Fatalf("Read() failure returned rate %d samples %#v", rate, samples)
			}
			for _, fragment := range test.fragments {
				if !strings.Contains(err.Error(), fragment) {
					t.Errorf("Read() error %q does not contain %q", err, fragment)
				}
			}
		})
	}
}

func TestTypedErrorContracts(t *testing.T) {
	unsupported := &UnsupportedError{Property: "unknown", Observed: 9, Supported: "known"}
	if unsupported.Error() != "unsupported WAV unknown: got 9; want known" || !errors.Is(unsupported, ErrUnsupported) || errors.Is(unsupported, ErrUnsupportedFormat) {
		t.Fatalf("unexpected unknown UnsupportedError behavior: %v", unsupported)
	}
	malformed := &MalformedError{Property: "field", Observed: 9}
	if malformed.Error() != "malformed WAV field: got 9" || !errors.Is(malformed, ErrMalformed) {
		t.Fatalf("unexpected no-reason MalformedError behavior: %v", malformed)
	}
	empty := &EmptyError{Property: "samples"}
	if empty.Error() != "empty WAV samples: got 0" || !errors.Is(empty, ErrEmptySamples) {
		t.Fatalf("unexpected no-operation EmptyError behavior: %v", empty)
	}
	size := &SizeError{Property: "data", Observed: 9, Maximum: 8}
	if size.Error() != "WAV data size 9 exceeds maximum 8" || !errors.Is(size, ErrSize) {
		t.Fatalf("unexpected SizeError behavior: %v", size)
	}
	stream := &StreamError{Operation: "read"}
	if stream.Error() != "WAV read failed" || !errors.Is(stream, ErrStream) {
		t.Fatalf("unexpected nil-cause StreamError behavior: %v", stream)
	}
	tooLarge := uint64(^uint(0)>>1) + 1
	if _, err := readData(bytes.NewReader(nil), tooLarge); err == nil || !errors.Is(err, ErrSize) {
		t.Fatalf("readData(too-large) error = %v, want ErrSize", err)
	}
}

func TestWriteValidationErrors(t *testing.T) {
	custom := errors.New("writer broke")
	tests := []struct {
		name string
		call func(io.Writer) error
		want func(error) bool
	}{
		{
			name: "unsupported rate",
			call: func(w io.Writer) error { return Write(w, 44100, []int16{1}) },
			want: func(err error) bool {
				var typed *UnsupportedError
				return errors.As(err, &typed) && errors.Is(err, ErrUnsupportedRate) && strings.Contains(err.Error(), "44100")
			},
		},
		{
			name: "empty samples",
			call: func(w io.Writer) error { return Write(w, Rate16kHz, nil) },
			want: func(err error) bool {
				var typed *EmptyError
				return errors.As(err, &typed) && errors.Is(err, ErrEmptySamples) && strings.Contains(err.Error(), "samples")
			},
		},
		{
			name: "underlying writer error",
			call: func(w io.Writer) error { return Write(w, Rate16kHz, []int16{1}) },
			want: func(err error) bool {
				return errors.Is(err, custom) && errors.Is(err, ErrStream)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writer := io.Writer(&bytes.Buffer{})
			if test.name == "underlying writer error" {
				writer = failingWriter{err: custom}
			}
			err := test.call(writer)
			if err == nil || !test.want(err) {
				t.Fatalf("Write() error = %T %v, want typed validation/I/O error", err, err)
			}
		})
	}
}

func TestReadAndWriteHandleNilAndNoProgressStreams(t *testing.T) {
	if _, samples, err := Read(nil); err == nil || samples != nil || !errors.Is(err, ErrStream) {
		t.Fatalf("Read(nil) = samples %#v error %v, want stream error and nil samples", samples, err)
	}
	if err := Write(nil, Rate16kHz, []int16{1}); err == nil || !errors.Is(err, ErrStream) {
		t.Fatalf("Write(nil) = %v, want stream error", err)
	}
	if _, samples, err := Read(noProgressReader{}); err == nil || samples != nil || !errors.Is(err, ErrStream) || !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("Read(no-progress) = samples %#v error %v, want wrapped no-progress error", samples, err)
	}
	if _, samples, err := Read(invalidCountReader{}); err == nil || samples != nil || !errors.Is(err, ErrStream) {
		t.Fatalf("Read(invalid-count) = samples %#v error %v, want stream error", samples, err)
	}
}

func TestReadPropagatesUnderlyingReaderError(t *testing.T) {
	sentinel := errors.New("reader broke")
	reader := &failingReader{data: canonicalWAV(Rate16kHz, []int16{1})[:12], err: sentinel}
	rate, samples, err := Read(reader)
	if rate != 0 || samples != nil || err == nil {
		t.Fatalf("Read() = rate %d samples %#v error %v, want failed read", rate, samples, err)
	}
	if !errors.Is(err, sentinel) || !errors.Is(err, ErrStream) {
		t.Fatalf("Read() error = %v, want underlying sentinel and ErrStream", err)
	}
}

func TestWriteHandlesShortAndInvalidWriters(t *testing.T) {
	shortErr := Write(zeroWriter{}, Rate16kHz, []int16{1})
	if shortErr == nil || !errors.Is(shortErr, io.ErrShortWrite) || !errors.Is(shortErr, ErrStream) {
		t.Fatalf("zero writer error = %v, want wrapped io.ErrShortWrite", shortErr)
	}

	partialErr := errors.New("partial writer broke")
	writeErr := Write(partialFailWriter{err: partialErr}, Rate16kHz, []int16{1})
	if writeErr == nil || !errors.Is(writeErr, partialErr) || !errors.Is(writeErr, ErrStream) {
		t.Fatalf("partial writer error = %v, want wrapped sentinel", writeErr)
	}

	invalidErr := Write(invalidCountWriter{}, Rate16kHz, []int16{1})
	if invalidErr == nil || !errors.Is(invalidErr, ErrStream) {
		t.Fatalf("invalid-count writer error = %v, want stream error", invalidErr)
	}
}

type failingReader struct {
	data []byte
	err  error
}

func (r *failingReader) Read(destination []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, r.err
	}
	count := copy(destination, r.data)
	r.data = r.data[count:]
	return count, nil
}

type noProgressReader struct{}

func (noProgressReader) Read([]byte) (int, error) { return 0, nil }

type invalidCountReader struct{}

func (invalidCountReader) Read(destination []byte) (int, error) { return len(destination) + 1, nil }

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

type partialFailWriter struct {
	err error
}

func (w partialFailWriter) Write(destination []byte) (int, error) {
	return len(destination) / 2, w.err
}

type invalidCountWriter struct{}

func (invalidCountWriter) Write(destination []byte) (int, error) { return len(destination) + 1, nil }
