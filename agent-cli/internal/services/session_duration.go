package services

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

// SessionMaxDurationReason is the stable terminal reason for a planned
// duration cutoff.
const SessionMaxDurationReason messages.TerminalReason = "max_duration"

// ErrInvalidSessionMaxDuration identifies a negative --max-duration value.
var ErrInvalidSessionMaxDuration = errors.New("invalid session max duration")

// SessionMaxDurationError describes a duration that cannot be used as a
// session bound. It is returned before runtime planning or session startup.
type SessionMaxDurationError struct {
	Duration time.Duration
}

// InvalidSessionDurationError is retained as a descriptive alias for callers
// that use the validation error by its general duration name.
type InvalidSessionDurationError = SessionMaxDurationError

// Error returns an actionable validation message for the CLI.
func (e *SessionMaxDurationError) Error() string {
	if e == nil {
		return ErrInvalidSessionMaxDuration.Error()
	}
	return fmt.Sprintf("--max-duration must be non-negative, got %s", e.Duration)
}

// Unwrap preserves a stable errors.Is identity for duration validation.
func (e *SessionMaxDurationError) Unwrap() error {
	return ErrInvalidSessionMaxDuration
}

// ValidateSessionMaxDuration validates the optional session duration before
// any provider, session, or output resource is planned.
func ValidateSessionMaxDuration(duration time.Duration) error {
	if duration < 0 {
		return &SessionMaxDurationError{Duration: duration}
	}
	return nil
}

// SessionDurationTimer is the timer contract owned by the session duration
// controller. The small interface gives deterministic tests a clock seam while
// ensuring the real timer is stopped on every termination path.
type SessionDurationTimer interface {
	C() <-chan time.Time
	Stop() bool
}

// SessionDurationClock creates one timer for a positive session bound.
type SessionDurationClock interface {
	NewTimer(time.Duration) SessionDurationTimer
}

// SessionDurationArtifactLifecycle receives the stream messages that crossed
// the duration admission boundary and owns their finalization. Callers can
// attach the existing audio/transcript resources through the context without
// making the CLI responsible for session cleanup.
type SessionDurationArtifactLifecycle interface {
	Accept(messages.StreamMessage) error
	Flush() error
	Close() error
}

type sessionDurationArtifactsContextKey struct{}

// SessionDurationArtifactPaths identifies the production-owned files that a
// positive duration run should finalize. The CLI supplies these paths while
// the services layer retains ownership of opening, flushing, and closing the
// resources.
type SessionDurationArtifactPaths struct {
	AudioPath      string
	TranscriptPath string
}

type sessionDurationArtifactPathsContextKey struct{}

// WithSessionDurationArtifacts attaches production-owned output resources to a
// duration run. The duration controller flushes and closes them after the
// accepted loop output has drained, including the synthesized terminal record.
func WithSessionDurationArtifacts(ctx context.Context, artifacts SessionDurationArtifactLifecycle) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sessionDurationArtifactsContextKey{}, artifacts)
}

func sessionDurationArtifactsFromContext(ctx context.Context) SessionDurationArtifactLifecycle {
	if ctx == nil {
		return nil
	}
	artifacts, _ := ctx.Value(sessionDurationArtifactsContextKey{}).(SessionDurationArtifactLifecycle)
	return artifacts
}

// WithSessionDurationArtifactPaths asks the duration entry point to create
// the production-owned WAV and JSONL resources after validation and runtime
// planning. Existing lifecycle values take precedence, which keeps injected
// sinks useful for tests and other callers that already own their resources.
func WithSessionDurationArtifactPaths(ctx context.Context, paths SessionDurationArtifactPaths) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sessionDurationArtifactPathsContextKey{}, paths)
}

func sessionDurationArtifactPathsFromContext(ctx context.Context) (SessionDurationArtifactPaths, bool) {
	if ctx == nil {
		return SessionDurationArtifactPaths{}, false
	}
	paths, ok := ctx.Value(sessionDurationArtifactPathsContextKey{}).(SessionDurationArtifactPaths)
	return paths, ok
}

func prepareSessionDurationArtifacts(ctx context.Context) (context.Context, error) {
	if sessionDurationArtifactsFromContext(ctx) != nil {
		return ctx, nil
	}
	paths, ok := sessionDurationArtifactPathsFromContext(ctx)
	if !ok || (paths.AudioPath == "" && paths.TranscriptPath == "") {
		return ctx, nil
	}
	if paths.AudioPath == "" || paths.TranscriptPath == "" {
		return nil, errors.New("session duration artifacts require both audio and transcript paths")
	}
	artifacts, err := NewSessionDurationArtifactSet(paths.AudioPath, paths.TranscriptPath)
	if err != nil {
		return nil, fmt.Errorf("open session duration artifacts: %w", err)
	}
	return WithSessionDurationArtifacts(ctx, artifacts), nil
}

