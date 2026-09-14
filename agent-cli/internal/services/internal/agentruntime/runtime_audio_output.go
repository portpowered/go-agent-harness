package agentruntime

import sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"

import audioio "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"

import audioiowire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/contract"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

// RunSessionWithAudioOut runs a session and writes assistant PCM to path; an empty path preserves normal output and "-" writes raw PCM16.
func RunSessionWithAudioOut(ctx context.Context, out io.Writer, opts SessionRunOptions, path string) (runErr error) {
	return RunSessionWithAudioOutAndTextSeed(ctx, out, opts, path, SessionTextSeed{})
}

// RunSessionWithAudioOutAndTextSeed combines text-seed behavior with assistant audio output.
func RunSessionWithAudioOutAndTextSeed(ctx context.Context, out io.Writer, opts SessionRunOptions, path string, seed SessionTextSeed) (runErr error) {
	return runSessionAudioOutput(ctx, out, opts, path, 0, seed, false)
}

// RunSessionWithAudioOutAndTextSeedAndMaxDuration combines assistant audio output with duration control.
func RunSessionWithAudioOutAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, path string, maxDuration time.Duration, seed SessionTextSeed) (runErr error) {
	return runSessionAudioOutput(ctx, out, opts, path, maxDuration, seed, true)
}

func runSessionAudioOutput(ctx context.Context, out io.Writer, opts SessionRunOptions, path string, maxDuration time.Duration, seed SessionTextSeed, withDuration bool) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	if path == "" {
		return runAudioOutputFallback(ctx, out, opts, maxDuration, seed, withDuration)
	}
	if withDuration {
		if err := sessioncontract.ValidateSessionMaxDuration(maxDuration); err != nil {
			return err
		}
	}
	if seed.Present {
		opts.Prompt = seed.Value
		opts.PromptProvided = true
	}
	opts.AudioOutputRequested = true

	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return err
	}
	defer func() { _ = claim.release() }()
	plan, err := planSessionRuntime(opts)
	if err != nil {
		return err
	}

	audioOut, err := newRuntimeAudioOutputForPlan(&plan, path, out, audio.NewLoudnessNormalizer(audio.LoudnessNormalizerConfig{GainDB: sessionVoiceGainDB(opts.Voice)}))
	if err != nil {
		return fmt.Errorf("--audio-out %q: %w", path, err)
	}
	defer func() {
		if closeErr := audioOut.close(); closeErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", path, closeErr))
		}
	}()

	return executeAudioOutputPlan(ctx, out, path, seed, maxDuration, withDuration, plan, audioOut)
}

func runAudioOutputFallback(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration, seed SessionTextSeed, withDuration bool) error {
	if withDuration {
		return RunSessionWithTextSeedAndMaxDuration(ctx, out, opts, maxDuration, seed)
	}
	if seed.Present {
		return RunSessionWithTextSeed(ctx, out, opts, seed)
	}
	return RunSession(ctx, out, opts)
}

func executeAudioOutputPlan(ctx context.Context, out io.Writer, path string, seed SessionTextSeed, maxDuration time.Duration, withDuration bool, plan sessionRuntimePlan, audioOut *runtimeAudioOutput) (runErr error) {
	if plan.inferencer == nil {
		return runAudioOutputPlan(ctx, out, path, plan, maxDuration, withDuration)
	}
	wirePrompt := ""
	if seed.Present {
		wirePrompt = nextSessionTextWirePrompt()
		plan.loop.Prompt = wirePrompt
	}
	wrapped := newRuntimeAudioOutputInferencer(plan.inferencer, audioOut, wirePrompt, seed.Value)
	plan.inferencer = wrapped
	runErr = runAudioOutputPlan(ctx, out, path, plan, maxDuration, withDuration)
	wrapped.wait()
	if outputErr := wrapped.err(); outputErr != nil {
		runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", path, outputErr))
	}
	return runErr
}

func runAudioOutputPlan(ctx context.Context, out io.Writer, path string, plan sessionRuntimePlan, maxDuration time.Duration, withDuration bool) error {
	sessionOut := out
	if path == "-" {
		sessionOut = io.Discard
	}
	if !withDuration || maxDuration == 0 {
		return plan.run(ctx, sessionOut)
	}
	durationCtx, err := prepareSessionDurationArtifacts(ctx)
	if err != nil {
		return err
	}
	return runSessionDurationPlan(durationCtx, sessionOut, plan, maxDuration, realSessionDurationClock{})
}

