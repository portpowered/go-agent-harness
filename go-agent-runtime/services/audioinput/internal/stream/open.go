package stream

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// WithSource gives one callback ownership of an opened source and joins its
// cleanup error with the callback result.
func WithSource[T any](open func() (T, error), run func(T) error, close func(T) error) (runErr error) {
	source, err := open()
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, close(source)) }()
	return run(source)
}

// StreamWithCleanup joins an explicitly transferred source cleanup error with
// the stream result and never closes a caller-owned source implicitly.
func StreamWithCleanup(ctx context.Context, service AudioService, input Input, loop SessionLoop, close func() error) (runErr error) {
	defer func() { runErr = errors.Join(runErr, close()) }()
	return service.Stream(ctx, input, loop)
}

// ReadWithCleanup applies the same explicit ownership rule to finite reads.
func ReadWithCleanup(ctx context.Context, service AudioService, input Input, close func() error) (pcm []byte, rate int, runErr error) {
	defer func() { runErr = errors.Join(runErr, close()) }()
	return service.Read(ctx, input)
}

// StreamManaged applies the managed source clock, ports, and cleanup policy.
func StreamManaged(ctx context.Context, service AudioService, source *ManagedSource, loop SessionLoop) (runErr error) {
	if source == nil {
		return &Error{Kind: KindUnreadable, Err: audio.ErrNilStream}
	}
	streamClock := clock.Ensure(source.Clock)
	return StreamWithCleanup(ctx, service, source.Input(streamClock), loop, source.Close)
}

// ReadManaged applies the managed source and cleanup policy to a finite read.
func ReadManaged(ctx context.Context, service AudioService, source *ManagedSource) (pcm []byte, rate int, runErr error) {
	if source == nil {
		return nil, 0, &Error{Kind: KindUnreadable, Err: audio.ErrNilStream}
	}
	return ReadWithCleanup(ctx, service, source.Input(nil), source.Close)
}

// DispatchWithObservation adapts a bounded callback without exposing the
// private stream implementation.
func DispatchWithObservation(ctx context.Context, service AudioService, loop SessionLoop, input ScheduledInput, options DispatchOptions, observe func(AudioObservation)) error {
	if observe != nil {
		options.Observer = ObserverFunc(observe)
	}
	return service.Dispatch(ctx, loop, input, options)
}

// OpenWAVFile keeps host file acquisition separate from reusable WAV parsing.
func OpenWAVFile[T ReadSeekCloser](path string, open func(string) (T, error)) (audio.AudioSource, error) {
	reader, err := open(path)
	if err != nil {
		return nil, err
	}
	source, err := NewWAVSource(path, reader)
	if err != nil {
		err = errors.Join(err, reader.Close())
	}
	return source, err
}

// Open turns a host-selected spec into a managed runtime source. It does
// not open paths itself and leaves classification of host errors to the host.
func (s *Service) Open(input InputSpec, opener Opener) (*ManagedSource, error) {
	rate := input.SourceSampleRate
	if rate == 0 {
		rate = audio.SampleRate
	}
	if input.Source != nil {
		return managedSource(input, input.Source, rate, false), nil
	}
	if strings.EqualFold(filepath.Ext(input.Path), ".wav") {
		return openWAV(input, opener)
	}
	if input.Path == "-" && input.Stdin == nil {
		return nil, &Error{Kind: KindUnreadable, Err: audio.ErrNilStream}
	}
	if input.Path == "-" {
		return openStdin(input, opener, rate)
	}
	return openFile(input, opener, rate)
}

func managedSource(input InputSpec, source audio.AudioSource, sourceRate int, pace bool) *ManagedSource {
	return &ManagedSource{
		Source: source, Path: input.Path, SourceSampleRate: sourceRate, Pace: pace,
		SendAudioInput: input.SendAudioInput, SendEndOfTurn: input.SendEndOfTurn,
	}
}

func classifyOpen(opener Opener, path string, err error) error {
	if opener.Classify != nil {
		return opener.Classify(path, err)
	}
	return err
}

func openWAV(input InputSpec, opener Opener) (*ManagedSource, error) {
	if opener.OpenWAV == nil {
		return nil, ErrUnavailable
	}
	source, err := opener.OpenWAV(input.Path)
	if err != nil {
		return nil, classifyOpen(opener, input.Path, err)
	}
	return managedSource(input, source, SourceRate(source, audio.SampleRate), true), nil
}

func openStdin(input InputSpec, opener Opener, rate int) (*ManagedSource, error) {
	if opener.PrepareStdin == nil {
		return openFile(input, opener, rate)
	}
	stdin, err := opener.PrepareStdin(input.Stdin, input.CloseStdinOnCancel)
	if err != nil {
		return nil, classifyOpen(opener, input.Path, err)
	}
	reader, err := NewReaderSource(stdin, input.CloseStdinOnCancel)
	if err != nil {
		return nil, err
	}
	if opener.OpenFile == nil {
		return nil, errors.Join(ErrUnavailable, reader.Close())
	}
	source, err := opener.OpenFile(input.Path, reader)
	if err != nil {
		return nil, errors.Join(classifyOpen(opener, input.Path, err), reader.Close())
	}
	managed := managedSource(input, source, rate, false)
	managed.Reader, managed.CloseOnCancel, managed.OwnedInput = reader, input.CloseStdinOnCancel, reader
	return managed, nil
}

func openFile(input InputSpec, opener Opener, rate int) (*ManagedSource, error) {
	if opener.OpenFile == nil {
		return nil, ErrUnavailable
	}
	source, err := opener.OpenFile(input.Path, input.Stdin)
	if err != nil {
		return nil, classifyOpen(opener, input.Path, err)
	}
	if err := checkFilePath(input.Path, source, opener); err != nil {
		return nil, err
	}
	return managedSource(input, source, rate, input.Path != "-"), nil
}

func checkFilePath(path string, source audio.AudioSource, opener Opener) error {
	if opener.CheckPath == nil {
		return nil
	}
	info, err := opener.CheckPath(path)
	if err != nil {
		return errors.Join(classifyOpen(opener, path, err), source.Close())
	}
	if info != nil && info.IsDir() {
		return errors.Join(
			&Error{Kind: KindUnreadable, Path: path, Err: fmt.Errorf("path is a directory; provide a .wav, .pcm, or .raw file")},
			source.Close(),
		)
	}
	return nil
}
