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
	var layout Layout
	formatFound, dataFound := false, false
	for remaining > 0 {
		if remaining < 8 {
			return Layout{}, &MalformedError{Property: "chunk header", Observed: remaining, Reason: "fewer than 8 bytes remain"}
		}
		var header [8]byte
		if err := readPart(r, header[:], "chunk header"); err != nil {
			return Layout{}, err
		}
		remaining -= 8
		n := uint64(binary.LittleEndian.Uint32(header[4:]))
		padded := n + (n & 1)
		if padded > remaining {
			return Layout{}, &TruncatedError{Property: string(header[:4]) + " chunk", Expected: padded, Read: remaining}
		}
		switch string(header[:4]) {
		case "fmt ":
			if formatFound || n < 16 {
				return Layout{}, &MalformedError{Property: "fmt chunk", Observed: n, Reason: "require one format chunk of at least 16 bytes"}
			}
			var payload [16]byte
			if err := readPart(r, payload[:], "fmt chunk"); err != nil {
				return Layout{}, err
			}
			format, err := validatePCM16Format(payload)
			if err != nil {
				return Layout{}, err
			}
			layout.SampleRate = int(format.sampleRate)
			formatFound = true
			if _, err := r.Seek(int64(padded-16), io.SeekCurrent); err != nil {
				return Layout{}, err
			}
		case "data":
			if dataFound || n%2 != 0 {
				return Layout{}, &MalformedError{Property: "data chunk", Observed: n, Reason: "require one PCM16 data chunk with an even byte count"}
			}
			position, err := r.Seek(0, io.SeekCurrent)
			if err != nil {
				return Layout{}, err
			}
			layout.DataOffset, layout.DataBytes = position, n
			dataFound = true
			if _, err := r.Seek(int64(padded), io.SeekCurrent); err != nil {
				return Layout{}, err
			}
		default:
			if _, err := r.Seek(int64(padded), io.SeekCurrent); err != nil {
				return Layout{}, err
			}
		}
		remaining -= padded
	}
	if !formatFound || !dataFound {
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
	if _, err := r.Seek(layout.DataOffset, io.SeekStart); err != nil {
		return Layout{}, err
	}
	return layout, nil
}