// SessionDurationAudioSink accepts PCM16 samples and owns their final WAV
// encoding. It deliberately accepts a partial final frame so a cutoff between
// audio frames remains an exact, playable artifact.
type SessionDurationAudioSink interface {
	WriteSamples([]int16) error
	Flush() error
	Close() error
}

// SessionDurationTranscriptSink is the lifecycle subset implemented by the
// shared transcript.Writer.
type SessionDurationTranscriptSink interface {
	Write(transcript.Record) error
	Flush() error
	Close() error
}

// SessionDurationArtifactSet adapts the shared audio and transcript primitives
// to the ordered duration finalization boundary.
type SessionDurationArtifactSet struct {
	audio      SessionDurationAudioSink
	transcript SessionDurationTranscriptSink

	mu       sync.Mutex
	sequence uint64
	closed   bool
	closeErr error
}

// NewSessionDurationArtifactSet opens the WAV and JSONL resources used by a
// duration run. The returned set owns both resources and closes them exactly
// once when the duration controller finishes.
func NewSessionDurationArtifactSet(audioPath, transcriptPath string) (*SessionDurationArtifactSet, error) {
	audioSink, err := newSessionDurationWAVSink(audioPath)
	if err != nil {
		return nil, err
	}
	transcriptSink, err := newSessionDurationTranscriptSink(transcriptPath)
	if err != nil {
		_ = audioSink.Close()
		return nil, err
	}
	return NewSessionDurationArtifactSetWithSinks(audioSink, transcriptSink), nil
}

func newSessionDurationTranscriptSink(path string) (*transcript.Writer, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open duration transcript %q: %w", path, err)
	}
	writer, err := transcript.NewWriterOn(file)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("create duration transcript %q: %w", path, err)
	}
	return writer, nil
}

// NewSessionDurationArtifactSetWithSinks builds the same production lifecycle
// around caller-provided resources. It is useful for non-filesystem sinks and
// for preserving underlying flush/close error identity.
func NewSessionDurationArtifactSetWithSinks(audioSink SessionDurationAudioSink, transcriptSink SessionDurationTranscriptSink) *SessionDurationArtifactSet {
	return &SessionDurationArtifactSet{audio: audioSink, transcript: transcriptSink}
}

type sessionDurationTranscriptEvent struct {
	Type  messages.StreamMessageType  `json:"type"`
	Role  messages.Role               `json:"role,omitempty"`
	Value messages.StreamMessageValue `json:"value,omitempty"`
}

func (a *SessionDurationArtifactSet) Accept(msg messages.StreamMessage) error {
	if a == nil {
		return nil
	}
	// LOOP.END is an internal agent-loop lifecycle marker emitted after the
	// session terminal record; it is not provider output and must not trail the
	// finalized transcript's terminal record.
	if msg.Type == messages.StreamTypeLoopEnd {
		return nil
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return errors.New("session duration artifacts are closed")
	}

	if audioValue, ok := msg.Value.(*messages.AudioDeltaValue); ok && a.audio != nil {
		samples, err := sessionDurationPCM16Samples(audioValue.Content)
		if err != nil {
			return err
		}
		if err := a.audio.WriteSamples(samples); err != nil {
			return fmt.Errorf("write duration audio: %w", err)
		}
	}

	if a.transcript == nil {
		return nil
	}
	payload, err := json.Marshal(sessionDurationTranscriptEvent{
		Type:  msg.Type,
		Role:  msg.Role,
		Value: msg.Value,
	})
	if err != nil {
		return fmt.Errorf("encode duration transcript event: %w", err)
	}
	sequence := a.sequence + 1
	record := transcript.NewRecord(
		sequence,
		time.Unix(0, int64(sequence)),
		transcript.PeerAgent,
		transcript.DirectionIn,
		transcript.StreamWebSocket,
		payload,
	)
	if err := a.transcript.Write(record); err != nil {
		return fmt.Errorf("write duration transcript: %w", err)
	}
	a.sequence = sequence
	return nil
}

func (a *SessionDurationArtifactSet) Flush() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return a.closeErr
	}
	var flushErrs []error
	if a.audio != nil {
		if err := a.audio.Flush(); err != nil {
			flushErrs = append(flushErrs, fmt.Errorf("flush duration audio: %w", err))
		}
	}
	if a.transcript != nil {
		if err := a.transcript.Flush(); err != nil {
			flushErrs = append(flushErrs, fmt.Errorf("flush duration transcript: %w", err))
		}
	}
	return errors.Join(flushErrs...)
}

