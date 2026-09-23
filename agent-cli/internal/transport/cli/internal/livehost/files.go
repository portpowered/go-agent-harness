package livehost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	devicegateway "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

// FilePorts is the host-admitted finite media bundle. Paths are resolved at
// the CLI edge; the runtime device service receives canonical audio ports and
// owns them for the duration of Open.
type FilePorts struct {
	Input              *runtimeDevices.FileInput
	InputTurns         []runtimeDevices.FileInput
	InputInterruptions []runtimeDevices.FileInput
	Output             *runtimeDevices.FileOutput

	once     sync.Once
	closeErr error
}

// interruptibleAudioSource owns a process-local duplicate of stdin. The
// generic AudioSource contract cannot cancel an in-flight io.ReadFull call,
// while the live runtime must close and join a capture worker after the
// provider ends a session. Closing the duplicate first wakes that read without
// closing the caller-owned command descriptor.
type interruptibleAudioSource struct {
	source audio.AudioSource
	input  *os.File

	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

func (s *interruptibleAudioSource) ReadFrame(ctx context.Context, buf []int16) error {
	if s == nil || s.source == nil {
		return io.EOF
	}
	err := s.source.ReadFrame(ctx, buf)
	if s.isClosed() && errors.Is(err, os.ErrClosed) {
		return context.Canceled
	}
	return err
}

func (s *interruptibleAudioSource) ReadSamples(ctx context.Context, buf []int16) (int, error) {
	if s == nil || s.source == nil {
		return 0, io.EOF
	}
	countSource, ok := s.source.(audio.SampleSource)
	if !ok {
		return 0, fmt.Errorf("interruptible audio source does not support sample-count reads")
	}
	count, err := countSource.ReadSamples(ctx, buf)
	if s.isClosed() && errors.Is(err, os.ErrClosed) {
		return count, context.Canceled
	}
	return count, err
}

func (s *interruptibleAudioSource) isClosed() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *interruptibleAudioSource) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		// Close the duplicate before asking FileSource to close. FileSource
		// serializes its state around the underlying read, so reversing this
		// order would wait forever for a blocked stdin read.
		if s.input != nil {
			s.closeErr = s.input.Close()
		}
		if s.source != nil {
			s.closeErr = errors.Join(s.closeErr, s.source.Close())
		}
	})
	return s.closeErr
}

// OpenFilePorts opens the explicit finite source and sink paths for one
// invocation. A failed later admission closes every earlier port before
// returning the joined error.
func OpenFilePorts(request serviceSession.Request, out io.Writer, outputRate int) (*FilePorts, error) {
	if !request.AudioInput.Present && len(request.AudioTurns) == 0 && len(request.AudioInterrupts) == 0 && request.AudioOutputPath == "" {
		return nil, nil
	}
	ports := &FilePorts{}
	if request.AudioInput.Present {
		source, rate, err := openAudioInput(request.AudioInput)
		if err != nil {
			return nil, err
		}
		ports.Input = &runtimeDevices.FileInput{Source: source, SampleRate: rate, Pace: request.AudioInput.Path != "-", Continuous: request.AudioInput.Path == "-"}
	}
	for index, path := range request.AudioTurns {
		source, rate, err := openAudioInput(serviceSession.AudioInput{Path: path})
		if err != nil {
			return nil, errors.Join(fmt.Errorf("--audio-in-turn %d %q: %w", index+1, path, err), ports.Close())
		}
		ports.InputTurns = append(ports.InputTurns, runtimeDevices.FileInput{Source: source, SampleRate: rate, Pace: path != "-", Continuous: path == "-"})
	}
	for index, path := range request.AudioInterrupts {
		source, rate, err := openAudioInput(serviceSession.AudioInput{Path: path})
		if err != nil {
			return nil, errors.Join(fmt.Errorf("--audio-interrupt %d %q: %w", index+1, path, err), ports.Close())
		}
		ports.InputInterruptions = append(ports.InputInterruptions, runtimeDevices.FileInput{Source: source, SampleRate: rate, Pace: true})
	}
	if request.AudioOutputPath != "" {
		sink, err := openAudioOutput(request, out, outputRate)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("--audio-out %q: %w", request.AudioOutputPath, err), ports.Close())
		}
		ports.Output = &runtimeDevices.FileOutput{Sink: sink, SampleRate: outputRate, Continuous: request.AudioOutputPath == "-"}
	}
	return ports, nil
}

