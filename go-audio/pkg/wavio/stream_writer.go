package wavio

import (
	"encoding/binary"
	"errors"
	"io"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

// PCM16Header is the canonical mono PCM16 RIFF header used by whole-file and
// streaming writers. Any positive rate with a representable RIFF byte rate
// is valid; resampler rate admission is a separate contract. A zero-length
// data section is valid during recording.
func PCM16Header(sampleRate int, dataBytes uint64) ([44]byte, error) {
	var header [44]byte
	if sampleRate <= 0 || uint64(sampleRate) > uint64(maxUint32)/pcm16BlockAlign {
		return header, &UnsupportedError{Property: "sample rate", Observed: sampleRate, Supported: "positive PCM16 rate with a representable RIFF byte rate"}
	}
	if dataBytes > uint64(maxUint32)-36 {
		return header, &SizeError{Property: "data", Observed: dataBytes, Maximum: uint64(maxUint32) - 36}
	}
	if dataBytes%2 != 0 {
		return header, codec.ErrPCM16OddLength
	}
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], uint32(36+dataBytes))
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16)
	binary.LittleEndian.PutUint16(header[20:22], pcmFormat)
	binary.LittleEndian.PutUint16(header[22:24], monoChannels)
	binary.LittleEndian.PutUint32(header[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(header[28:32], uint32(uint64(sampleRate)*pcm16BlockAlign))
	binary.LittleEndian.PutUint16(header[32:34], pcm16BlockAlign)
	binary.LittleEndian.PutUint16(header[34:36], pcm16Bits)
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], uint32(dataBytes))
	return header, nil
}

// StreamWriter persists PCM as it arrives with bounded working memory. Its
// owner serializes calls and owns the underlying writer. Close finalizes the
// header without closing that writer; Checkpoint makes the current complete
// prefix readable before the session ends.
type StreamWriter struct {
	writer  io.WriteSeeker
	rate    int
	start   int64
	bytes   uint64
	closed  bool
	err     error
	encoded [32 * 1024]byte
}

func NewStreamWriter(writer io.WriteSeeker, sampleRate int) (*StreamWriter, error) {
	if writer == nil {
		return nil, &StreamError{Operation: "write", Err: errors.New("nil writer")}
	}
	header, err := PCM16Header(sampleRate, 0)
	if err != nil {
		return nil, err
	}
	start, err := writer.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	if err := writeAll(writer, header[:]); err != nil {
		return nil, err
	}
	return &StreamWriter{writer: writer, rate: sampleRate, start: start}, nil
}

func (w *StreamWriter) WriteSamples(samples []int16) error {
	if w.closed {
		return errors.New("WAV stream writer is closed")
	}
	if w.err != nil {
		return w.err
	}
	if uint64(len(samples)) > (uint64(maxUint32)-36-w.bytes)/2 {
		w.err = &SizeError{Property: "data", Observed: w.bytes + uint64(len(samples))*2, Maximum: uint64(maxUint32) - 36}
		return w.err
	}
	for len(samples) > 0 {
		n := min(len(samples), len(w.encoded)/2)
		encoded := w.encoded[:n*2]
		if err := codec.EncodePCM16Into(encoded, samples[:n]); err != nil {
			w.err = err
			return err
		}
		for len(encoded) > 0 {
			written, err := w.writer.Write(encoded)
			if written < 0 || written > len(encoded) {
				w.err = io.ErrShortWrite
				return w.err
			}
			w.bytes += uint64(written)
			encoded = encoded[written:]
			if err != nil {
				w.err = err
				return err
			}
			if written == 0 {
				w.err = io.ErrShortWrite
				return w.err
			}
		}
		samples = samples[n:]
	}
	return nil
}

func (w *StreamWriter) BytesWritten() uint64 { return w.bytes }

func (w *StreamWriter) Checkpoint() error {
	if w.err != nil {
		return w.err
	}
	header, err := PCM16Header(w.rate, w.bytes)
	if err != nil {
		w.err = err
		return err
	}
	end, err := w.writer.Seek(0, io.SeekCurrent)
	if err != nil {
		w.err = err
		return err
	}
	if _, err = w.writer.Seek(w.start, io.SeekStart); err == nil {
		err = writeAll(w.writer, header[:])
	}
	_, restoreErr := w.writer.Seek(end, io.SeekStart)
	w.err = errors.Join(err, restoreErr)
	return w.err
}

func (w *StreamWriter) Close() error {
	if w.closed {
		return w.err
	}
	w.closed = true
	return w.Checkpoint()
}