func (a *SessionDurationArtifactSet) Close() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return a.closeErr
	}
	a.closed = true

	var closeErrs []error
	if a.audio != nil {
		if err := a.audio.Close(); err != nil {
			closeErrs = append(closeErrs, fmt.Errorf("close duration audio: %w", err))
		}
	}
	if a.transcript != nil {
		if err := a.transcript.Close(); err != nil {
			closeErrs = append(closeErrs, fmt.Errorf("close duration transcript: %w", err))
		}
	}
	a.closeErr = errors.Join(closeErrs...)
	return a.closeErr
}

func sessionDurationPCM16Samples(content []byte) ([]int16, error) {
	if len(content)%2 != 0 {
		return nil, fmt.Errorf("duration audio has odd PCM16 byte count %d", len(content))
	}
	samples := make([]int16, len(content)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(content[index*2:]))
	}
	return samples, nil
}

type sessionDurationWAVSink struct {
	mu       sync.Mutex
	path     string
	file     *os.File
	samples  []int16
	closed   bool
	closeErr error
}

func newSessionDurationWAVSink(path string) (*sessionDurationWAVSink, error) {
	if path == "" {
		return nil, errors.New("duration audio path is empty")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open duration audio %q: %w", path, err)
	}
	return &sessionDurationWAVSink{path: path, file: file}, nil
}

func (s *sessionDurationWAVSink) WriteSamples(samples []int16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("duration audio sink is closed")
	}
	s.samples = append(s.samples, samples...)
	return nil
}

func (s *sessionDurationWAVSink) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closeErr
	}
	if s.file == nil {
		return nil
	}
	return s.file.Sync()
}

func (s *sessionDurationWAVSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closeErr
	}
	s.closed = true
	var closeErrs []error
	if s.file != nil {
		if err := writeSessionDurationWAV(s.file, s.samples); err != nil {
			closeErrs = append(closeErrs, fmt.Errorf("write duration audio %q: %w", s.path, err))
		} else if err := s.file.Sync(); err != nil {
			closeErrs = append(closeErrs, fmt.Errorf("flush duration audio %q: %w", s.path, err))
		}
		if err := s.file.Close(); err != nil {
			closeErrs = append(closeErrs, fmt.Errorf("close duration audio %q: %w", s.path, err))
		}
	}
	s.closeErr = errors.Join(closeErrs...)
	return s.closeErr
}

// writeSessionDurationWAV preserves a valid, playable WAV container even when
// the logical deadline precedes the first audio delta. wavio.Write rejects an
// empty sample slice because it is normally used for non-empty recordings;
// planned duration cutoffs still need a canonical zero-sample artifact.
func writeSessionDurationWAV(w io.Writer, samples []int16) error {
	if len(samples) > 0 {
		return wavio.Write(w, wavio.Rate16kHz, samples)
	}

	var header [44]byte
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], 36)
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16)
	binary.LittleEndian.PutUint16(header[20:22], 1)
	binary.LittleEndian.PutUint16(header[22:24], 1)
	binary.LittleEndian.PutUint32(header[24:28], wavio.Rate16kHz)
	binary.LittleEndian.PutUint32(header[28:32], wavio.Rate16kHz*2)
	binary.LittleEndian.PutUint16(header[32:34], 2)
	binary.LittleEndian.PutUint16(header[34:36], 16)
	copy(header[36:40], "data")
	return writeSessionDurationBytes(w, header[:])
}

func writeSessionDurationBytes(w io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := w.Write(data)
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

type realSessionDurationClock struct{}

func (realSessionDurationClock) NewTimer(duration time.Duration) SessionDurationTimer {
	return realSessionDurationTimer{timer: time.NewTimer(duration)}
}

type realSessionDurationTimer struct {
	timer *time.Timer
}

func (t realSessionDurationTimer) C() <-chan time.Time {
	return t.timer.C
}

func (t realSessionDurationTimer) Stop() bool {
	return t.timer.Stop()
}

const sessionDurationAdmissionBufferCapacity = 1024

// sessionDurationAdmission is the single admission boundary for provider
// events. Closing it prevents a provider event from entering the loop after
// the logical deadline while preserving events accepted before the close.
type sessionDurationAdmission struct {
	mu     sync.Mutex
	closed bool
	done   chan struct{}
	once   sync.Once
}

func newSessionDurationAdmission() *sessionDurationAdmission {
	return &sessionDurationAdmission{done: make(chan struct{})}
}

func (a *sessionDurationAdmission) close() {
	a.closeWithDrain(nil, nil)
}

func (a *sessionDurationAdmission) closeWithDrain(receive, source *messages.TypedBuffer[messages.StreamMessage]) {
	a.once.Do(func() {
		a.mu.Lock()
		if receive != nil && source != nil {
			for {
				msg, ok := source.Read()
				if !ok || !receive.Write(context.Background(), msg) {
					break
				}
			}
		}
		a.closed = true
		a.mu.Unlock()
		close(a.done)
	})
}

func (a *sessionDurationAdmission) admit(receive *messages.TypedBuffer[messages.StreamMessage], msg messages.StreamMessage) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return false
	}
	return receive.Write(context.Background(), msg)
}

