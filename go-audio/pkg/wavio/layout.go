package wavio

import (
	"encoding/binary"
	"io"
)

// Layout describes validated mono PCM16 audio in a seekable RIFF container.
// Inspect reads metadata only; it never loads the audio payload into memory.
type Layout struct {
	SampleRate int
	DataOffset int64
	DataBytes  uint64
}

func Inspect(r io.ReadSeeker) (Layout, error) {
	start, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return Layout{}, err
	}
	var descriptor [12]byte
	if err := readPart(r, descriptor[:], "RIFF header"); err != nil {
		return Layout{}, err
	}
	if string(descriptor[:4]) != "RIFF" || string(descriptor[8:]) != "WAVE" {
		return Layout{}, &MalformedError{Property: "container", Observed: string(descriptor[:]), Reason: "want RIFF/WAVE"}
	}
	size := uint64(binary.LittleEndian.Uint32(descriptor[4:8]))
	if size < 4 {
		return Layout{}, &MalformedError{Property: "RIFF size", Observed: size, Reason: "must include WAVE form"}
	}
	remaining := size - 4
	scan := layoutScan{r: r}
	for remaining > 0 {
		consumed, err := scan.next(remaining)
		if err != nil {
			return Layout{}, err
		}
		remaining -= consumed
	}
	if !scan.formatFound || !scan.dataFound {
		return Layout{}, &MalformedError{Property: "chunks", Observed: "missing", Reason: "fmt and data required"}
	}
	// Seeking beyond EOF succeeds for regular files. Check the physical extent
	// before exposing a stream so a truncated recording cannot look complete.
	end, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return Layout{}, err
	}
	if end < start+int64(size)+8 {
		return Layout{}, &TruncatedError{Property: "RIFF payload", Expected: size + 8, Read: uint64(max(0, end-start))}
	}
	if _, err := r.Seek(scan.layout.DataOffset, io.SeekStart); err != nil {
		return Layout{}, err
	}
	return scan.layout, nil
}

// layoutScan records chunk metadata while Inspect seeks across the RIFF
// payload without loading audio samples.
type layoutScan struct {
	r           io.ReadSeeker
	layout      Layout
	formatFound bool
	dataFound   bool
}

// next inspects one chunk and returns the RIFF payload bytes it spans,
// including its header and padding byte.
func (s *layoutScan) next(remaining uint64) (uint64, error) {
	if remaining < chunkHeaderBytes {
		return 0, &MalformedError{Property: "chunk header", Observed: remaining, Reason: "fewer than 8 bytes remain"}
	}
	var header [chunkHeaderBytes]byte
	if err := readPart(s.r, header[:], "chunk header"); err != nil {
		return 0, err
	}
	remaining -= chunkHeaderBytes
	n := uint64(binary.LittleEndian.Uint32(header[4:]))
	padded := n + (n & 1)
	if padded > remaining {
		return 0, &TruncatedError{Property: string(header[:4]) + " chunk", Expected: padded, Read: remaining}
	}
	var err error
	switch string(header[:4]) {
	case "fmt ":
		err = s.inspectFormat(n, padded)
	case "data":
		err = s.inspectData(n, padded)
	default:
		_, err = s.r.Seek(int64(padded), io.SeekCurrent)
	}
	if err != nil {
		return 0, err
	}
	return chunkHeaderBytes + padded, nil
}

func (s *layoutScan) inspectFormat(n, padded uint64) error {
	if s.formatFound || n < pcmFormatChunkBytes {
		return &MalformedError{Property: "fmt chunk", Observed: n, Reason: "require one format chunk of at least 16 bytes"}
	}
	var payload [pcmFormatChunkBytes]byte
	if err := readPart(s.r, payload[:], "fmt chunk"); err != nil {
		return err
	}
	format, err := validatePCM16Format(payload)
	if err != nil {
		return err
	}
	s.layout.SampleRate = int(format.sampleRate)
	s.formatFound = true
	_, err = s.r.Seek(int64(padded-pcmFormatChunkBytes), io.SeekCurrent)
	return err
}

func (s *layoutScan) inspectData(n, padded uint64) error {
	if s.dataFound || n%2 != 0 {
		return &MalformedError{Property: "data chunk", Observed: n, Reason: "require one PCM16 data chunk with an even byte count"}
	}
	position, err := s.r.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	s.layout.DataOffset, s.layout.DataBytes = position, n
	s.dataFound = true
	_, err = s.r.Seek(int64(padded), io.SeekCurrent)
	return err
}
