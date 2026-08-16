package audiofixture

import (
	"errors"
	"fmt"
)

var (
	ErrUnknownID         = errors.New("unknown audio fixture ID")
	ErrMalformedManifest = errors.New("malformed audio fixture manifest")
	ErrMissingFile       = errors.New("manifest audio file is missing")
	ErrUnmanifestedFile  = errors.New("audio file is not in the manifest")
	ErrHashMismatch      = errors.New("audio fixture hash mismatch")
	ErrInvalidAudio      = errors.New("invalid audio fixture")
	ErrCorpusIO          = errors.New("audio fixture corpus I/O error")
	ErrInvalidFrameSize  = errors.New("audio fixture frame has an invalid size")
	ErrClosed            = errors.New("audio fixture source is closed")
)

// UnknownIDError reports an ID that is not declared by the validated manifest.
type UnknownIDError struct{ ID string }

func (e *UnknownIDError) Error() string        { return fmt.Sprintf("unknown audio fixture ID %q", e.ID) }
func (e *UnknownIDError) Is(target error) bool { return target == ErrUnknownID }

// MalformedManifestError reports invalid JSON or invalid manifest structure.
// Path is always a safe relative corpus path, normally "manifest.json".
type MalformedManifestError struct {
	Path   string
	Field  string
	Reason string
	Err    error
}

func (e *MalformedManifestError) Error() string {
	message := fmt.Sprintf("malformed audio fixture manifest %q", e.Path)
	if e.Field != "" {
		message += " field " + e.Field
	}
	if e.Reason != "" {
		message += ": " + e.Reason
	}
	return message
}

func (e *MalformedManifestError) Unwrap() error        { return e.Err }
func (e *MalformedManifestError) Is(target error) bool { return target == ErrMalformedManifest }

// MissingFileError reports a manifest entry whose declared file is absent.
type MissingFileError struct {
	ID   string
	Path string
}

func (e *MissingFileError) Error() string {
	return fmt.Sprintf("audio fixture %q is missing declared file %q", e.ID, e.Path)
}
func (e *MissingFileError) Is(target error) bool { return target == ErrMissingFile }

// UnmanifestedFileError reports a corpus WAV file absent from the manifest.
type UnmanifestedFileError struct{ Path string }

func (e *UnmanifestedFileError) Error() string {
	return fmt.Sprintf("audio fixture file %q has no manifest entry", e.Path)
}
func (e *UnmanifestedFileError) Is(target error) bool { return target == ErrUnmanifestedFile }

// HashMismatchError reports the exact expected and observed lowercase SHA-256.
type HashMismatchError struct {
	ID       string
	Path     string
	Expected string
	Actual   string
}

func (e *HashMismatchError) Error() string {
	return fmt.Sprintf("audio fixture %q hash mismatch for %q: expected %s, got %s", e.ID, e.Path, e.Expected, e.Actual)
}
func (e *HashMismatchError) Is(target error) bool { return target == ErrHashMismatch }

// InvalidAudioError reports audio bytes that cannot satisfy the source contract.
type InvalidAudioError struct {
	ID   string
	Path string
	Err  error
}

func (e *InvalidAudioError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("audio fixture %q at %q is invalid", e.ID, e.Path)
	}
	return fmt.Sprintf("audio fixture %q at %q is invalid: %v", e.ID, e.Path, e.Err)
}
func (e *InvalidAudioError) Unwrap() error { return e.Err }
func (e *InvalidAudioError) Is(target error) bool {
	return target == ErrInvalidAudio || (e.Err != nil && errors.Is(e.Err, target))
}

// CorpusIOError reports a read-only corpus operation failure without exposing
// the machine-specific absolute root path.
type CorpusIOError struct {
	Operation string
	Path      string
	Err       error
}

func (e *CorpusIOError) Error() string {
	message := fmt.Sprintf("audio fixture corpus %s %q failed", e.Operation, e.Path)
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message
}
func (e *CorpusIOError) Unwrap() error        { return e.Err }
func (e *CorpusIOError) Is(target error) bool { return target == ErrCorpusIO }

// FrameSizeError reports a buffer that cannot hold exactly one source frame.
type FrameSizeError struct {
	Operation string
	Got       int
	Want      int
}

func (e *FrameSizeError) Error() string {
	return fmt.Sprintf("audio fixture %s frame has %d samples; want exactly %d", e.Operation, e.Got, e.Want)
}
func (e *FrameSizeError) Is(target error) bool { return target == ErrInvalidFrameSize }

// ClosedError reports a read after Source.Close.
type ClosedError struct{ Operation string }

func (e *ClosedError) Error() string {
	return fmt.Sprintf("audio fixture %s: %s", e.Operation, ErrClosed)
}
func (e *ClosedError) Is(target error) bool { return target == ErrClosed }

// Compatibility aliases keep the failure vocabulary explicit for callers that
// describe a fixture rather than a manifest entry.
var (
	ErrUnknownFixture        = ErrUnknownID
	ErrMissingAudioFile      = ErrMissingFile
	ErrUnmanifestedAudioFile = ErrUnmanifestedFile
)

type UnknownFixtureError = UnknownIDError
type MissingAudioFileError = MissingFileError
type UnmanifestedAudioFileError = UnmanifestedFileError