// sessionDurationAdmissionInferencer inserts the admission boundary between
// the provider session and the agent loop. The public Session interface exposes
// a concrete receive buffer, so the wrapper forwards through its own buffer and
// can stop admitting provider events without changing the shared interface.
type sessionDurationAdmissionInferencer struct {
	inner      messages.SessionInferencer
	admission  *sessionDurationAdmission
	mu         sync.Mutex
	runtimeErr error
	closeErr   error
	connected  bool
	session    *sessionDurationAdmissionSession
	closeDone  chan struct{}
	closeOnce  sync.Once
}

func (i *sessionDurationAdmissionInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session, err := i.inner.ConnectSession(ctx)
	if err != nil {
		i.mu.Lock()
		i.runtimeErr = err
		i.mu.Unlock()
		return nil, err
	}
	i.mu.Lock()
	i.connected = true
	wrapped := newSessionDurationAdmissionSession(ctx, session, i.admission, i.recordCloseError)
	i.session = wrapped
	i.mu.Unlock()
	return wrapped, nil
}

func (i *sessionDurationAdmissionInferencer) recordCloseError(err error) {
	i.mu.Lock()
	i.closeErr = err
	i.mu.Unlock()
	if i.closeDone != nil {
		i.closeOnce.Do(func() { close(i.closeDone) })
	}
}

func (i *sessionDurationAdmissionInferencer) closeError() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.closeErr
}

func (i *sessionDurationAdmissionInferencer) runtimeError() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.runtimeErr
}

func (i *sessionDurationAdmissionInferencer) waitForClose() {
	i.mu.Lock()
	connected := i.connected
	closeDone := i.closeDone
	i.mu.Unlock()
	if connected && closeDone != nil {
		<-closeDone
	}
}

func (i *sessionDurationAdmissionInferencer) closeAdmission() {
	i.mu.Lock()
	session := i.session
	i.mu.Unlock()
	if session != nil {
		session.closeAdmission()
		return
	}
	i.admission.close()
}

type sessionDurationAdmissionSession struct {
	inner     messages.Session
	admission *sessionDurationAdmission
	receive   *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	doneOnce  sync.Once
	closeOnce sync.Once
	closeMu   sync.Mutex
	closeErr  error
	onClose   func(error)
}

func newSessionDurationAdmissionSession(ctx context.Context, inner messages.Session, admission *sessionDurationAdmission, onClose func(error)) *sessionDurationAdmissionSession {
	s := &sessionDurationAdmissionSession{
		inner:     inner,
		admission: admission,
		receive:   messages.NewTypedBuffer[messages.StreamMessage](sessionDurationAdmissionBufferCapacity),
		done:      make(chan struct{}),
		onClose:   onClose,
	}
	go s.forward(ctx)
	return s
}

func (s *sessionDurationAdmissionSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.inner.Send(ctx, msg)
}

func (s *sessionDurationAdmissionSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *sessionDurationAdmissionSession) Done() <-chan struct{} {
	return s.done
}

func (s *sessionDurationAdmissionSession) rtcMedia() (RTCMediaEndpoints, bool) {
	return rtcMediaFromSession(s.inner)
}

func (s *sessionDurationAdmissionSession) Close() error {
	s.closeOnce.Do(func() {
		s.closeAdmission()
		err := s.inner.Close()
		s.closeMu.Lock()
		s.closeErr = err
		s.closeMu.Unlock()
		if s.onClose != nil {
			s.onClose(err)
		}
	})
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	return s.closeErr
}

func (s *sessionDurationAdmissionSession) closeAdmission() {
	s.admission.closeWithDrain(s.receive, s.inner.Receive())
}

func (s *sessionDurationAdmissionSession) forward(ctx context.Context) {
	source := s.inner.Receive()
	sourceCh := source.Chan()
	admissionDone := s.admission.done
	for {
		select {
		case <-s.inner.Done():
			s.drainSource(source)
			s.closeDone()
			return
		case <-ctx.Done():
			s.closeDone()
			return
		case <-admissionDone:
			// The deadline closes admission, but the provider session must stay
			// alive long enough to receive the graceful close request. Disable
			// source forwarding and wait for the provider/session lifecycle.
			admissionDone = nil
			sourceCh = nil
		case msg, ok := <-sourceCh:
			if !ok {
				s.closeDone()
				return
			}
			if !s.admission.admit(s.receive, msg) {
				admissionDone = nil
				sourceCh = nil
			}
		}
	}
}

