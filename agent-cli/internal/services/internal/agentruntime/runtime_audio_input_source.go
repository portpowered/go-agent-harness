package agentruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	sharedclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	devicegateway "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

func validateRuntimeAudioInput(input RuntimeAudioInput) error {
	if input.DevicePresent {
		return &RuntimeAudioInputError{
			Kind: RuntimeAudioInputConflict,
			Path: input.Path,
			Err:  ErrRuntimeAudioInputConflict,
		}
	}
	if strings.TrimSpace(input.Path) == "" {
		return &RuntimeAudioInputError{
			Kind: RuntimeAudioInputEmpty,
			Path: input.Path,
			Err:  ErrRuntimeAudioInputEmpty,
		}
	}
	return nil
}

// validateRuntimeAudioInputFileExists is a lightweight existence preflight
// for a file-backed --audio-in path. It intentionally runs before the
// generic validateSessionRunOptions check so a missing --audio-in file is
// reported as a missing file instead of being masked by provider setup — a
// `session --audio-in <missing file>` invocation must reach the file-open code
// that names the real problem. Stdin ("-") and an injected test Source are
// exempt: there is no filesystem path to check.
func validateRuntimeAudioInputFileExists(input RuntimeAudioInput) error {
	if input.Source != nil || input.Path == "" || input.Path == "-" {
		return nil
	}
	if _, err := os.Stat(input.Path); err != nil {
		return classifySessionAudioOpenError(input.Path, err)
	}
	return nil
}

func openRuntimeAudioInput(input RuntimeAudioInput) (*runtimeAudioSource, error) {
	sourceRate := input.SourceSampleRate
	if sourceRate == 0 {
		sourceRate = audio.SampleRate
	}
	if input.Source != nil {
		return newInjectedAudioSource(input, sourceRate), nil
	}
	if strings.EqualFold(filepath.Ext(input.Path), ".wav") {
		return openWAVAudioInput(input)
	}
	return openStreamAudioInput(input)
}

func newInjectedAudioSource(input RuntimeAudioInput, sourceRate int) *runtimeAudioSource {
	return &runtimeAudioSource{source: input.Source, path: input.Path, sourceRate: sourceRate, continuous: true, emitBoundaryOnSilence: input.EmitBoundaryOnSilence, send: input.SendAudioInput, endOfTurn: input.SendEndOfTurn}
}

func openWAVAudioInput(input RuntimeAudioInput) (*runtimeAudioSource, error) {
	source, err := openSessionWAVSource(input.Path)
	if err != nil {
		return nil, err
	}
	wavRate := runtimeAudioSourceSampleRate(source, audio.SampleRate)
	return &runtimeAudioSource{source: source, path: input.Path, sourceRate: wavRate, paced: true, emitBoundaryOnSilence: input.EmitBoundaryOnSilence, send: input.SendAudioInput, endOfTurn: input.SendEndOfTurn}, nil
}

func openStreamAudioInput(input RuntimeAudioInput) (*runtimeAudioSource, error) {
	stdin := input.Stdin
	var inputReader *sessionAudioReader
	var ownedInput *os.File
	if input.Path == "-" {
		var err error
		stdin, inputReader, ownedInput, err = prepareStdinAudioInput(input, stdin)
		if err != nil {
			return nil, err
		}
	}
	source, err := audio.NewFileSource(input.Path, stdin)
	if err != nil {
		return nil, closeOwnedInputOnError(input.Path, ownedInput, err)
	}
	if err := validateOpenedAudioPath(input.Path, source); err != nil {
		return nil, err
	}
	// Stdin is a live stream and its producer controls the delivery cadence.
	// Applying finite-file pacing here can deadlock deterministic runtimes:
	// the reader waits for a virtual timer before asking the bridge for its next
	// frame, while the bridge waits for that frame before advancing its schedule.
	return &runtimeAudioSource{source: source, path: input.Path, reader: inputReader, ownedInput: ownedInput, paced: input.Path != "-", continuous: input.Path == "-", send: input.SendAudioInput, endOfTurn: input.SendEndOfTurn}, nil
}

func prepareStdinAudioInput(input RuntimeAudioInput, stdin io.Reader) (io.Reader, *sessionAudioReader, *os.File, error) {
	if stdin == nil {
		return nil, nil, nil, classifySessionAudioOpenError(input.Path, audio.ErrNilStream)
	}
	var ownedInput *os.File
	if file, ok := stdin.(*os.File); ok && input.CloseStdinOnCancel {
		var err error
		ownedInput, err = devicegateway.OpenInterruptibleInput(file)
		if err != nil {
			return nil, nil, nil, classifySessionAudioOpenError(input.Path, err)
		}
		stdin = ownedInput
	}
	reader := newSessionAudioReader(stdin, input.CloseStdinOnCancel)
	return reader, reader, ownedInput, nil
}

func closeOwnedInputOnError(path string, owned *os.File, err error) error {
	if owned != nil {
		if closeErr := owned.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}
	return classifySessionAudioOpenError(path, err)
}

func validateOpenedAudioPath(path string, source audio.AudioSource) error {
	if path == "-" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if closeErr := source.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		return classifySessionAudioOpenError(path, err)
	}
	if !info.IsDir() {
		return nil
	}
	closeErr := source.Close()
	pathErr := fmt.Errorf("path is a directory; provide a .wav, .pcm, or .raw file")
	return &RuntimeAudioInputError{Kind: RuntimeAudioInputUnreadable, Path: path, Err: errors.Join(pathErr, closeErr)}
}

