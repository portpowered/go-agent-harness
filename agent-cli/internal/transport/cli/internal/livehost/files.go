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
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// FilePorts is the host-admitted finite media bundle. Paths are resolved at
// the CLI edge; the runtime device service receives canonical audio ports and
// owns them for the duration of Open.
type FilePorts struct {
	Input      *runtimeDevices.FileInput
	InputTurns []runtimeDevices.FileInput
	Output     *runtimeDevices.FileOutput

	once     sync.Once
	closeErr error
}

// frameAudioSource keeps the explicit legacy replay compatibility path on the
// canonical fixed-frame AudioSource contract. Ordinary file and finite-turn
// callers retain count-aware source tails.
type frameAudioSource struct {
	source audio.AudioSource
}

func (s *frameAudioSource) ReadFrame(ctx context.Context, buf []int16) error {
	if s == nil || s.source == nil {
		return io.EOF
	}
	return s.source.ReadFrame(ctx, buf)
}

func (s *frameAudioSource) Close() error {
	if s == nil || s.source == nil {
		return nil
	}
	return s.source.Close()
}

// OpenFilePorts opens the explicit finite source and sink paths for one
// invocation. A failed later admission closes every earlier port before
// returning the joined error.
func OpenFilePorts(request serviceSession.Request, out io.Writer, outputRate int) (*FilePorts, error) {
	if !request.AudioInput.Present && len(request.AudioTurns) == 0 && request.AudioOutputPath == "" {
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
	if request.AudioInput.Present || len(request.AudioTurns) > 0 || request.AudioOutputDevicePresent || request.InteractiveDevices {
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
	source, err := audio.NewFileSource(path, input.Stdin)
	if err != nil {
		return nil, 0, fmt.Errorf("--audio-in %q: %w", path, err)
	}
	rate := input.SourceSampleRate
	if rate <= 0 {
		rate = audio.SampleRate
	}
	return source, rate, nil
}

// UseLegacyFrameSource preserves old raw replay captures whose handshake did
// not record an input rate. It is intentionally opt-in and never changes the
// normal count-aware finite source contract.
func UseLegacyFrameSource(input *runtimeDevices.FileInput) {
	if input == nil || input.Source == nil {
		return
	}
	input.Source = &frameAudioSource{source: input.Source}
}

// Close releases every caller-opened source and sink exactly once.
func (p *FilePorts) Close() error {
	if p == nil {
		return nil
	}
	p.once.Do(func() {
		var errs []error
		if p.Output != nil && p.Output.Sink != nil {
			errs = append(errs, p.Output.Sink.Close())
		}
		if p.Input != nil && p.Input.Source != nil {
			errs = append(errs, p.Input.Source.Close())
		}
		for index := range p.InputTurns {
			if p.InputTurns[index].Source != nil {
				errs = append(errs, p.InputTurns[index].Source.Close())
			}
		}
		p.closeErr = errors.Join(errs...)
	})
	return p.closeErr
}
