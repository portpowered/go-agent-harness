package agentruntime

import sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
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

const (
	sessionAudioOutputBufferSize = 256
	sessionAudioWAVHeaderSize    = 44
	sessionAudioWAVMaxDataSize   = uint64(^uint32(0)) - 36
)

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
	plan, err := planSessionRuntime(opts)
	if err != nil {
		return err
	}

	audioOut, err := newSessionAudioOutputForPlan(&plan, path, out, audio.NewLoudnessNormalizer(audio.LoudnessNormalizerConfig{GainDB: VoiceLoudnessGainDB(opts.Voice)}))
	if err != nil {
		return fmt.Errorf("--audio-out %q: %w", path, err)
	}
	defer func() {
		if closeErr := audioOut.close(); closeErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", path, closeErr))
		}
	}()

	if plan.inferencer != nil {
		wirePrompt := ""
		if seed.Present {
			wirePrompt = nextSessionTextWirePrompt()
			plan.loop.Prompt = wirePrompt
		}
		wrapped := newSessionAudioOutputInferencer(plan.inferencer, audioOut, wirePrompt, seed.Value)
		plan.inferencer = wrapped

		sessionOut := out
		if path == "-" {
			sessionOut = io.Discard
		}
		runErr = plan.run(ctx, sessionOut)
		wrapped.wait()
		if outputErr := wrapped.err(); outputErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", path, outputErr))
		}
		return runErr
	}

	sessionOut := out
	if path == "-" {
		sessionOut = io.Discard
	}
	return plan.run(ctx, sessionOut)
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
	plan, err := planSessionRuntime(opts)
	if err != nil {
		return err
	}

	audioOut, err := newSessionAudioOutputForPlan(&plan, path, out, audio.NewLoudnessNormalizer(audio.LoudnessNormalizerConfig{GainDB: VoiceLoudnessGainDB(opts.Voice)}))
	if err != nil {
		return fmt.Errorf("--audio-out %q: %w", path, err)
	}
	defer func() {
		if closeErr := audioOut.close(); closeErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", path, closeErr))
		}
	}()

	if plan.inferencer != nil {
		wirePrompt := ""
		if seed.Present {
			wirePrompt = nextSessionTextWirePrompt()
			plan.loop.Prompt = wirePrompt
		}
		wrapped := newSessionAudioOutputInferencer(plan.inferencer, audioOut, wirePrompt, seed.Value)
		plan.inferencer = wrapped

		sessionOut := out
		if path == "-" {
			sessionOut = io.Discard
		}
		if maxDuration == 0 {
			runErr = plan.run(ctx, sessionOut)
		} else {
			durationCtx, durationErr := prepareSessionDurationArtifacts(ctx)
			if durationErr != nil {
				return durationErr
			}
			runErr = runSessionDurationPlan(durationCtx, sessionOut, plan, maxDuration, realSessionDurationClock{})
		}
		wrapped.wait()
		if outputErr := wrapped.err(); outputErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", path, outputErr))
		}
		return runErr
	}

	sessionOut := out
	if path == "-" {
		sessionOut = io.Discard
	}
	if maxDuration == 0 {
		return plan.run(ctx, sessionOut)
	}
	durationCtx, err := prepareSessionDurationArtifacts(ctx)
	if err != nil {
		return err
	}
	return runSessionDurationPlan(durationCtx, sessionOut, plan, maxDuration, realSessionDurationClock{})
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
func (o *sessionAudioOutput) close() error {
	o.closeOnce.Do(func() {
		o.mu.Lock()
		o.closed = true
		var sinkErr error
		if o.sink != nil {
			sinkErr = o.sink.Close()
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

type sessionAudioOutputInferencer struct {
	inner      messages.SessionInferencer
	output     *sessionAudioOutput
	wirePrompt string
	seedValue  string
	mu         sync.Mutex
	lastErr    error
	connected  *sessionAudioOutputSession
}

func newSessionAudioOutputInferencer(inner messages.SessionInferencer, output *sessionAudioOutput, wirePrompt string, seedValue string) *sessionAudioOutputInferencer {
	return &sessionAudioOutputInferencer{
		inner:      inner,
		output:     output,
		wirePrompt: wirePrompt,
		seedValue:  seedValue,
	}
}

func (i *sessionAudioOutputInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session, err := i.inner.ConnectSession(context.WithoutCancel(ctx))
	if err != nil {
		return nil, err
	}
	wrapped := newSessionAudioOutputSession(ctx, session, i.output, i.recordErr, i.wirePrompt, i.seedValue)
	i.mu.Lock()
	i.connected = wrapped
	i.mu.Unlock()
	return wrapped, nil
}

func (i *sessionAudioOutputInferencer) wait() {
	i.mu.Lock()
	connected := i.connected
	i.mu.Unlock()
	if connected != nil {
		<-connected.done
	}
}

func (i *sessionAudioOutputInferencer) recordErr(err error) {
	if err == nil {
		return
	}
	i.mu.Lock()
	if i.lastErr == nil {
		i.lastErr = err
	}
	i.mu.Unlock()
}

func (i *sessionAudioOutputInferencer) err() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.lastErr
}

type sessionAudioOutputSession struct {
	messages.Session
	ctx            context.Context
	output         *sessionAudioOutput
	record         func(error)
	wirePrompt     string
	seedValue      string
	receive        *messages.TypedBuffer[messages.StreamMessage]
	done           chan struct{}
	once           sync.Once
	closeRequested chan struct{}
	closeOnce      sync.Once
	innerCloseOnce sync.Once
	innerCloseDone chan struct{}
	closeErr       error
	seedMu         sync.Mutex
	seedSent       bool
}

func newSessionAudioOutputSession(ctx context.Context, inner messages.Session, output *sessionAudioOutput, record func(error), wirePrompt string, seedValue string) *sessionAudioOutputSession {
	s := &sessionAudioOutputSession{
		Session:        inner,
		ctx:            ctx,
		output:         output,
		record:         record,
		wirePrompt:     wirePrompt,
		seedValue:      seedValue,
		receive:        messages.NewTypedBuffer[messages.StreamMessage](sessionAudioOutputBufferSize),
		done:           make(chan struct{}),
		closeRequested: make(chan struct{}),
		innerCloseDone: make(chan struct{}),
	}
	go s.forward()
	return s
}
func (s *sessionAudioOutputSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if s.replaceSeed(msg) {
		msg.Value = messages.NewTextDeltaValue(s.seedValue)
	}
	return s.Session.Send(ctx, msg)
}
func (s *sessionAudioOutputSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	return messages.RequestSessionResponse(ctx, s.Session)
}
func (s *sessionAudioOutputSession) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(s.Session)
}
func (s *sessionAudioOutputSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(SessionImageMessageSender)
	return ok && sender.SendMessage(ctx, msg)
}
func (s *sessionAudioOutputSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(SessionImageMessageSenderWithoutResponse)
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}
func (s *sessionAudioOutputSession) SupportsCompleteMessages() bool {
	complete, _ := completeMessageCapabilities(s.Session)
	return complete
}
func (s *sessionAudioOutputSession) SupportsCompleteMessagesWithoutResponse() bool {
	_, withoutResponse := completeMessageCapabilities(s.Session)
	return withoutResponse
}
func (s *sessionAudioOutputSession) replaceSeed(msg messages.StreamMessage) bool {
	if s.wirePrompt == "" || msg.Type != messages.StreamTypeTextDelta {
		return false
	}
	value, ok := msg.Value.(*messages.TextDeltaValue)
	if !ok || value.Content != s.wirePrompt {
		return false
	}

	s.seedMu.Lock()
	defer s.seedMu.Unlock()
	if s.seedSent {
		return false
	}
	s.seedSent = true
	return true
}
func (s *sessionAudioOutputSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *sessionAudioOutputSession) Done() <-chan struct{} {
	return s.done
}

func (s *sessionAudioOutputSession) rtcMedia() (RTCMediaEndpoints, bool) {
	return rtcMediaFromSession(s.Session)
}

func (s *sessionAudioOutputSession) TerminalError() error {
	return terminalSessionError(s.Session)
}

func (s *sessionAudioOutputSession) forward() {
	defer s.once.Do(func() { close(s.done) })
	input := s.Session.Receive()
	for {
		if s.ctx.Err() != nil {
			s.drainAfterCancellation(input)
			return
		}
		if s.forwardNext(input) {
			continue
		}
		if s.ctx.Err() != nil {
			s.drainAfterCancellation(input)
		}
		return
	}
}

func (s *sessionAudioOutputSession) forwardNext(input *messages.TypedBuffer[messages.StreamMessage]) bool {
	select {
	case msg := <-input.Chan():
		return s.forwardMessage(msg)
	case <-s.Session.Done():
		if s.ctx.Err() == nil {
			s.drain(input, s.ctx, false)
		}
	case <-s.closeRequested:
		s.drainAfterCancellation(input)
	case <-s.ctx.Done():
	}
	return false
}

func (s *sessionAudioOutputSession) drain(input *messages.TypedBuffer[messages.StreamMessage], ctx context.Context, retaining bool) bool {
	for {
		msg, ok := input.Read()
		if !ok {
			return true
		}
		if !s.forwardMessageWithContext(ctx, msg, retaining) {
			return false
		}
	}
}

func (s *sessionAudioOutputSession) drainAfterCancellation(input *messages.TypedBuffer[messages.StreamMessage]) {
	retainCtx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), sessionStragglerDrainWallSafety)
	defer cancel()
	terminal := time.NewTimer(sessionStragglerDrainWallSafety)
	defer terminal.Stop()
	for {
		select {
		case msg := <-input.Chan():
			if !s.forwardMessageWithContext(retainCtx, msg, true) {
				return
			}
		case <-s.Session.Done():
			s.closeInner()
			s.drainAfterInnerClose(input, retainCtx)
			return
		case <-terminal.C:
			s.closeInner()
			s.drainAfterInnerClose(input, retainCtx)
			return
		}
	}
}

