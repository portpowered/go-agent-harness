package stream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	audioinput "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioinput"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

type Input = audioinput.Input
type InputSpec = audioinput.InputSpec
type Error = audioinput.Error
type ErrorKind = audioinput.ErrorKind
type ReadSeekCloser = audioinput.ReadSeekCloser
type Opener = audioinput.Opener
type SessionLoop = audioinput.SessionLoop
type ScheduledInput = audioinput.ScheduledInput
type DispatchOptions = audioinput.DispatchOptions
type AudioObservation = audioinput.AudioObservation
type Observer = audioinput.Observer
type ObserverFunc = audioinput.ObserverFunc
type InvocationEvent = audioinput.InvocationEvent
type TurnStopPolicy = audioinput.TurnStopPolicy
type ManagedSource = audioinput.ManagedSource
type AudioService = audioinput.Service

const (
	KindEmpty           = audioinput.KindEmpty
	KindMissing         = audioinput.KindMissing
	KindUnreadable      = audioinput.KindUnreadable
	KindFormat          = audioinput.KindFormat
	KindConflict        = audioinput.KindConflict
	KindRead            = audioinput.KindRead
	KindSend            = audioinput.KindSend
	KindClose           = audioinput.KindClose
	KindUninterruptible = audioinput.KindUninterruptible
	ErrEmpty            = audioinput.ErrEmpty
	ErrMissing          = audioinput.ErrMissing
	ErrUnreadable       = audioinput.ErrUnreadable
	ErrFormat           = audioinput.ErrFormat
	ErrConflict         = audioinput.ErrConflict
	ErrRead             = audioinput.ErrRead
	ErrSend             = audioinput.ErrSend
	ErrClose            = audioinput.ErrClose
	ErrUninterruptible  = audioinput.ErrUninterruptible
	ErrEndOfTurnLost    = audioinput.ErrEndOfTurnLost
	ErrPCM16Truncated   = audioinput.ErrPCM16Truncated
	ErrUnavailable      = audioinput.ErrUnavailable
)

const readerFrameBytes = audio.FrameSize * 2

func newEmptyError(path string) error {
	return &Error{Kind: KindEmpty, Path: path, Err: fmt.Errorf("no audio frames were sent; refusing to commit an empty user turn: %w", ErrEmpty)}
}

// ValidateInput rejects malformed sources before a session or provider side
// effect. Injected sources may omit Path because it is diagnostic metadata.
func ValidateInput(input Input) error {
	if input.DevicePresent {
		return &Error{Kind: KindConflict, Path: input.Path, Err: ErrConflict}
	}
	if input.SourceSampleRate < 0 || input.ProviderSampleRate < 0 {
		return &Error{Kind: KindFormat, Path: input.Path, Err: fmt.Errorf("sample rates must not be negative")}
	}
	count := 0
	if input.Source != nil {
		count++
	}
	if input.Reader != nil {
		count++
	}
	if input.Buffer != nil {
		count++
	}
	if count > 1 {
		return &Error{Kind: KindConflict, Path: input.Path, Err: fmt.Errorf("multiple audio sources supplied")}
	}
	if count == 0 {
		return &Error{Kind: KindEmpty, Path: input.Path, Err: ErrEmpty}
	}
	if input.Buffer != nil && len(input.Buffer) == 0 {
		return newEmptyError(input.Path)
	}
	return nil
}

// NewBufferSource decodes a caller-owned little-endian mono PCM16 buffer into
// a finite frame source. The source is independent of later buffer mutation.
func NewBufferSource(pcm []byte) (audio.AudioSource, error) {
	if len(pcm) == 0 {
		return nil, newEmptyError("")
	}
	if len(pcm)%2 != 0 {
		return nil, fmt.Errorf("%w: %d bytes", ErrPCM16Truncated, len(pcm))
	}
	samples, err := codec.DecodePCM16WithLimit(pcm, len(pcm))
	if err != nil {
		return nil, &Error{Kind: KindFormat, Err: err}
	}
	return audio.NewSliceSource(samples), nil
}

