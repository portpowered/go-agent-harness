package wavio

import (
	"errors"
	"fmt"
)

// Sentinel errors allow callers to use errors.Is while the concrete errors
// expose the property and observed value that caused validation to fail.
var (
	ErrMalformed           = errors.New("malformed WAV")
	ErrTruncated           = errors.New("truncated WAV")
	ErrUnsupported         = errors.New("unsupported WAV property")
	ErrEmpty               = errors.New("empty WAV audio")
	ErrEmptySamples        = errors.New("empty WAV samples")
	ErrEmptyData           = errors.New("empty WAV data")
	ErrSize                = errors.New("WAV size overflow")
	ErrStream              = errors.New("WAV stream I/O error")
	ErrUnsupportedFormat   = errors.New("unsupported WAV audio format")
	ErrUnsupportedChannels = errors.New("unsupported WAV channel count")
	ErrUnsupportedBitDepth = errors.New("unsupported WAV bit depth")
	ErrUnsupportedRate     = errors.New("unsupported WAV sample rate")
)

// UnsupportedError reports a valid WAV field whose value is outside this
// package's supported PCM16 mono contract.
type UnsupportedError struct {
	Property  string
	Observed  any
	Supported string
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("unsupported WAV %s: got %v; want %s", e.Property, e.Observed, e.Supported)
}

func (e *UnsupportedError) Is(target error) bool {
	if target == ErrUnsupported {
		return true
	}
	switch e.Property {
	case "audio format":
		return target == ErrUnsupportedFormat
	case "channels":
		return target == ErrUnsupportedChannels
	case "bit depth":
		return target == ErrUnsupportedBitDepth
	case "sample rate":
		return target == ErrUnsupportedRate
	default:
		return false
	}
}

// MalformedError reports an internally inconsistent or structurally invalid
// WAV field.
type MalformedError struct {
	Property string
	Observed any
	Reason   string
}

func (e *MalformedError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("malformed WAV %s: got %v", e.Property, e.Observed)
	}
	return fmt.Sprintf("malformed WAV %s: got %v (%s)", e.Property, e.Observed, e.Reason)
}

func (e *MalformedError) Is(target error) bool { return target == ErrMalformed }

// TruncatedError reports a stream that ended before a declared WAV field was
// fully available.
type TruncatedError struct {
	Property string
	Expected uint64
	Read     uint64
}

func (e *TruncatedError) Error() string {
	return fmt.Sprintf("truncated WAV %s: read %d of %d bytes", e.Property, e.Read, e.Expected)
}

func (e *TruncatedError) Is(target error) bool { return target == ErrTruncated }

// EmptyError reports an empty sample slice or a zero-length data chunk.
type EmptyError struct {
	Property  string
	Operation string
}

func (e *EmptyError) Error() string {
	if e.Operation == "" {
		return fmt.Sprintf("empty WAV %s: got 0", e.Property)
	}
	return fmt.Sprintf("empty WAV %s during %s: got 0", e.Property, e.Operation)
}

func (e *EmptyError) Is(target error) bool {
	if target == ErrEmpty {
		return true
	}
	if e.Property == "samples" {
		return target == ErrEmptySamples
	}
	return e.Property == "data" && target == ErrEmptyData
}

// SizeError reports a size that cannot be represented by the RIFF/WAVE
// container or by the host's slice index type.
type SizeError struct {
	Property string
	Observed uint64
	Maximum  uint64
}

func (e *SizeError) Error() string {
	return fmt.Sprintf("WAV %s size %d exceeds maximum %d", e.Property, e.Observed, e.Maximum)
}

func (e *SizeError) Is(target error) bool { return target == ErrSize }

// StreamError wraps an error returned by the caller-owned reader or writer.
type StreamError struct {
	Operation string
	Err       error
}

func (e *StreamError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("WAV %s failed", e.Operation)
	}
	return fmt.Sprintf("WAV %s failed: %v", e.Operation, e.Err)
}

func (e *StreamError) Unwrap() error { return e.Err }

func (e *StreamError) Is(target error) bool { return target == ErrStream }