func (s *sessionDurationAdmissionSession) drainSource(source *messages.TypedBuffer[messages.StreamMessage]) {
	for {
		msg, ok := source.Read()
		if !ok || !s.admission.admit(s.receive, msg) {
			return
		}
	}
}

func (s *sessionDurationAdmissionSession) closeDone() {
	s.doneOnce.Do(func() { close(s.done) })
}

var _ messages.SessionInferencer = (*sessionDurationAdmissionInferencer)(nil)
var _ messages.Session = (*sessionDurationAdmissionSession)(nil)

// RunSessionWithMaxDuration runs a session with an optional graceful duration
// bound. A zero duration disables the controller; a positive duration requests
// a session close and drains the accepted output before finalization.
func RunSessionWithMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration) error {
	return RunSessionWithMaxDurationClock(ctx, out, opts, maxDuration, realSessionDurationClock{})
}

// RunSessionWithMaxDurationClock is the deterministic-clock seam for the
// duration path. Production callers should use RunSessionWithMaxDuration.
func RunSessionWithMaxDurationClock(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration, durationClock SessionDurationClock) error {
	if err := ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}

	plan, err := planSessionRuntime(opts)
	if err != nil {
		return err
	}
	// Zero disables this controller. Preserve the runtime's existing safety
	// behavior for replay and injected sessions when the flag is omitted.
	if maxDuration == 0 {
		return plan.run(ctx, out)
	}
	durationCtx, err := prepareSessionDurationArtifacts(ctx)
	if err != nil {
		return err
	}
	if durationClock == nil {
		durationClock = realSessionDurationClock{}
	}
	return runSessionDurationPlan(durationCtx, out, plan, maxDuration, durationClock)
}

// RunSessionWithTextSeedAndMaxDuration preserves the explicit --prompt seed
// behavior while applying the duration admission boundary before the seed
// wrapper's audio sink. A zero duration delegates to the existing text-seed
// path so omitted-duration behavior remains unchanged.
func RunSessionWithTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration, seed SessionTextSeed) error {
	if err := ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	if !seed.Present {
		return RunSessionWithMaxDuration(ctx, out, opts, maxDuration)
	}
	if maxDuration == 0 {
		return RunSessionWithTextSeed(ctx, out, opts, seed)
	}

	opts.Prompt = seed.Value
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	plan, err := planSessionRuntime(opts)
	if err != nil {
		return err
	}
	durationCtx, err := prepareSessionDurationArtifacts(ctx)
	if err != nil {
		return err
	}

	wirePrompt := nextSessionTextWirePrompt()
	plan.loop.Prompt = wirePrompt
	output := &sessionTextOutput{writer: out}
	admission := newSessionDurationAdmission()
	var inner messages.SessionInferencer
	if plan.inferencer != nil {
		// The seed substitution wrapper must sit INSIDE the admission
		// boundary: the duration runner connects through admittedInferencer,
		// so any wrapper composed outside it never observes the session and
		// the sentinel prompt would leak onto the live wire.
		inner = &sessionTextSeedInferencer{
			inner:      plan.inferencer,
			wirePrompt: wirePrompt,
			value:      seed.Value,
			audioOut:   output,
		}
	}
	admittedInferencer := &sessionDurationAdmissionInferencer{
		inner:     inner,
		admission: admission,
		closeDone: make(chan struct{}),
	}
	if inner != nil {
		plan.inferencer = admittedInferencer
	}
	err = runSessionDurationPlanWithAdmission(durationCtx, output, plan, maxDuration, realSessionDurationClock{}, admittedInferencer)
	return errors.Join(err, output.errorValue())
}

func runSessionDurationPlan(ctx context.Context, out io.Writer, plan sessionRuntimePlan, maxDuration time.Duration, durationClock SessionDurationClock) error {
	return runSessionDurationPlanWithAdmission(ctx, out, plan, maxDuration, durationClock, nil)
}