// NewReaderSource adapts a host-provided raw PCM16 reader. It never closes a
// caller-owned reader unless CloseOnCancel is explicitly true.
func NewReaderSource(reader io.Reader, closeOnCancel bool) (*ReaderSource, error) {
	if reader == nil {
		return nil, &Error{Kind: KindUnreadable, Err: audio.ErrNilStream}
	}
	return &ReaderSource{reader: reader, closeOnCancel: closeOnCancel, CloseOnCancel: closeOnCancel}, nil
}

// SourceRate returns a declared source rate when available, or fallback.
func SourceRate(source audio.AudioSource, fallback int) int {
	if rated, ok := source.(interface{ SampleRate() int }); ok && rated.SampleRate() > 0 {
		return rated.SampleRate()
	}
	return fallback
}

// CloseSources closes a source and optional host-owned resources in order.
func CloseSources(source audio.AudioSource, extras ...io.Closer) error {
	var result error
	if source != nil {
		result = errors.Join(result, source.Close())
	}
	for _, extra := range extras {
		if extra != nil {
			result = errors.Join(result, extra.Close())
		}
	}
	return result
}

// NewWAVSource validates an already-open WAV stream without filesystem access.
// The caller transfers ownership of reader only after this succeeds.
func NewWAVSource(path string, reader ReadSeekCloser) (audio.AudioSource, error) {
	source, err := audio.NewWAVSource(path, reader)
	if err != nil {
		// Keep the legacy audio.FormatError identity at the public boundary.
		// CLI callers use errors.Is(err, audio.ErrUnsupportedFormat) for both
		// unsupported extensions and malformed .wav containers.
		formatErr := &audio.FormatError{Path: path, Extension: ".wav", Format: "wav", Reason: err.Error(), Err: err}
		return nil, &Error{Kind: KindFormat, Path: path, Err: errors.Join(ErrFormat, formatErr)}
	}
	return source, nil
}

// ReadPCM reads a source using the canonical audio frame contract and returns
// the source rate when it exposes one. It does not retain the source after the
// call and intentionally leaves ownership/Close to the caller.
func ReadPCM(ctx context.Context, source audio.AudioSource, fallbackRate int) ([]byte, int, error) {
	if source == nil {
		return nil, 0, &Error{Kind: KindUnreadable, Err: audio.ErrNilStream}
	}
	if fallbackRate <= 0 {
		fallbackRate = audio.SampleRate
	}
	rate := fallbackRate
	if rated, ok := source.(interface{ SampleRate() int }); ok && rated.SampleRate() > 0 {
		rate = rated.SampleRate()
	}
	if samples, ok := source.(audio.SampleSource); ok {
		return readSamplesPCM(ctx, samples, rate)
	}
	frame := make([]int16, audio.FrameSize)
	var encoded bytes.Buffer
	for {
		clear(frame)
		err := source.ReadFrame(ctx, frame)
		if errors.Is(err, io.EOF) {
			break
		}
		if errors.Is(err, audio.ErrEndOfTurn) {
			break
		}
		if err != nil {
			return nil, 0, &Error{Kind: KindRead, Err: err}
		}
		payload := make([]byte, readerFrameBytes)
		if err := codec.EncodePCM16Into(payload, frame); err != nil {
			return nil, 0, &Error{Kind: KindFormat, Err: err}
		}
		_, _ = encoded.Write(payload)
	}
	if encoded.Len() == 0 {
		return nil, rate, newEmptyError("")
	}
	return encoded.Bytes(), rate, nil
}

func readSamplesPCM(ctx context.Context, source audio.SampleSource, rate int) ([]byte, int, error) {
	const sampleChunk = audio.FrameSize
	samples := make([]int16, sampleChunk)
	var encoded bytes.Buffer
	for {
		clear(samples)
		count, err := source.ReadSamples(ctx, samples)
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, 0, &Error{Kind: KindRead, Err: err}
		}
		if count > 0 {
			payload := make([]byte, count*2)
			if encodeErr := codec.EncodePCM16Into(payload, samples[:count]); encodeErr != nil {
				return nil, 0, &Error{Kind: KindFormat, Err: encodeErr}
			}
			_, _ = encoded.Write(payload)
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || count == 0 {
			break
		}
	}
	if encoded.Len() == 0 {
		return nil, rate, newEmptyError("")
	}
	return encoded.Bytes(), rate, nil
}