// prepareScheduledAudioInputs loads a finite sequence of audio files for one
// persistent session. Each file becomes one queued user turn; the runtime
// emits its MESSAGE.END boundary after the bytes so the next file is not
// merged into the same provider response.
func prepareScheduledAudioInputs(paths []string) ([]ScheduledAudioInput, error) {
	return prepareScheduledAudioInputsContext(context.Background(), paths)
}

func prepareRuntimeAudioInputs(ctx context.Context, paths []string) ([]ScheduledAudioInput, error) {
	return prepareScheduledAudioInputsContext(ctx, paths)
}

func prepareScheduledAudioInputsContext(ctx context.Context, paths []string) ([]ScheduledAudioInput, error) {
	inputs := make([]ScheduledAudioInput, 0, len(paths))
	for index, path := range paths {
		input := RuntimeAudioInput{Path: path, Present: true}
		if err := validateRuntimeAudioInput(input); err != nil {
			return nil, err
		}
		pcm, sourceRate, err := readRuntimeAudioInputPCM(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("load audio turn %d from %q: %w", index+1, path, err)
		}
		if len(pcm) == 0 {
			return nil, fmt.Errorf("load audio turn %d from %q: %w", index+1, path, emptyRuntimeAudioInput(path))
		}
		inputs = append(inputs, ScheduledAudioInput{
			AfterCompletedTurns: index,
			PCM:                 pcm,
			SourceSampleRate:    sourceRate,
			EndOfTurn:           true,
		})
	}
	return inputs, nil
}

// readRuntimeAudioInputPCM decodes one CLI audio input using the same source
// implementation as --audio-in, but returns its normalized 16 kHz PCM bytes
// for a scheduled persistent-session turn.
func readRuntimeAudioInputPCM(ctx context.Context, input RuntimeAudioInput) (pcm []byte, sourceRate int, runErr error) {
	source, err := openRuntimeAudioInput(input)
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		if closeErr := source.Close(); closeErr != nil {
			runErr = errors.Join(runErr, closeErr)
		}
	}()

	frame := make([]int16, audio.FrameSize)
	var encoded bytes.Buffer
	for {
		clear(frame)
		if err := source.source.ReadFrame(ctx, frame); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, 0, &RuntimeAudioInputError{Kind: RuntimeAudioInputRead, Path: input.Path, Err: err}
		}
		frameBytes := make([]byte, len(frame)*2)
		if err := codec.EncodePCM16Into(frameBytes, frame); err != nil {
			return nil, 0, &RuntimeAudioInputError{Kind: RuntimeAudioInputFormat, Path: input.Path, Err: err}
		}
		_, _ = encoded.Write(frameBytes)
	}
	return encoded.Bytes(), source.sourceRate, nil
}

func classifySessionAudioOpenError(path string, err error) error {
	kind := RuntimeAudioInputUnreadable
	switch {
	case errors.Is(err, audio.ErrUnsupportedFormat):
		kind = RuntimeAudioInputFormat
	case errors.Is(err, os.ErrNotExist):
		kind = RuntimeAudioInputMissing
	case errors.Is(err, audio.ErrNilStream):
		kind = RuntimeAudioInputUnreadable
	}
	return &RuntimeAudioInputError{Kind: kind, Path: path, Err: err}
}

type runtimeAudioSource struct {
	source       audio.AudioSource
	path         string
	sourceRate   int
	providerRate int
	reader       *sessionAudioReader
	ownedInput   *os.File
	// paced marks file-backed finite sources whose frames must be delivered
	// at the encoded real-time rate. Synthetic test sources injected through
	// the RuntimeAudioInput.Source seam are never paced so tests control
	// their own timing.
	paced                 bool
	continuous            bool
	emitBoundaryOnSilence bool
	send                  func(context.Context, []byte) error
	endOfTurn             func(context.Context) error
	runtime               *sessionRuntimeObservationRecorder
	clock                 sharedclock.Source
	once                  sync.Once
	err                   error
}

func (s *runtimeAudioSource) bindProviderRate(rate int) {
	if s != nil {
		s.providerRate = rate
	}
}

func (s *runtimeAudioSource) bindRuntimePlan(plan sessionRuntimePlan) {
	s.bindRuntime(plan.runtime, plan.clockSource)
	s.bindProviderRate(plan.inputAudioSampleRate)
}

func (s *runtimeAudioSource) bindContext(ctx context.Context) {
	if s.reader != nil {
		s.reader.bindContext(ctx)
	}
}

func (s *runtimeAudioSource) bindRuntime(runtime *sessionRuntimeObservationRecorder, source sharedclock.Source) {
	if s != nil {
		s.runtime = runtime
		s.clock = source
	}
}

func (s *runtimeAudioSource) Close() error {
	s.once.Do(func() {
		s.err = s.source.Close()
		if s.ownedInput != nil {
			s.err = errors.Join(s.err, s.reader.Close())
		}
	})
	if s.err == nil {
		return nil
	}
	return &RuntimeAudioInputError{Kind: RuntimeAudioInputClose, Path: s.path, Err: s.err}
}

// sessionAudioReader carries cancellation into readers that can honor it
// without closing the caller-owned stdin. The standard io.Reader contract has
// no cancellation method, so a reader must implement ReadContext or support
// read deadlines once the session context is bound. Calling an arbitrary
// blocking Read in a helper goroutine would leak that goroutine when stdin is
// caller-owned and cannot be closed.
