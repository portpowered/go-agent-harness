package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	durationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/contract"
)

const sessionAudioOutputBufferSize = 256

func joinSessionAudioOutputError(runErr error, path string, outputErr error) error {
	if outputErr == nil || errors.Is(runErr, outputErr) {
		return runErr
	}
	return errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", path, outputErr))
}

func attachSessionAudioOutput(turnRuntime sessionturn.Runtime, plan *sessionRuntimePlan, output *sessionAudioOutput) (sessionturn.AudioOutputRuntime, error) {
	if turnRuntime == nil || plan == nil || plan.inferencer == nil || output == nil {
		return nil, sessionturn.ErrMissingTurnInferencer
	}
	wrapped, err := turnRuntime.AttachAudioOutput(plan.inferencer, output.writeDelta)
	if err != nil {
		return nil, err
	}
	plan.inferencer = wrapped.Inferencer()
	return wrapped, nil
}

func runSessionAudioOutPlan(ctx context.Context, out io.Writer, plan sessionRuntimePlan, path string, seed SessionTextSeed, voice string, maxDuration time.Duration) (runErr error) {
	turnRuntime, err := prepareSessionTurnSeed(ctx, &plan, seed)
	if err != nil {
		return err
	}
	audioOut, err := newSessionAudioOutputForPlan(ctx, &plan, path, out, audio.NewLoudnessNormalizer(audio.LoudnessNormalizerConfig{GainDB: plan.voiceGainDB}))
	if err != nil {
		return fmt.Errorf("--audio-out %q: %w", path, err)
	}
	defer func() {
		if closeErr := audioOut.close(); closeErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", path, closeErr))
		}
	}()
	sessionOut := audioSessionWriter(out, path)
	if plan.inferencer == nil {
		return runSessionAudioPlan(ctx, sessionOut, plan, maxDuration)
	}
	wrapped, err := attachSessionAudioOutput(turnRuntime, &plan, audioOut)
	if err != nil {
		return errors.Join(err, audioOut.close())
	}
	runErr = runSessionAudioPlan(ctx, sessionOut, plan, maxDuration)
	if outputErr := wrapped.Wait(); outputErr != nil {
		runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", path, outputErr))
	}
	return runErr
}

func audioSessionWriter(out io.Writer, path string) io.Writer {
	if path == "-" {
		return io.Discard
	}
	return out
}

func assistantAudioDelta(msg messages.StreamMessage) bool {
	return msg.Role == "" || msg.Role == messages.RoleAssistant
}

func runSessionAudioPlan(ctx context.Context, out io.Writer, plan sessionRuntimePlan, maxDuration time.Duration) error {
	if maxDuration == 0 {
		return plan.run(ctx, out)
	}
	durationCtx, err := durationwire.NewService().PrepareArtifacts(ctx)
	if err != nil {
		return err
	}
	return runSessionDurationPlan(durationCtx, out, plan, maxDuration, nil)
}

// RunSessionWithAudioOut runs a session and writes assistant PCM to path; an empty path preserves normal output and "-" writes raw PCM16.
func RunSessionWithAudioOut(ctx context.Context, out io.Writer, opts SessionRunOptions, path string) (runErr error) {
	return RunSessionWithAudioOutAndTextSeed(ctx, out, opts, path, SessionTextSeed{})
}

// RunSessionWithAudioOutAndTextSeed combines text-seed behavior with assistant audio output.
func RunSessionWithAudioOutAndTextSeed(ctx context.Context, out io.Writer, opts SessionRunOptions, path string, seed SessionTextSeed) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	if path == "" {
		if seed.Present {
			return RunSessionWithTextSeed(ctx, out, opts, seed)
		}
		return RunSession(ctx, out, opts)
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
	plan, err := planSessionRuntimeWithContext(ctx, opts)
	if err != nil {
		return err
	}
	return runSessionAudioOutPlan(ctx, out, plan, path, seed, opts.Voice, 0)
}

// RunSessionWithAudioOutAndTextSeedAndMaxDuration combines assistant audio output with duration control.
func RunSessionWithAudioOutAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, path string, maxDuration time.Duration, seed SessionTextSeed) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	if path == "" {
		return RunSessionWithTextSeedAndMaxDuration(ctx, out, opts, maxDuration, seed)
	}
	if err := sessioncontract.ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	if seed.Present {
		opts.Prompt = seed.Value
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
	plan, err := planSessionRuntimeWithContext(ctx, opts)
	if err != nil {
		return err
	}
	return runSessionAudioOutPlan(ctx, out, plan, path, seed, opts.Voice, maxDuration)
}

type sessionAudioOutput struct {
	sink         audioio.PCM16FileOutput
	audioService audioio.Service
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

func (o *sessionAudioOutput) close() error {
	if o == nil {
		return nil
	}
	o.closeOnce.Do(func() {
		o.mu.Lock()
		o.closed = true
		sink := o.sink
		o.mu.Unlock()
		var err error
		if sink != nil {
			err = sink.Close()
		}
		o.mu.Lock()
		o.closeErr = err
		o.mu.Unlock()
	})
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.closeErr
}

func newSessionAudioOutputForPlan(ctx context.Context, plan *sessionRuntimePlan, path string, out io.Writer, loudness *audio.LoudnessNormalizer) (*sessionAudioOutput, error) {
	service := sessionAudioService(plan.loop.audioService)
	if plan != nil && plan.rtcDeviceRequest.HasOutput() {
		output := &sessionAudioOutput{
			audioService: service,
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
	sink, err := service.OpenPCM16FileOutput(ctx, audioio.PCM16FileOutputRequest{Path: path, Writer: out, SampleRate: plan.outputAudioSampleRate})
	if err != nil {
		return nil, err
	}
	return &sessionAudioOutput{sink: sink, audioService: service, runtime: plan.runtime, loudness: loudness}, nil
}
func (o *sessionAudioOutput) writeDeviceSamples(ctx context.Context, sampleRate int, samples []int16) error {
	if len(samples) == 0 {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return contract.ErrClosed
	}
	if o.sink == nil {
		sink, err := o.audioService.OpenPCM16FileOutput(ctx, audioio.PCM16FileOutputRequest{Path: o.devicePath, Writer: o.deviceWriter, SampleRate: sampleRate})
		if err != nil {
			return err
		}
		o.sink = sink
	}
	return o.sink.WriteSamples(ctx, samples)
}
func (o *sessionAudioOutput) writeDelta(ctx context.Context, content []byte, msg messages.StreamMessage) error {
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
	if o.loudness != nil {
		content = o.loudness.ProcessBytes(content)
	}
	samples, err := codec.DecodePCM16(content)
	if err != nil {
		return pcm16AudioDeltaError(len(content), err)
	}
	o.runtime.audioOutputMessage(content, msg)
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