type runtimeAudioOutput struct {
	sink         audio.AudioSink
	output       audioio.Output
	path         string
	runtime      *sessionRuntimeObservationRecorder
	deviceBound  bool
	devicePath   string
	deviceWriter io.Writer
	loudness     *audio.LoudnessNormalizer
	mu           sync.Mutex
	closed       bool
	closeOnce    sync.Once
	closeErr     error
}

func newRuntimeAudioOutputForPlan(plan *sessionRuntimePlan, path string, out io.Writer, loudness *audio.LoudnessNormalizer) (*runtimeAudioOutput, error) {
	if plan != nil && sessionOutputDeviceSelected(plan.rtcDeviceRequest) {
		output := &runtimeAudioOutput{
			runtime:      plan.runtime,
			deviceBound:  true,
			devicePath:   path,
			deviceWriter: out,
		}
		prior := plan.rtcDeviceRequest.PlaybackSamplesObserver
		plan.rtcDeviceRequest.PlaybackSamplesObserver = func(ctx context.Context, rate int, samples []int16) error {
			var priorErr error
			if prior != nil {
				priorErr = prior(ctx, rate, samples)
			}
			return errors.Join(priorErr, output.writeDeviceSamples(ctx, rate, samples))
		}
		return output, nil
	}
	sink, err := audio.NewFileSinkAtSampleRate(path, out, plan.outputAudioSampleRate)
	if err != nil {
		return nil, err
	}
	output, err := audioiowire.NewService().OpenOutput(context.Background(), audioio.OutputRequest{
		Sink: sink, SinkRate: plan.outputAudioSampleRate, ProviderRate: plan.outputAudioSampleRate,
		Voice: plan.voice, Continuous: true,
	})
	if err != nil {
		_ = sink.Close()
		return nil, err
	}
	return &runtimeAudioOutput{sink: sink, output: output, path: path, runtime: plan.runtime, loudness: loudness}, nil
}
func (o *runtimeAudioOutput) writeDeviceSamples(ctx context.Context, sampleRate int, samples []int16) error {
	if len(samples) == 0 {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return contract.ErrClosed
	}
	if o.sink == nil {
		sink, err := audio.NewFileSinkAtSampleRate(o.devicePath, o.deviceWriter, sampleRate)
		if err != nil {
			return err
		}
		o.sink = sink
	}
	writer, ok := o.sink.(interface {
		WriteSamples(context.Context, []int16) error
	})
	if !ok {
		return fmt.Errorf("PCM16 device audio output cannot stream a %d-sample chunk", len(samples))
	}
	return writer.WriteSamples(ctx, samples)
}
func (o *runtimeAudioOutput) writeDelta(ctx context.Context, content []byte, msg messages.StreamMessage) error {
	if len(content) == 0 {
		return nil
	}
	if o.deviceBound {
		o.runtime.audioOutputMessage(content, msg)
		return nil
	}
	if err := codec.ValidatePCM16(content, codec.MaxPCM16Bytes); err != nil {
		return pcm16AudioDeltaError(len(content), err)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return contract.ErrClosed
	}
	if o.output == nil && o.loudness != nil {
		content = o.loudness.ProcessBytes(content)
	}
	samples, err := codec.DecodePCM16(content)
	if err != nil {
		return pcm16AudioDeltaError(len(content), err)
	}
	o.runtime.audioOutputMessage(content, msg)
	if o.output != nil {
		return o.output.Write(ctx, audio.PCMFrame{Samples: samples})
	}
	writer, ok := o.sink.(interface {
		WriteSamples(context.Context, []int16) error
	})
	if !ok {
		return fmt.Errorf("PCM16 audio output cannot stream a %d-sample delta", len(samples))
	}
	return writer.WriteSamples(ctx, samples)
}
func pcm16AudioDeltaError(byteCount int, err error) error {
	if errors.Is(err, codec.ErrPCM16OddLength) {
		return fmt.Errorf("PCM16 audio delta has odd byte length %d: %w", byteCount, err)
	}
	return fmt.Errorf("PCM16 audio delta: %w", err)
}
func (o *runtimeAudioOutput) close() error {
	o.closeOnce.Do(func() {
		o.mu.Lock()
		o.closed = true
		var sinkErr error
		if o.output != nil {
			sinkErr = o.output.Close()
		} else if o.sink != nil {
			sinkErr = o.sink.Close()
		}
		if errors.Is(sinkErr, wavio.ErrEmptySamples) && o.path != "" && strings.EqualFold(filepath.Ext(o.path), ".wav") {
			if err := os.Remove(o.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				sinkErr = errors.Join(sinkErr, err)
			} else {
				sinkErr = nil
			}
		}
		o.mu.Unlock()
		o.mu.Lock()
		o.closeErr = sinkErr
		o.mu.Unlock()
	})
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.closeErr
}