func runSessionDurationPlanWithAdmission(ctx context.Context, out io.Writer, plan sessionRuntimePlan, maxDuration time.Duration, durationClock SessionDurationClock, admittedInferencer *sessionDurationAdmissionInferencer) (runErr error) {
	artifacts := sessionDurationArtifactsFromContext(ctx)
	deviceBinding, err := PrepareRTCDeviceBindings(plan.rtcDeviceRequest)
	if err != nil {
		return errors.Join(err, finalizeSessionDurationArtifacts(artifacts))
	}
	if deviceBinding != nil {
		plan.loop.rtcDeviceBinding = deviceBinding
		defer func() {
			runErr = errors.Join(runErr, deviceBinding.Close())
		}()
	}

	if plan.announce != "" {
		if _, err := fmt.Fprintln(out, plan.announce); err != nil {
			return wrapSessionRuntimeError(plan, errors.Join(err, finalizeSessionDurationArtifacts(artifacts)))
		}
	}

	loopOut := out
	if plan.loopOut != nil {
		loopOut = plan.loopOut
	}
	plan.configureLoopObserver(&plan.loop)
	if plan.inferencer != nil {
		runErr = runAgentLoopSessionWithDurationAdmissionClock(ctx, loopOut, plan.inferencer, plan.loop, maxDuration, durationClock, admittedInferencer)
	}

	artifactErr := finalizeSessionDurationArtifacts(artifacts)
	if runErr != nil {
		runErrs := []error{wrapSessionPhaseError("run session loop", runErr)}
		if artifactErr != nil {
			runErrs = append(runErrs, artifactErr)
		}
		if plan.flushCapture != nil {
			runErrs = append(runErrs, wrapSessionPhaseError("flush capture", plan.flushCapture()))
		}
		return wrapSessionRuntimeError(plan, errors.Join(runErrs...))
	}
	if artifactErr != nil {
		return wrapSessionRuntimeError(plan, artifactErr)
	}

	if plan.flushCapture != nil {
		if err := plan.flushCapture(); err != nil {
			return wrapSessionRuntimeError(plan, wrapSessionPhaseError("flush capture", err))
		}
	}
	if plan.finalize != nil {
		if err := plan.finalize(ctx, out); err != nil {
			return wrapSessionRuntimeError(plan, err)
		}
	}
	return nil
}

func finalizeSessionDurationArtifacts(artifacts SessionDurationArtifactLifecycle) error {
	if artifacts == nil {
		return nil
	}
	return errors.Join(
		wrapSessionPhaseError("flush duration artifacts", artifacts.Flush()),
		wrapSessionPhaseError("close duration artifacts", artifacts.Close()),
	)
}

func runAgentLoopSessionWithDurationClock(ctx context.Context, out io.Writer, sessionInferencer messages.SessionInferencer, opts sessionLoopOptions, maxDuration time.Duration, durationClock SessionDurationClock) error {
	return runAgentLoopSessionWithDurationAdmissionClock(ctx, out, sessionInferencer, opts, maxDuration, durationClock, nil)
}

func runAgentLoopSessionWithDurationAdmissionClock(ctx context.Context, out io.Writer, sessionInferencer messages.SessionInferencer, opts sessionLoopOptions, maxDuration time.Duration, durationClock SessionDurationClock, admittedInferencer *sessionDurationAdmissionInferencer) error {
	err := runAgentLoopSessionWithDurationAdmissionClockStream(ctx, out, sessionInferencer, opts, maxDuration, durationClock, admittedInferencer)
	opts.observer.finish(err)
	return err
}