func openAudioOutput(request serviceSession.Request, out io.Writer, outputRate int) (audio.AudioSink, error) {
	if outputRate <= 0 {
		outputRate = audio.SampleRate
	}
	if request.AudioInput.Present || len(request.AudioTurns) > 0 || len(request.AudioInterrupts) > 0 || request.AudioOutputDevicePresent || request.InteractiveDevices {
		return newNegotiatedFileSink(request.AudioOutputPath, out, outputRate)
	}
	return audio.NewFileSinkAtSampleRate(request.AudioOutputPath, out, outputRate)
}

func openAudioInput(input serviceSession.AudioInput) (audio.AudioSource, int, error) {
	path := input.Path
	if strings.EqualFold(filepath.Ext(path), ".wav") && path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return nil, 0, fmt.Errorf("--audio-in %q: %w", path, err)
		}
		source, err := audio.NewWAVSource(path, file)
		if err != nil {
			return nil, 0, errors.Join(fmt.Errorf("--audio-in %q: %w", path, err), file.Close())
		}
		return source, source.SampleRate(), nil
	}
	source, interruptibleInput, err := openFileAudioSource(input)
	if err != nil {
		return nil, 0, err
	}
	rate := input.SourceSampleRate
	if rate <= 0 {
		rate = audio.SampleRate
	}
	if interruptibleInput != nil {
		return &interruptibleAudioSource{source: source, input: interruptibleInput}, rate, nil
	}
	return source, rate, nil
}

func openFileAudioSource(input serviceSession.AudioInput) (audio.AudioSource, *os.File, error) {
	stdin := input.Stdin
	var interruptibleInput *os.File
	if input.Path == "-" && input.CloseStdinOnCancel {
		file, ok := stdin.(*os.File)
		if ok {
			var err error
			interruptibleInput, err = devicegateway.OpenInterruptibleInput(file)
			if err != nil {
				return nil, nil, fmt.Errorf("--audio-in %q: %w", input.Path, err)
			}
			stdin = interruptibleInput
		}
	}
	source, err := audio.NewFileSource(input.Path, stdin)
	if err != nil {
		if interruptibleInput != nil {
			if closeErr := interruptibleInput.Close(); closeErr != nil {
				return nil, nil, errors.Join(fmt.Errorf("--audio-in %q: %w", input.Path, err), fmt.Errorf("close interruptible input: %w", closeErr))
			}
		}
		return nil, nil, fmt.Errorf("--audio-in %q: %w", input.Path, err)
	}
	if interruptibleInput == nil {
		return source, nil, nil
	}
	return source, interruptibleInput, nil
}

// Close releases every caller-opened source and sink exactly once.
func (p *FilePorts) Close() error {
	if p == nil {
		return nil
	}
	p.once.Do(func() { p.closeErr = closeFilePortSources(p) })
	return p.closeErr
}

func closeFilePortSources(ports *FilePorts) error {
	var errs []error
	if ports.Output != nil {
		errs = appendFilePortClose(errs, ports.Output.Sink)
	}
	if ports.Input != nil {
		errs = appendFilePortClose(errs, ports.Input.Source)
	}
	for index := range ports.InputTurns {
		errs = appendFilePortClose(errs, ports.InputTurns[index].Source)
	}
	for index := range ports.InputInterruptions {
		errs = appendFilePortClose(errs, ports.InputInterruptions[index].Source)
	}
	return errors.Join(errs...)
}

func appendFilePortClose(errs []error, closer interface{ Close() error }) []error {
	if closer == nil {
		return errs
	}
	return append(errs, closer.Close())
}

