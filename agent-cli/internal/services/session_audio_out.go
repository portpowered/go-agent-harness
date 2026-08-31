package services

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const sessionAudioOutputBufferSize = 256

const (
	sessionAudioWAVHeaderSize  = 44
	sessionAudioWAVMaxDataSize = uint64(^uint32(0)) - 36
)

// RunSessionWithAudioOut runs a session and writes assistant AUDIO.DELTA
// samples to path as they arrive. An empty path preserves the normal session
// output behavior. A path of "-" writes raw little-endian PCM16 to out.
func RunSessionWithAudioOut(ctx context.Context, out io.Writer, opts SessionRunOptions, path string) (runErr error) {
	return RunSessionWithAudioOutAndTextSeed(ctx, out, opts, path, SessionTextSeed{})
}

// RunSessionWithAudioOutAndTextSeed combines the session text-seed behavior
// with assistant audio output. An empty path preserves the normal session
// output behavior, including the --prompt presence contract.
func RunSessionWithAudioOutAndTextSeed(ctx context.Context, out io.Writer, opts SessionRunOptions, path string, seed SessionTextSeed) (runErr error) {
	var coordinator *SessionCapabilityCoordinator
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

	sink, err := newSessionAudioSink(path, out)
	if err != nil {
		return fmt.Errorf("--audio-out %q: %w", path, err)
	}
	audioOut := &sessionAudioOutput{sink: sink, runtime: plan.runtime}
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

		// A binary stdout stream cannot also carry session text, announcements,
		// or terminal decorations. File output keeps the established text path.
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

// RunSessionWithAudioOutAndTextSeedAndMaxDuration combines assistant audio
// output with the session duration controller. The audio wrapper is placed
// inside the duration admission plan so accepted deltas are written before
// the sink is finalized, including clean duration cutoffs.
func RunSessionWithAudioOutAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, path string, maxDuration time.Duration, seed SessionTextSeed) (runErr error) {
	var coordinator *SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	if path == "" {
		return RunSessionWithTextSeedAndMaxDuration(ctx, out, opts, maxDuration, seed)
	}
	if err := ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	if seed.Present {
		opts.Prompt = seed.Value
	}
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

	sink, err := newSessionAudioSink(path, out)
	if err != nil {
		return fmt.Errorf("--audio-out %q: %w", path, err)
	}
	audioOut := &sessionAudioOutput{sink: sink, runtime: plan.runtime}
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

		// A binary stdout stream cannot also carry session text, announcements,
		// or terminal decorations. File output keeps the established text path.
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
	sink    audio.AudioSink
	runtime *sessionRuntimeObservationRecorder

	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

type sessionAudioSamplesWriter interface {
	WriteSamples(context.Context, []int16) error
}

// newSessionAudioSink keeps the existing frame-oriented audio sink in the
// write path while adding a bounded tail writer and an incremental WAV
// container for session output. The audio package's WAV sink intentionally
// buffers samples for its generic file-sink contract; session output needs a
// streaming container because deltas can be consumed before the session ends.
func newSessionAudioSink(path string, out io.Writer) (audio.AudioSink, error) {
	if path == "-" {
		raw, err := audio.NewFileSink(path, out)
		if err != nil {
			return nil, err
		}
		return &sessionAudioSink{path: path, raw: raw, writer: out}, nil
	}

	// Use the established sink as the format/open preflight so its typed path
	// and format errors remain part of the CLI contract. The session sink below
	// reopens the now-validated target as a raw frame sink so it can stream WAV
	// bytes and preserve a non-frame-aligned tail.
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
		path:   path,
		raw:    raw,
		writer: file,
		file:   file,
		wav:    strings.EqualFold(filepath.Ext(path), ".wav"),
	}
	if sink.wav {
		if err := sink.updateWAVHeaderLocked(); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	return sink, nil
}

// sessionAudioSink is an AudioSink-backed stream. Session deltas use
// WriteSamples so every sample becomes observable before the delta returns;
// the frame-oriented AudioSink API remains available for the established sink
// contract.
// WAV headers are rewritten in place after each write, so the file grows and
// remains readable throughout the session without retaining the response.
type sessionAudioSink struct {
	mu sync.Mutex

	path   string
	raw    audio.AudioSink
	writer io.Writer
	file   *os.File
	wav    bool

	samples  uint64
	closed   bool
	closeErr error
}

var _ audio.AudioSink = (*sessionAudioSink)(nil)
var _ sessionAudioSamplesWriter = (*sessionAudioSink)(nil)

func (s *sessionAudioSink) WriteFrame(ctx context.Context, frame []int16) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return audio.ErrClosed
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
		return audio.ErrClosed
	}
	if s.wav && !sessionAudioWAVSizeFits(s.samples+uint64(len(samples))) {
		return fmt.Errorf("WAV audio output %q exceeds the 32-bit data chunk limit", s.path)
	}
	encoded := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(encoded[index*2:], uint16(sample))
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
	var header [sessionAudioWAVHeaderSize]byte
	dataSize := uint32(s.samples * 2)
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], 36+dataSize)
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16)
	binary.LittleEndian.PutUint16(header[20:22], 1)
	binary.LittleEndian.PutUint16(header[22:24], audio.Channels)
	binary.LittleEndian.PutUint32(header[24:28], audio.SampleRate)
	binary.LittleEndian.PutUint32(header[28:32], audio.SampleRate*2)
	binary.LittleEndian.PutUint16(header[32:34], 2)
	binary.LittleEndian.PutUint16(header[34:36], 16)
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], dataSize)
	if err := writeSessionAudioAll(s.file, header[:]); err != nil {
		return err
	}
	_, err := s.file.Seek(0, io.SeekEnd)
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

