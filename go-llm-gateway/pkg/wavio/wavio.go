package wavio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	// Rate16kHz is one of the sample rates accepted by Read and Write.
	Rate16kHz = 16000
	// Rate24kHz is one of the sample rates accepted by Read and Write.
	Rate24kHz = 24000

	pcmFormat       = 1
	monoChannels    = 1
	pcm16Bits       = 16
	pcm16BlockAlign = 2
	readBufferSize  = 32 * 1024
	maxUint32       = ^uint32(0)
)

var errInvalidReadCount = errors.New("reader returned an invalid byte count")

// SupportedSampleRates returns a new slice containing the sample rates
// accepted by this package.
func SupportedSampleRates() []int { return []int{Rate16kHz, Rate24kHz} }

// Read decodes a PCM16 mono WAV stream and returns its sample rate followed by
// the signed samples. The reader remains owned by the caller and is not
// closed. On every error Read returns a zero rate and a nil sample slice.
func Read(r io.Reader) (sampleRate int, samples []int16, err error) {
	if r == nil {
		return 0, nil, &StreamError{Operation: "read", Err: errors.New("nil reader")}
	}

	var header [12]byte
	if err := readPart(r, header[:], "RIFF header"); err != nil {
		return 0, nil, err
	}
	if string(header[0:4]) != "RIFF" {
		return 0, nil, &MalformedError{Property: "container", Observed: string(header[0:4]), Reason: "want RIFF"}
	}
	if string(header[8:12]) != "WAVE" {
		return 0, nil, &MalformedError{Property: "form", Observed: string(header[8:12]), Reason: "want WAVE"}
	}

	riffSize := uint64(binary.LittleEndian.Uint32(header[4:8]))
	if riffSize < 4 {
		return 0, nil, &MalformedError{Property: "RIFF size", Observed: riffSize, Reason: "must include the WAVE form"}
	}

	format, data, err := readChunks(r, riffSize-4)
	if err != nil {
		return 0, nil, err
	}

	samples = make([]int16, len(data)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(data[index*2:]))
	}
	return int(format.sampleRate), samples, nil
}

// Decode is an explicit synonym for Read.
func Decode(r io.Reader) (sampleRate int, samples []int16, err error) {
	return Read(r)
}