func selectFileDevices(physical, finite runtimeDevices.Service, deviceRequest runtimeDevices.Request, filePorts *FilePorts) (runtimeDevices.Service, runtimeDevices.Request) {
	if filePorts == nil {
		return physical, deviceRequest
	}
	if filePorts.Input != nil {
		deviceRequest.CaptureEnabled = false
	}
	if deviceRequest.CaptureEnabled || deviceRequest.PlaybackEnabled {
		return physical, deviceRequest
	}
	if filePorts.Input != nil || filePorts.Output != nil {
		deviceRequest.CaptureEnabled = filePorts.Input != nil
		deviceRequest.PlaybackEnabled = filePorts.Output != nil
		return finite, deviceRequest
	}
	if len(filePorts.InputTurns) > 0 || len(filePorts.InputInterruptions) > 0 {
		return finite, deviceRequest
	}
	return nil, deviceRequest
}

func outputWriter(request serviceSession.Request, out io.Writer) io.Writer {
	if request.AudioOutputPath == "-" {
		return io.Discard
	}
	return out
}

func devicesRequest(request serviceSession.Request, liveRequest runtimeSession.LiveRequest) runtimeDevices.Request {
	sampleRate := liveRequest.InputAudioSampleRate
	if sampleRate <= 0 {
		sampleRate = liveRequest.OutputAudioSampleRate
	}
	if sampleRate <= 0 {
		sampleRate = 24000
	}
	return runtimeDevices.Request{
		InputDevice: normalizeDeviceSelector(request.AudioInputDevice), OutputDevice: normalizeDeviceSelector(request.AudioOutputDevice),
		RemoteEndpoint:  request.AudioDeviceServer,
		CaptureEnabled:  request.InteractiveDevices || request.AudioInputDevicePresent,
		PlaybackEnabled: request.InteractiveDevices || request.AudioOutputDevicePresent,
		SampleRate:      sampleRate, Channels: audio.Channels, PlaybackProfile: "voice",
		HoldToneConfig: request.HoldToneConfig,
	}
}

// normalizeDeviceSelector translates the CLI's historical "default" spelling
// to the service contract's empty-selector default. Device IDs remain opaque;
// only this stateless compatibility spelling is handled at the host edge.
func normalizeDeviceSelector(selector string) string {
	selector = strings.TrimSpace(selector)
	if strings.EqualFold(selector, "default") {
		return ""
	}
	return selector
}

func applyFileSchedulers(filePorts *FilePorts, scheduler clock.Scheduler) {
	if filePorts == nil {
		return
	}
	if filePorts.Input != nil {
		filePorts.Input.Scheduler = scheduler
	}
	for index := range filePorts.InputTurns {
		filePorts.InputTurns[index].Scheduler = scheduler
	}
	for index := range filePorts.InputInterruptions {
		filePorts.InputInterruptions[index].Scheduler = scheduler
	}
}

func audioTurnAdmission(request serviceSession.Request) runtimeSession.AudioTurnAdmission {
	if request.AudioInTurnBarge {
		return runtimeSession.AudioTurnAdmissionBarge
	}
	return runtimeSession.AudioTurnAdmissionCompletionGated
}

func captureTurns(filePorts *FilePorts) []runtimeDevices.FileInput {
	if filePorts == nil {
		return nil
	}
	return append([]runtimeDevices.FileInput(nil), filePorts.InputTurns...)
}

func captureInterruptions(filePorts *FilePorts) []runtimeDevices.FileInput {
	if filePorts == nil {
		return nil
	}
	return append([]runtimeDevices.FileInput(nil), filePorts.InputInterruptions...)
}

func captureCompleteControls(request serviceSession.Request, custom func(serviceSession.Request) []runtimeSession.LiveControl) []runtimeSession.LiveControl {
	if custom != nil {
		return custom(request)
	}
	if !request.AudioInput.Present && len(request.AudioTurns) == 0 && len(request.AudioInterrupts) == 0 {
		return nil
	}
	return []runtimeSession.LiveControl{{Kind: runtimeSession.LiveControlAudioCommit}}
}