func (o *sessionAudioOutput) writeDelta(ctx context.Context, content []byte) error {
	if len(content) == 0 {
		return nil
	}
	if len(content)%2 != 0 {
		return fmt.Errorf("PCM16 audio delta has odd byte length %d", len(content))
	}

	samples := make([]int16, len(content)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(content[index*2:]))
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return audio.ErrClosed
	}
	// Observe the exact validated PCM at the CLI output boundary before the
	// underlying sink is called. This lets a coupled runtime consume the
	// command's own clock-stamped output event rather than inventing metadata
	// around the writer.
	o.runtime.audioOutput(content)
	writer, ok := o.sink.(sessionAudioSamplesWriter)
	if !ok {
		return fmt.Errorf("PCM16 audio output cannot stream a %d-sample delta", len(samples))
	}
	return writer.WriteSamples(ctx, samples)
}

func (o *sessionAudioOutput) close() error {
	o.closeOnce.Do(func() {
		o.mu.Lock()
		o.closed = true
		sinkErr := o.sink.Close()
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

	mu        sync.Mutex
	lastErr   error
	connected *sessionAudioOutputSession
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
	session, err := i.inner.ConnectSession(ctx)
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
	ctx        context.Context
	output     *sessionAudioOutput
	record     func(error)
	wirePrompt string
	seedValue  string

	receive  *messages.TypedBuffer[messages.StreamMessage]
	done     chan struct{}
	once     sync.Once
	seedMu   sync.Mutex
	seedSent bool
}

func newSessionAudioOutputSession(ctx context.Context, inner messages.Session, output *sessionAudioOutput, record func(error), wirePrompt string, seedValue string) *sessionAudioOutputSession {
	s := &sessionAudioOutputSession{
		Session:    inner,
		ctx:        ctx,
		output:     output,
		record:     record,
		wirePrompt: wirePrompt,
		seedValue:  seedValue,
		receive:    messages.NewTypedBuffer[messages.StreamMessage](sessionAudioOutputBufferSize),
		done:       make(chan struct{}),
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

// RequestResponse forwards the optional explicit response capability while
// keeping audio output observation local to inbound provider events.
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
		select {
		case msg := <-input.Chan():
			if !s.forwardMessage(msg) {
				return
			}
		case <-s.Session.Done():
			s.drain(input)
			return
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *sessionAudioOutputSession) drain(input *messages.TypedBuffer[messages.StreamMessage]) {
	for {
		msg, ok := input.Read()
		if !ok {
			return
		}
		if !s.forwardMessage(msg) {
			return
		}
	}
}

func (s *sessionAudioOutputSession) forwardMessage(msg messages.StreamMessage) bool {
	if msg.Type == messages.StreamTypeAudioDelta && assistantAudioDelta(msg) {
		value, ok := msg.Value.(*messages.AudioDeltaValue)
		if !ok {
			s.record(fmt.Errorf("AUDIO.DELTA has unexpected value %T", msg.Value))
			_ = s.Close()
			return false
		}
		if err := s.output.writeDelta(s.ctx, value.Content); err != nil {
			s.record(err)
			_ = s.Close()
			return false
		}
	}

	for {
		if outcome := s.receive.WriteContext(s.ctx, msg); outcome.OK() {
			return true
		} else if outcome.Err != nil {
			return false
		}
		select {
		case <-s.ctx.Done():
			return false
		case <-time.After(time.Millisecond):
		}
	}
}

func assistantAudioDelta(msg messages.StreamMessage) bool {
	// Provider session adapters currently omit Role on server events; an
	// explicitly user/tool/system-authored delta must still be ignored.
	return msg.Role == "" || msg.Role == messages.RoleAssistant
}
