package devices

import (
	"io"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// FileMediaLabels names each finite media role in admission errors. A host
// supplies the spelling its operators use for those roles (for example its
// command-line options); an empty label selects a neutral default.
type FileMediaLabels struct {
	Input        string
	InputTurn    string
	Interruption string
	Output       string
}

// FileMediaSource selects one caller-owned PCM input. Path "-" reads Stdin; a
// ".wav" path reads its header rate; any other path is raw PCM16 at
// SampleRate (non-positive selects audio.SampleRate).
type FileMediaSource struct {
	Path       string
	Stdin      io.Reader
	SampleRate int
	// CloseStdinOnCancel reads a process-local duplicate of an *os.File stdin
	// so closing the media handle wakes a blocked read without closing the
	// caller's descriptor.
	CloseStdinOnCancel bool
}

// FileMediaRequest describes every finite source and sink one live invocation
// attaches. Paths are already admitted by the host; the service owns opening,
// framing, pacing, and joined cleanup.
type FileMediaRequest struct {
	Input         *FileMediaSource
	InputTurns    []string
	Interruptions []string
	OutputPath    string
	// Stdout receives output when OutputPath is "-".
	Stdout           io.Writer
	OutputSampleRate int
	// NegotiateOutputRate defers the output file header until playback
	// reports its negotiated rate. It is implied whenever any input is set.
	NegotiateOutputRate bool
	// FrameInput reads Input with fixed-frame semantics for legacy captures
	// that did not record a negotiated input rate.
	FrameInput bool
	// ObserveSource optionally wraps Input and each InputTurn source (for
	// example with an evidence tap). The wrapper is closed in place of the
	// source it wraps.
	ObserveSource func(source audio.AudioSource, sampleRate int) audio.AudioSource
	Scheduler     clock.Scheduler
	Labels        FileMediaLabels
}

// FileMedia is the admitted finite media bundle for one invocation.
type FileMedia struct {
	Input         *FileInput
	InputTurns    []FileInput
	Interruptions []FileInput
	Output        *FileOutput
}

// FileMediaHandle owns every source and sink it admitted until Close, which
// releases them exactly once and is safe to call more than once.
type FileMediaHandle interface {
	Media() FileMedia
	Close() error
}

// FileMediaService opens a request's finite sources and sinks. A request that
// names no media returns a nil handle. A failed later admission closes every
// earlier port before the joined error returns.
type FileMediaService interface {
	OpenFileMedia(FileMediaRequest) (FileMediaHandle, error)
}