// Write encodes samples as deterministic PCM16 mono WAV bytes at sampleRate.
// It writes the canonical RIFF/WAVE header and little-endian sample payload to
// the caller-owned writer, which remains open. Empty samples are rejected.
func Write(w io.Writer, sampleRate int, samples []int16) error {
	if w == nil {
		return &StreamError{Operation: "write", Err: errors.New("nil writer")}
	}
	if !isSupportedRate(sampleRate) {
		return &UnsupportedError{Property: "sample rate", Observed: sampleRate, Supported: "16000 or 24000 Hz"}
	}
	if len(samples) == 0 {
		return &EmptyError{Property: "samples", Operation: "write"}
	}

	dataSize := uint64(len(samples)) * pcm16BlockAlign
	maximumDataSize := uint64(maxUint32) - 36
	maximumIntSize := uint64(^uint(0)>>1) - 44
	if maximumIntSize < maximumDataSize {
		maximumDataSize = maximumIntSize
	}
	if dataSize > maximumDataSize {
		return &SizeError{Property: "data", Observed: dataSize, Maximum: maximumDataSize}
	}

	encoded := make([]byte, 44+int(dataSize))
	copy(encoded[0:4], "RIFF")
	binary.LittleEndian.PutUint32(encoded[4:8], uint32(36+dataSize))
	copy(encoded[8:12], "WAVE")
	copy(encoded[12:16], "fmt ")
	binary.LittleEndian.PutUint32(encoded[16:20], 16)
	binary.LittleEndian.PutUint16(encoded[20:22], pcmFormat)
	binary.LittleEndian.PutUint16(encoded[22:24], monoChannels)
	binary.LittleEndian.PutUint32(encoded[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(encoded[28:32], uint32(sampleRate*pcm16BlockAlign))
	binary.LittleEndian.PutUint16(encoded[32:34], pcm16BlockAlign)
	binary.LittleEndian.PutUint16(encoded[34:36], pcm16Bits)
	copy(encoded[36:40], "data")
	binary.LittleEndian.PutUint32(encoded[40:44], uint32(dataSize))
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(encoded[44+index*2:], uint16(sample))
	}

	return writeAll(w, encoded)
}

// Encode is an explicit synonym for Write.
func Encode(w io.Writer, sampleRate int, samples []int16) error {
	return Write(w, sampleRate, samples)
}

type waveFormat struct {
	sampleRate uint32
}

func readChunks(r io.Reader, remaining uint64) (waveFormat, []byte, error) {
	var format waveFormat
	var data []byte
	formatFound := false
	dataFound := false

	for remaining > 0 {
		if remaining < 8 {
			return waveFormat{}, nil, &MalformedError{Property: "chunk header", Observed: remaining, Reason: "fewer than 8 bytes remain in RIFF"}
		}

		var chunkHeader [8]byte
		if err := readPart(r, chunkHeader[:], "chunk header"); err != nil {
			return waveFormat{}, nil, err
		}
		remaining -= 8
		chunkID := string(chunkHeader[0:4])
		chunkSize := uint64(binary.LittleEndian.Uint32(chunkHeader[4:8]))
		if chunkSize > remaining {
			return waveFormat{}, nil, &MalformedError{Property: chunkID + " chunk size", Observed: chunkSize, Reason: fmt.Sprintf("RIFF has only %d bytes remaining", remaining)}
		}
		if chunkSize&1 == 1 && chunkSize == remaining {
			return waveFormat{}, nil, &MalformedError{Property: chunkID + " padding", Observed: chunkSize, Reason: "odd chunks require a padding byte"}
		}

		switch chunkID {
		case "fmt ":
			if formatFound {
				return waveFormat{}, nil, &MalformedError{Property: "fmt chunk", Observed: "duplicate", Reason: "only one format chunk is supported"}
			}
			if chunkSize < 16 {
				return waveFormat{}, nil, &MalformedError{Property: "fmt chunk size", Observed: chunkSize, Reason: "PCM format requires at least 16 bytes"}
			}
			var payload [16]byte
			if err := readPart(r, payload[:], "fmt chunk"); err != nil {
				return waveFormat{}, nil, err
			}
			remaining -= 16
			validatedFormat, err := validateFormat(payload)
			if err != nil {
				return waveFormat{}, nil, err
			}
			format = validatedFormat
			if err := skipPart(r, chunkSize-16, "fmt chunk extension"); err != nil {
				return waveFormat{}, nil, err
			}
			remaining -= chunkSize - 16
			formatFound = true
		case "data":
			if dataFound {
				return waveFormat{}, nil, &MalformedError{Property: "data chunk", Observed: "duplicate", Reason: "only one data chunk is supported"}
			}
			if chunkSize == 0 {
				return waveFormat{}, nil, &EmptyError{Property: "data", Operation: "read"}
			}
			if chunkSize&1 == 1 {
				return waveFormat{}, nil, &MalformedError{Property: "data length", Observed: chunkSize, Reason: "PCM16 data must contain an even number of bytes"}
			}
			readDataValue, err := readData(r, chunkSize)
			if err != nil {
				return waveFormat{}, nil, err
			}
			data = readDataValue
			remaining -= chunkSize
			dataFound = true
		default:
			if err := skipPart(r, chunkSize, chunkID+" chunk"); err != nil {
				return waveFormat{}, nil, err
			}
			remaining -= chunkSize
		}

		if chunkSize&1 == 1 {
			var padding [1]byte
			if err := readPart(r, padding[:], chunkID+" padding"); err != nil {
				return waveFormat{}, nil, err
			}
			remaining--
		}
	}

	if !formatFound {
		return waveFormat{}, nil, &MalformedError{Property: "fmt chunk", Observed: "missing", Reason: "PCM format is required"}
	}
	if !dataFound {
		return waveFormat{}, nil, &MalformedError{Property: "data chunk", Observed: "missing", Reason: "audio data is required"}
	}
	return format, data, nil
}

func validateFormat(payload [16]byte) (waveFormat, error) {
	format := binary.LittleEndian.Uint16(payload[0:2])
	if format != pcmFormat {
		return waveFormat{}, &UnsupportedError{Property: "audio format", Observed: format, Supported: "PCM integer (1)"}
	}
	channels := binary.LittleEndian.Uint16(payload[2:4])
	if channels != monoChannels {
		return waveFormat{}, &UnsupportedError{Property: "channels", Observed: channels, Supported: "mono (1)"}
	}
	bits := binary.LittleEndian.Uint16(payload[14:16])
	if bits != pcm16Bits {
		return waveFormat{}, &UnsupportedError{Property: "bit depth", Observed: bits, Supported: "PCM16 (16 bits)"}
	}
	rate := binary.LittleEndian.Uint32(payload[4:8])
	if !isSupportedRate(int(rate)) {
		return waveFormat{}, &UnsupportedError{Property: "sample rate", Observed: rate, Supported: "16000 or 24000 Hz"}
	}
	blockAlign := binary.LittleEndian.Uint16(payload[12:14])
	if blockAlign != pcm16BlockAlign {
		return waveFormat{}, &MalformedError{Property: "block alignment", Observed: blockAlign, Reason: "PCM16 mono requires 2 bytes per sample"}
	}
	byteRate := binary.LittleEndian.Uint32(payload[8:12])
	wantByteRate := rate * pcm16BlockAlign
	if byteRate != wantByteRate {
		return waveFormat{}, &MalformedError{Property: "byte rate", Observed: byteRate, Reason: fmt.Sprintf("want %d", wantByteRate)}
	}
	return waveFormat{sampleRate: rate}, nil
}

func readData(r io.Reader, size uint64) ([]byte, error) {
	maxInt := uint64(^uint(0) >> 1)
	if size > maxInt {
		return nil, &SizeError{Property: "data", Observed: size, Maximum: maxInt}
	}

	data := make([]byte, 0, minUint(size, readBufferSize))
	var buffer [readBufferSize]byte
	remaining := size
	for remaining > 0 {
		count := minUint(remaining, uint64(len(buffer)))
		if err := readPart(r, buffer[:count], "data chunk"); err != nil {
			return nil, err
		}
		data = append(data, buffer[:count]...)
		remaining -= uint64(count)
	}
	return data, nil
}

func skipPart(r io.Reader, size uint64, property string) error {
	var buffer [readBufferSize]byte
	for size > 0 {
		count := minUint(size, uint64(len(buffer)))
		if err := readPart(r, buffer[:count], property); err != nil {
			return err
		}
		size -= uint64(count)
	}
	return nil
}

func readPart(r io.Reader, destination []byte, property string) error {
	read, err := readFull(r, destination)
	if err == nil {
		return nil
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return &TruncatedError{Property: property, Expected: uint64(len(destination)), Read: uint64(read)}
	}
	return &StreamError{Operation: "read", Err: err}
}

func readFull(r io.Reader, destination []byte) (int, error) {
	read := 0
	zeroReads := 0
	for read < len(destination) {
		n, err := r.Read(destination[read:])
		if n < 0 || n > len(destination)-read {
			return read, errInvalidReadCount
		}
		read += n
		if read == len(destination) {
			return read, nil
		}
		if err != nil {
			return read, err
		}
		if n == 0 {
			zeroReads++
			if zeroReads >= 100 {
				return read, io.ErrNoProgress
			}
		} else {
			zeroReads = 0
		}
	}
	return read, nil
}

func writeAll(w io.Writer, encoded []byte) error {
	for len(encoded) > 0 {
		written, err := w.Write(encoded)
		if written < 0 || written > len(encoded) {
			return &StreamError{Operation: "write", Err: errors.New("writer returned an invalid byte count")}
		}
		encoded = encoded[written:]
		if err != nil {
			return &StreamError{Operation: "write", Err: err}
		}
		if written == 0 {
			return &StreamError{Operation: "write", Err: io.ErrShortWrite}
		}
	}
	return nil
}

func minUint(left, right uint64) int {
	if left < right {
		return int(left)
	}
	return int(right)
}

func isSupportedRate(rate int) bool { return rate == Rate16kHz || rate == Rate24kHz }