// Keep the post-barrier quiet tail bounded so a final accepted delta cannot
// race the public PCM oracle after provider close completes.
func (s *sessionAudioOutputSession) drainAfterInnerClose(input *messages.TypedBuffer[messages.StreamMessage], ctx context.Context) {
	select {
	case <-s.innerCloseDone:
	case <-ctx.Done():
		return
	}
	for {
		select {
		case msg := <-input.Chan():
			if !s.forwardMessageWithContext(ctx, msg, true) {
				return
			}
		case <-time.After(sessionStragglerDrainQuietPeriod):
			return
		case <-ctx.Done():
			return
		}
	}
}

func (s *sessionAudioOutputSession) closeInner() {
	s.innerCloseOnce.Do(func() { go s.finishInnerClose() })
}

func (s *sessionAudioOutputSession) finishInnerClose() {
	s.closeErr = s.Session.Close()
	close(s.innerCloseDone)
}

func (s *sessionAudioOutputSession) Close() error {
	s.closeOnce.Do(func() {
		close(s.closeRequested)
		s.closeInner()
		<-s.done
		<-s.innerCloseDone
	})
	return s.closeErr
}

func (s *sessionAudioOutputSession) forwardMessage(msg messages.StreamMessage) bool {
	return s.forwardMessageWithContext(s.ctx, msg, false)
}

func (s *sessionAudioOutputSession) forwardMessageWithContext(ctx context.Context, msg messages.StreamMessage, retaining bool) bool {
	if msg.Type == messages.StreamTypeAudioDelta && assistantAudioDelta(msg) {
		value, ok := msg.Value.(*messages.AudioDeltaValue)
		if !ok {
			s.record(fmt.Errorf("AUDIO.DELTA has unexpected value %T", msg.Value))
			s.closeInner()
			return false
		}
		if err := s.output.writeDelta(ctx, value.Content, msg); err != nil {
			s.record(err)
			s.closeInner()
			return false
		}
	}

	for {
		outcome := s.receive.WriteContext(ctx, msg)
		if outcome.OK() {
			return true
		}
		if outcome.Err != nil {
			return false
		}
		if retaining {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-s.closeRequested:
			return false
		case <-time.After(time.Millisecond):
		}
	}
}

func assistantAudioDelta(msg messages.StreamMessage) bool {
	return msg.Role == "" || msg.Role == messages.RoleAssistant
}