func runAgentLoopSessionWithDurationAdmissionClockStream(ctx context.Context, out io.Writer, sessionInferencer messages.SessionInferencer, opts sessionLoopOptions, maxDuration time.Duration, durationClock SessionDurationClock, admittedInferencer *sessionDurationAdmissionInferencer) error {
	if maxDuration <= 0 {
		return runAgentLoopSession(ctx, out, sessionInferencer, opts)
	}

	if admittedInferencer == nil {
		admission := newSessionDurationAdmission()
		admittedInferencer = &sessionDurationAdmissionInferencer{
			inner:     sessionInferencer,
			admission: admission,
			closeDone: make(chan struct{}),
		}
	}
	var rtcPumpErrors <-chan error
	boundInferencer, rtcErrors := bindRTCDeviceSessionInferencer(admittedInferencer, opts.rtcDeviceBinding)
	rtcPumpErrors = rtcErrors
	observedInferencer := newObservedSessionInferencer(boundInferencer)
	loop, err := agentloop.New(duplexSessionLoopOptions(observedInferencer, opts)...)
	if err != nil {
		return fmt.Errorf("create session agent loop: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- loop.Run(runCtx)
	}()

	timer := durationClock.NewTimer(maxDuration)
	if timer == nil {
		admittedInferencer.closeAdmission()
		cancel()
		<-runErrCh
		admittedInferencer.waitForClose()
		return errors.New("session duration clock returned a nil timer")
	}
	defer timer.Stop()
	timerCh := timer.C()

	promptSent := false
	closeSent := false
	durationExpired := false
	durationTerminalWritten := false
	artifacts := sessionDurationArtifactsFromContext(ctx)

	finish := func(planned bool, preferredErr error) error {
		var preCancelDrainErr error
		if preferredErr == nil {
			preCancelDrainErr = drainDurationSessionLoopMessagesUntilQuiet(out, loop, planned, &durationTerminalWritten, artifacts, opts.observer)
		}
		cancel()
		bindingErr := closeRTCDeviceBinding(opts.rtcDeviceBinding)
		runErr := <-runErrCh
		admittedInferencer.waitForClose()
		if drainErr := drainDurationSessionLoopMessages(out, loop, planned, &durationTerminalWritten, artifacts, opts.observer); drainErr != nil {
			return drainErr
		}
		runtimeErr := admittedInferencer.runtimeError()
		closeErr := admittedInferencer.closeError()
		if preferredErr != nil {
			if lifecycleErr := sessionDurationLifecycleError(runtimeErr, closeErr, bindingErr); lifecycleErr != nil {
				return errors.Join(preferredErr, lifecycleErr)
			}
			return preferredErr
		}
		if preCancelDrainErr != nil {
			return preCancelDrainErr
		}
		if lifecycleErr := sessionDurationLifecycleError(runtimeErr, closeErr, bindingErr); lifecycleErr != nil {
			return lifecycleErr
		}
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			return fmt.Errorf("session error: %w", runErr)
		}
		if planned && !durationTerminalWritten {
			if err := writeMaxDurationTerminal(out, artifacts); err != nil {
				return err
			}
		}
		return nil
	}

	expire := func() error {
		if durationExpired {
			return nil
		}
		durationExpired = true
		timerCh = nil
		admittedInferencer.closeAdmission()
		if closeSent {
			return nil
		}
		closeSent = true
		return sendSessionClose(runCtx, loop)
	}

	for {
		// Prefer a deadline that is already ready over a simultaneously ready
		// provider-close signal. Once this branch wins, the planned reason is
		// retained and the close is still drained normally.
		if !durationExpired && sessionDurationTimerReady(timerCh) {
			if err := expire(); err != nil {
				return finish(false, err)
			}
			continue
		}

		select {
		case <-timerCh:
			if err := expire(); err != nil {
				return finish(false, err)
			}
		case <-ctx.Done():
			if err := finish(false, nil); err != nil {
				return err
			}
			return ctx.Err()
		case <-opts.Done:
			doneErr := error(nil)
			if opts.DoneErr != nil {
				doneErr = opts.DoneErr()
			}
			return finish(durationExpired && doneErr == nil, doneErr)
		case <-observedInferencer.Done():
			doneErr := error(nil)
			if opts.DoneErr != nil {
				doneErr = opts.DoneErr()
			}
			return finish(durationExpired && doneErr == nil, doneErr)
		case pumpErr := <-rtcPumpErrors:
			return finish(false, pumpErr)
		case err := <-runErrCh:
			admittedInferencer.waitForClose()
			if drainErr := drainDurationSessionLoopMessages(out, loop, durationExpired, &durationTerminalWritten, artifacts, opts.observer); drainErr != nil {
				return drainErr
			}
			if runtimeErr := admittedInferencer.runtimeError(); runtimeErr != nil {
				return wrapSessionPhaseError("session runtime", runtimeErr)
			}
			if closeErr := admittedInferencer.closeError(); closeErr != nil {
				return wrapSessionPhaseError("close session", closeErr)
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				return fmt.Errorf("session error: %w", err)
			}
			if durationExpired && !durationTerminalWritten {
				if err := writeMaxDurationTerminal(out, artifacts); err != nil {
					return err
				}
			}
			return nil
		case msg, ok := <-loop.Deltas().Chan():
			if !ok {
				return finish(durationExpired, nil)
			}
			if durationExpired && msg.Type == messages.StreamTypeSessionClose {
				msg, durationTerminalWritten = maxDurationTerminalMessage(msg)
			}
			opts.observer.observe(msg)
			opts.observer.dispatchScheduledInputs(runCtx, loop)
			if err := writeDurationSessionReplayMessage(out, msg, artifacts); err != nil {
				return finish(false, err)
			}
			if msg.Type == messages.StreamTypeSessionOpen && !durationExpired {
				if opts.Prompt != "" && !promptSent {
					promptSent = true
					userMsg := messages.NewTextMessage(messages.RoleUser, opts.Prompt)
					if err := loop.Send(runCtx, []messages.Message{userMsg}); err != nil {
						return finish(false, fmt.Errorf("send session message: %w", err))
					}
					opts.observer.noteUserTextInput(opts.Prompt)
				}
				if opts.CloseAfterOpen && opts.Prompt == "" && !closeSent {
					closeSent = true
					if err := sendSessionClose(runCtx, loop); err != nil {
						return finish(false, err)
					}
				}
			}
			if !durationExpired && opts.CloseAfterOpen && opts.Prompt != "" && msg.Type == messages.StreamTypeMessageEnd && !closeSent {
				closeSent = true
				if err := sendSessionClose(runCtx, loop); err != nil {
					return finish(false, err)
				}
			}
			if shouldStopSessionLoop(msg, opts, closeSent) && (!durationExpired || msg.Type == messages.StreamTypeSessionClose) {
				return finish(durationExpired, nil)
			}
		}
	}
}

