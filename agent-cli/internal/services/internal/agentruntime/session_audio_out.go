package agentruntime

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

	sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/contract"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

const (
	sessionAudioOutputBufferSize = 256
	sessionAudioWAVHeaderSize    = 44
	sessionAudioWAVMaxDataSize   = uint64(^uint32(0)) - 36
)

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
	audioOut, err := newSessionAudioOutputForPlan(&plan, path, out, audio.NewLoudnessNormalizer(audio.LoudnessNormalizerConfig{GainDB: VoiceLoudnessGainDB(voice)}))
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
	durationCtx, err := prepareSessionDurationArtifacts(ctx)
	if err != nil {
		return err
	}
	return runSessionDurationPlan(durationCtx, out, plan, maxDuration, realSessionDurationClock{})
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
	sink         audio.AudioSink
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

func newSessionAudioOutputForPlan(plan *sessionRuntimePlan, path string, out io.Writer, loudness *audio.LoudnessNormalizer) (*sessionAudioOutput, error) {
	if plan != nil && plan.rtcDeviceRequest.outputSelected() {
		output := &sessionAudioOutput{
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
	sink, err := newSessionAudioSinkAtRate(path, out, plan.outputAudioSampleRate)
	if err != nil {
		return nil, err
	}
	return &sessionAudioOutput{sink: sink, runtime: plan.runtime, loudness: loudness}, nil
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
		sink, err := newSessionAudioSinkAtRate(o.devicePath, o.deviceWriter, sampleRate)
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
func newSessionAudioSinkAtRate(path string, out io.Writer, sampleRate int) (audio.AudioSink, error) {
	if sampleRate <= 0 {
		return nil, fmt.Errorf("audio output sample rate must be positive; got %d Hz", sampleRate)
	}
	if uint64(sampleRate)*2 > uint64(^uint32(0)) {
		return nil, fmt.Errorf("audio output sample rate %d Hz exceeds WAV header limits", sampleRate)
	}
	if path == "-" {
		raw, err := audio.NewFileSink(path, out)
		if err != nil {
			return nil, err
		}
		return &sessionAudioSink{path: path, raw: raw, writer: out, sampleRate: sampleRate}, nil
	}
	probe, err := audio.NewFileSink(path, out)
	if err != nil {
		return nil, err
	}
	_ = probe.Close()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	raw, err := audio.NewFileSink("-", file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	sink := &sessionAudioSink{
		path:       path,
		raw:        raw,
		writer:     file,
		file:       file,
		wav:        strings.EqualFold(filepath.Ext(path), ".wav"),
		sampleRate: sampleRate,
	}
	if sink.wav {
		if err := sink.updateWAVHeaderLocked(); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	return sink, nil
}

type sessionAudioSink struct {
	mu         sync.Mutex
	path       string
	raw        audio.AudioSink
	writer     io.Writer
	file       *os.File
	wav        bool
	sampleRate int
	samples    uint64
	closed     bool
	closeErr   error
}

var _ audio.AudioSink = (*sessionAudioSink)(nil)

func (s *sessionAudioSink) WriteFrame(ctx context.Context, frame []int16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return contract.ErrClosed
	}
	if s.wav && !sessionAudioWAVSizeFits(s.samples+uint64(len(frame))) {
		return fmt.Errorf("WAV audio output %q exceeds the 32-bit data chunk limit", s.path)
	}
	if err := s.raw.WriteFrame(ctx, frame); err != nil {
		return sessionAudioSinkError(s.path, "write", err)
	}
	s.samples += uint64(len(frame))
	return s.updateWAVHeaderLocked()
}
func (s *sessionAudioSink) WriteSamples(ctx context.Context, samples []int16) error {
	if err := sessionAudioContextError(ctx); err != nil {
		return err
	}
	if len(samples) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return contract.ErrClosed
	}
	if s.wav && !sessionAudioWAVSizeFits(s.samples+uint64(len(samples))) {
		return fmt.Errorf("WAV audio output %q exceeds the 32-bit data chunk limit", s.path)
	}
	encoded := make([]byte, len(samples)*2)
	if err := codec.EncodePCM16Into(encoded, samples); err != nil {
		return sessionAudioSinkError(s.path, "encode", err)
	}
	if err := writeSessionAudioAll(s.writer, encoded); err != nil {
		return sessionAudioSinkError(s.path, "write", err)
	}
	s.samples += uint64(len(samples))
	return s.updateWAVHeaderLocked()
}
func (s *sessionAudioSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closeErr
	}
	s.closed = true
	var closeErr error
	if err := s.raw.Close(); err != nil {
		closeErr = errors.Join(closeErr, sessionAudioSinkError(s.path, "close", err))
	}
	if s.wav && s.samples > 0 {
		closeErr = errors.Join(closeErr, sessionAudioSinkError(s.path, "write", s.updateWAVHeaderLocked()))
	}
	if s.file != nil {
		closeErr = errors.Join(closeErr, sessionAudioSinkError(s.path, "close", s.file.Close()))
		if s.wav && s.samples == 0 {
			if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				closeErr = errors.Join(closeErr, sessionAudioSinkError(s.path, "remove", err))
			}
		}
	}
	s.closeErr = closeErr
	return closeErr
}
func sessionAudioSinkError(path, operation string, err error) error {
	if err == nil {
		return nil
	}
	var streamErr *audio.StreamError
	if errors.As(err, &streamErr) {
		copyErr := *streamErr
		copyErr.Operation = operation
		copyErr.Path = path
		copyErr.Format = sessionAudioFormat(path)
		return &copyErr
	}
	return &audio.StreamError{Operation: operation, Path: path, Format: sessionAudioFormat(path), Err: err}
}
func sessionAudioFormat(path string) string {
	if strings.EqualFold(filepath.Ext(path), ".wav") {
		return "wav"
	}
	return "raw PCM16"
}
func (s *sessionAudioSink) updateWAVHeaderLocked() error {
	if !s.wav {
		return nil
	}
	if !sessionAudioWAVSizeFits(s.samples) {
		return fmt.Errorf("WAV audio output %q exceeds the 32-bit data chunk limit", s.path)
	}
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	header, err := wavio.PCM16Header(s.sampleRate, s.samples*2)
	if err != nil {
		return err
	}
	if err := writeSessionAudioAll(s.file, header[:]); err != nil {
		return err
	}
	_, err = s.file.Seek(0, io.SeekEnd)
	return err
}
func sessionAudioWAVSizeFits(samples uint64) bool {
	return samples <= sessionAudioWAVMaxDataSize/2
}
func sessionAudioContextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
func writeSessionAudioAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if written < 0 || written > len(data) {
			return fmt.Errorf("%w: writer returned invalid byte count %d", io.ErrShortWrite, written)
		}
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
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