func sessionDurationTimerReady(timerCh <-chan time.Time) bool {
	if timerCh == nil {
		return false
	}
	select {
	case <-timerCh:
		return true
	default:
		return false
	}
}

func writeDurationSessionReplayMessage(out io.Writer, msg messages.StreamMessage, artifacts SessionDurationArtifactLifecycle) error {
	if artifacts != nil {
		if err := artifacts.Accept(msg); err != nil {
			return wrapSessionPhaseError("write duration artifacts", err)
		}
	}
	return writeSessionReplayMessage(out, msg)
}

func drainDurationSessionLoopMessages(out io.Writer, loop *agentloop.AgentLoop, planned bool, terminalWritten *bool, artifacts SessionDurationArtifactLifecycle, obs *sessionProgressObserver) error {
	for {
		msg, ok := loop.Deltas().Read()
		if !ok {
			return nil
		}
		if planned && msg.Type == messages.StreamTypeSessionClose {
			msg, *terminalWritten = maxDurationTerminalMessage(msg)
		}
		obs.observe(msg)
		if err := writeDurationSessionReplayMessage(out, msg, artifacts); err != nil {
			return err
		}
	}
}

func drainDurationSessionLoopMessagesUntilQuiet(out io.Writer, loop *agentloop.AgentLoop, planned bool, terminalWritten *bool, artifacts SessionDurationArtifactLifecycle, obs *sessionProgressObserver) error {
	timer := time.NewTimer(sessionReplayDoneDrainIdleDelay)
	defer timer.Stop()
	for {
		select {
		case msg, ok := <-loop.Deltas().Chan():
			if !ok {
				return nil
			}
			if planned && msg.Type == messages.StreamTypeSessionClose {
				msg, *terminalWritten = maxDurationTerminalMessage(msg)
			}
			obs.observe(msg)
			if err := writeDurationSessionReplayMessage(out, msg, artifacts); err != nil {
				return err
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(sessionReplayDoneDrainIdleDelay)
		case <-timer.C:
			return nil
		}
	}
}

func maxDurationTerminalMessage(msg messages.StreamMessage) (messages.StreamMessage, bool) {
	value, ok := msg.Value.(*messages.SessionCloseValue)
	if !ok {
		return msg, false
	}
	clone := *value
	clone.Reason = string(SessionMaxDurationReason)
	clone.Classification = string(SessionMaxDurationReason)
	clone.TerminalReason = SessionMaxDurationReason
	clone.TerminalProvenance = messages.TerminalProvenanceLoop
	clone.OutputState = messages.TerminalOutputNotApplicable
	msg.Value = &clone
	return msg, true
}

func writeMaxDurationTerminal(out io.Writer, artifacts SessionDurationArtifactLifecycle) error {
	return writeDurationSessionReplayMessage(out, messages.StreamMessage{
		Type: messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValueWithTerminal(
			"",
			string(SessionMaxDurationReason),
			string(SessionMaxDurationReason),
			SessionMaxDurationReason,
			messages.TerminalProvenanceLoop,
			messages.TerminalOutputNotApplicable,
		),
	}, artifacts)
}

func sessionDurationLifecycleError(runtimeErr, closeErr, bindingErr error) error {
	var lifecycleErrs []error
	if runtimeErr != nil {
		lifecycleErrs = append(lifecycleErrs, wrapSessionPhaseError("session runtime", runtimeErr))
	}
	if closeErr != nil {
		lifecycleErrs = append(lifecycleErrs, wrapSessionPhaseError("close session", closeErr))
	}
	if bindingErr != nil {
		lifecycleErrs = append(lifecycleErrs, wrapSessionPhaseError("close RTC device binding", bindingErr))
	}
	return errors.Join(lifecycleErrs...)
}

var _ SessionDurationClock = realSessionDurationClock{}
var _ SessionDurationTimer = realSessionDurationTimer{}
