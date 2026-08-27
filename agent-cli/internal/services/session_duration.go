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

// sessionDurationTerminalRecorder receives normalized terminal metadata that
// the duration controller emits. It is deliberately separate from the raw
// artifact lifecycle: a recording directory needs the controller-owned
// summary, but must not be given a fabricated provider frame.
type sessionDurationTerminalRecorder interface {
	RecordTerminalSummary(transcript.RecordingTerminalSummary) error
}

type sessionDurationArtifactLifecycleWithTerminal struct {
	artifacts SessionDurationArtifactLifecycle
	recorder  sessionDurationTerminalRecorder
}

func (a *sessionDurationArtifactLifecycleWithTerminal) Accept(msg messages.StreamMessage) error {
	if a == nil {
		return nil
	}
	if a.artifacts != nil {
		if err := a.artifacts.Accept(msg); err != nil {
			return err
		}
	}
	if a.recorder == nil || msg.Type != messages.StreamTypeSessionClose {
		return nil
	}
	summary, present, err := recordingTerminalSummaryFromMessage(msg)
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	return a.recorder.RecordTerminalSummary(*summary)
}

func recordingTerminalSummaryFromMessage(msg messages.StreamMessage) (*transcript.RecordingTerminalSummary, bool, error) {
	if msg.Type != messages.StreamTypeSessionClose {
		return nil, false, nil
	}
	value, ok := msg.Value.(*messages.SessionCloseValue)
	if !ok || value == nil {
		return nil, false, nil
	}
	if value.Classification == "" && value.TerminalReason == "" && value.TerminalProvenance == "" && value.OutputState == "" {
		return nil, false, nil
	}
	summary := &transcript.RecordingTerminalSummary{
		Reason:             value.Reason,
		Classification:     value.Classification,
		TerminalReason:     value.TerminalReason,
		TerminalProvenance: value.TerminalProvenance,
		OutputState:        value.OutputState,
	}
	if err := summary.Validate(); err != nil {
		return nil, false, err
	}
	return summary, true, nil
}

func (a *sessionDurationArtifactLifecycleWithTerminal) Flush() error {
	if a == nil || a.artifacts == nil {
		return nil
	}
	return a.artifacts.Flush()
}

func (a *sessionDurationArtifactLifecycleWithTerminal) Close() error {
	if a == nil || a.artifacts == nil {
		return nil
	}
	return a.artifacts.Close()
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

func withSessionDurationTerminalRecorder(ctx context.Context, recorder sessionDurationTerminalRecorder) context.Context {
	if recorder == nil {
		return ctx
	}
	return WithSessionDurationArtifacts(ctx, &sessionDurationArtifactLifecycleWithTerminal{
		artifacts: sessionDurationArtifactsFromContext(ctx),
		recorder:  recorder,
	})
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
	if plan.rtcRuntime != nil {
		defer func() {
			if err := plan.rtcRuntime.Close(); err != nil {
				runErr = errors.Join(runErr, wrapSessionPhaseError("close WebRTC runtime", err))
			}
		}()
	}
	if plan.closeSession != nil {
		defer func() {
			if err := plan.closeSession(); err != nil {
				runErr = errors.Join(runErr, wrapSessionPhaseError("close WebRTC provider session", err))
			}
		}()
	}
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
	err = scheduledAudioCompletionError(err, opts)
	err = opts.observer.finish(err)
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
	observedInferencer.progress = opts.observer
	if opts.observer != nil {
		opts.observer.setToolResultsEnabled(opts.ToolExecutor != nil)
	}
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

	var sessionUpdatedTimer *time.Timer
	var sessionUpdatedTimeout <-chan time.Time
	startSessionUpdatedTimer := func() {
		if !opts.RequireSessionUpdated || opts.observer == nil || !opts.observer.scheduledAudioAwaitingConfiguration() || sessionUpdatedTimer != nil {
			return
		}
		timeout := opts.SessionUpdatedTimeout
		if timeout <= 0 {
			timeout = sessionScheduledAudioConfigTimeout
		}
		sessionUpdatedTimer = time.NewTimer(timeout)
		sessionUpdatedTimeout = sessionUpdatedTimer.C
	}
	stopSessionUpdatedTimer := func() {
		if sessionUpdatedTimer == nil {
			return
		}
		sessionUpdatedTimer.Stop()
		sessionUpdatedTimer = nil
		sessionUpdatedTimeout = nil
	}
	defer stopSessionUpdatedTimer()

	promptSent := false
	closeSent := false
	closeAfterOpenPending := false
	durationExpired := false
	durationTerminalWritten := false
	artifacts := sessionDurationArtifactsFromContext(ctx)
	terminalState := newSessionDurationTerminalState(admittedInferencer)
	toolLifecycleEvents := opts.observer.toolLifecycleEvents()

	finish := func(planned bool, preferredErr error) error {
		var preCancelDrainErr error
		if preferredErr == nil {
			preCancelDrainErr = drainDurationSessionLoopMessagesUntilQuiet(out, loop, planned, &durationTerminalWritten, artifacts, opts.observer, terminalState)
		}
		cancel()
		bindingErr := closeRTCDeviceBinding(opts.rtcDeviceBinding)
		runErr := <-runErrCh
		admittedInferencer.waitForClose()
		sessionErr := observedInferencer.sessionFailure()
		if drainErr := drainDurationSessionLoopMessages(out, loop, planned, &durationTerminalWritten, artifacts, opts.observer, terminalState); drainErr != nil {
			return drainErr
		}
		if planned && !terminalState.terminalWritten {
			if err := terminalState.writeObservedProviderTerminal(out, artifacts); err != nil {
				return err
			}
		}
		runtimeErr := admittedInferencer.runtimeError()
		closeErr := admittedInferencer.closeError()
		if preferredErr != nil {
			lifecycleErr := sessionDurationLifecycleError(runtimeErr, closeErr, bindingErr)
			transportErr := sessionTransportError(sessionErr)
			if lifecycleErr != nil || transportErr != nil {
				return errors.Join(preferredErr, lifecycleErr, transportErr)
			}
			return preferredErr
		}
		if preCancelDrainErr != nil {
			return preCancelDrainErr
		}
		if lifecycleErr := sessionDurationLifecycleError(runtimeErr, closeErr, bindingErr); lifecycleErr != nil {
			return lifecycleErr
		}
		if sessionErr != nil {
			return sessionTransportError(sessionErr)
		}
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			return fmt.Errorf("session error: %w", runErr)
		}
		if planned && !terminalState.terminalWritten {
			if err := writeMaxDurationTerminal(out, artifacts, terminalState.outputState()); err != nil {
				return err
			}
			terminalState.terminalWritten = true
			durationTerminalWritten = true
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
			return finish(true, nil)
		}

		select {
		case <-toolLifecycleEvents:
			state, closeErr := closePendingSessionIfReady(runCtx, loop, opts, sessionLoopMessageState{
				closeSent:             closeSent,
				closeAfterOpenPending: closeAfterOpenPending,
			})
			if closeErr != nil {
				return finish(false, closeErr)
			}
			closeSent = state.closeSent
		case <-timerCh:
			if err := expire(); err != nil {
				return finish(false, err)
			}
			return finish(true, nil)
		case <-sessionUpdatedTimeout:
			stopSessionUpdatedTimer()
			return finish(false, sessionScheduledAudioConfigTimeoutError(opts))
		case <-ctx.Done():
			if err := finish(durationExpired, nil); err != nil {
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
			if drainErr := drainDurationSessionLoopMessages(out, loop, durationExpired, &durationTerminalWritten, artifacts, opts.observer, terminalState); drainErr != nil {
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
			if durationExpired && !terminalState.terminalWritten {
				if err := terminalState.writeObservedProviderTerminal(out, artifacts); err != nil {
					return err
				}
			}
			if durationExpired && !terminalState.terminalWritten {
				if err := writeMaxDurationTerminal(out, artifacts, terminalState.outputState()); err != nil {
					return err
				}
				terminalState.terminalWritten = true
				durationTerminalWritten = true
			}
			return nil
		case msg, ok := <-loop.Deltas().Chan():
			if !ok {
				return finish(durationExpired, nil)
			}
			result, msgErr := processDurationLoopMessage(runCtx, loop, out, msg, opts, durationExpired, promptSent, closeSent, closeAfterOpenPending, durationTerminalWritten, artifacts, terminalState)
			promptSent = result.promptSent
			closeSent = result.closeSent
			closeAfterOpenPending = result.closeAfterOpenPending
			durationTerminalWritten = result.durationTerminalWritten
			if msgErr != nil {
				return finish(false, msgErr)
			}
			if msg.Type == messages.StreamTypeSessionOpen {
				startSessionUpdatedTimer()
			}
			if opts.observer != nil && opts.observer.scheduledAudioReady() {
				stopSessionUpdatedTimer()
			}
			if result.stop {
				return finish(result.planned, nil)
			}
		}
	}
}

func sessionTransportError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("session transport: %w", err)
}

type sessionDurationMessageResult struct {
	promptSent              bool
	closeSent               bool
	closeAfterOpenPending   bool
	durationTerminalWritten bool
	stop                    bool
	planned                 bool
}

type sessionDurationTerminalState struct {
	admittedInferencer *sessionDurationAdmissionInferencer
	terminalWritten    bool
	responseOutput     bool
	responseComplete   bool
}

func newSessionDurationTerminalState(admittedInferencer *sessionDurationAdmissionInferencer) *sessionDurationTerminalState {
	return &sessionDurationTerminalState{admittedInferencer: admittedInferencer}
}

func (s *sessionDurationTerminalState) observe(msg messages.StreamMessage) {
	if s == nil {
		return
	}
	switch msg.Type {
	case messages.StreamTypeMessageStart:
		s.responseOutput = false
		s.responseComplete = false
	case messages.StreamTypeTextDelta,
		messages.StreamTypeReasoningDelta,
		messages.StreamTypeAudioDelta,
		messages.StreamTypeImageDelta,
		messages.StreamTypeVideoDelta,
		messages.StreamTypeFileDelta,
		messages.StreamTypeEmbeddingDelta,
		messages.StreamTypeTranscriptDelta,
		messages.StreamTypeToolCallDelta,
		messages.StreamTypeToolCallEnd,
		messages.StreamTypeRefusal:
		s.responseOutput = true
	case messages.StreamTypeMessageEnd:
		s.responseComplete = true
	}
}

func (s *sessionDurationTerminalState) outputState() messages.TerminalOutputState {
	if s == nil || !s.responseOutput {
		return messages.TerminalOutputNone
	}
	if s.responseComplete {
		return messages.TerminalOutputComplete
	}
	return messages.TerminalOutputPartial
}

// admitTerminal decides which SESSION.CLOSE messages are visible in the
// normalized duration artifact. The loop emits its own close immediately when
// a close control reaches the coordinator; that close is only a shutdown
// request, not proof that the provider sent a terminal wire event. Defer it
// until the bounded drain has established whether the provider terminal was
// actually observed.
func (s *sessionDurationTerminalState) admitTerminal(planned bool, msg messages.StreamMessage) (messages.StreamMessage, bool) {
	if msg.Type != messages.StreamTypeSessionClose {
		return msg, true
	}
	if s.terminalWritten {
		return msg, false
	}
	if s.admittedInferencer != nil && s.admittedInferencer.isProviderTerminalMessage(msg) {
		s.terminalWritten = true
		return msg, true
	}
	if !planned {
		return msg, true
	}
	return msg, false
}

func (s *sessionDurationTerminalState) writeObservedProviderTerminal(out io.Writer, artifacts SessionDurationArtifactLifecycle) error {
	if s == nil || s.terminalWritten || s.admittedInferencer == nil {
		return nil
	}
	msg, ok := s.admittedInferencer.providerTerminalMessage()
	if !ok {
		return nil
	}
	if err := writeDurationSessionReplayMessage(out, msg, artifacts); err != nil {
		return err
	}
	s.terminalWritten = true
	return nil
}

func processDurationLoopMessage(ctx context.Context, loop *agentloop.AgentLoop, out io.Writer, msg messages.StreamMessage, opts sessionLoopOptions, durationExpired, promptSent, closeSent, closeAfterOpenPending, durationTerminalWritten bool, artifacts SessionDurationArtifactLifecycle, terminalState *sessionDurationTerminalState) (sessionDurationMessageResult, error) {
	result := sessionDurationMessageResult{
		promptSent:              promptSent,
		closeSent:               closeSent,
		closeAfterOpenPending:   closeAfterOpenPending,
		durationTerminalWritten: durationTerminalWritten,
	}
	if terminalState != nil {
		terminalState.observe(msg)
		var shouldWrite bool
		msg, shouldWrite = terminalState.admitTerminal(durationExpired, msg)
		if !shouldWrite {
			result.durationTerminalWritten = terminalState.terminalWritten
			result.planned = durationExpired
			result.stop = false
			return result, nil
		}
		result.durationTerminalWritten = terminalState.terminalWritten
	}
	opts.observer.observe(msg)
	if err := writeDurationSessionReplayMessage(out, msg, artifacts); err != nil {
		return result, err
	}
	if msg.Type == messages.StreamTypeSessionOpen && !durationExpired {
		if opts.Prompt != "" && !result.promptSent {
			result.promptSent = true
			userMsg := messages.NewTextMessage(messages.RoleUser, opts.Prompt)
			if err := loop.Send(ctx, []messages.Message{userMsg}); err != nil {
				return result, fmt.Errorf("send session message: %w", err)
			}
			opts.observer.noteUserTextInput(opts.Prompt)
		}
		if opts.CloseAfterOpen && opts.Prompt == "" && !result.closeSent {
			result.closeAfterOpenPending = true
		}
	}
	var err error
	result.closeSent, err = processDurationScheduledMessage(ctx, loop, msg, opts, result.closeSent)
	if err != nil {
		return result, err
	}
	if !durationExpired && opts.CloseAfterOpen && opts.Prompt != "" && msg.Type == messages.StreamTypeMessageEnd && !result.closeSent {
		result.closeAfterOpenPending = true
	}
	state, err := closePendingSessionIfReady(ctx, loop, opts, sessionLoopMessageState{
		closeSent:             result.closeSent,
		closeAfterOpenPending: result.closeAfterOpenPending,
	})
	if err != nil {
		return result, err
	}
	result.closeSent = state.closeSent
	result.stop = shouldStopSessionLoop(msg, opts, result.closeSent) && (!durationExpired || msg.Type == messages.StreamTypeSessionClose)
	result.planned = durationExpired
	return result, nil
}

func processDurationScheduledMessage(ctx context.Context, loop *agentloop.AgentLoop, msg messages.StreamMessage, opts sessionLoopOptions, closeSent bool) (bool, error) {
	if msg.Type != messages.StreamTypeSessionOpen && msg.Type != messages.StreamTypeMessageEnd && msg.Type != messages.StreamTypeSessionUpdated {
		return closeSent, nil
	}
	if err := opts.observer.dispatchScheduledInputs(ctx, loop); err != nil {
		return closeSent, err
	}
	return closeSent, nil
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

func drainDurationSessionLoopMessages(out io.Writer, loop *agentloop.AgentLoop, planned bool, terminalWritten *bool, artifacts SessionDurationArtifactLifecycle, obs *sessionProgressObserver, terminalState *sessionDurationTerminalState) error {
	for {
		msg, ok := loop.Deltas().Read()
		if !ok {
			return nil
		}
		if terminalState != nil {
			terminalState.observe(msg)
			var shouldWrite bool
			msg, shouldWrite = terminalState.admitTerminal(planned, msg)
			*terminalWritten = terminalState.terminalWritten
			if !shouldWrite {
				continue
			}
		}
		obs.observe(msg)
		if err := writeDurationSessionReplayMessage(out, msg, artifacts); err != nil {
			return err
		}
	}
}

func drainDurationSessionLoopMessagesUntilQuiet(out io.Writer, loop *agentloop.AgentLoop, planned bool, terminalWritten *bool, artifacts SessionDurationArtifactLifecycle, obs *sessionProgressObserver, terminalState *sessionDurationTerminalState) error {
	timer := time.NewTimer(sessionReplayDoneDrainIdleDelay)
	defer timer.Stop()
	for {
		select {
		case msg, ok := <-loop.Deltas().Chan():
			if !ok {
				return nil
			}
			if terminalState != nil {
				terminalState.observe(msg)
				var shouldWrite bool
				msg, shouldWrite = terminalState.admitTerminal(planned, msg)
				*terminalWritten = terminalState.terminalWritten
				if !shouldWrite {
					continue
				}
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

func writeMaxDurationTerminal(out io.Writer, artifacts SessionDurationArtifactLifecycle, outputState messages.TerminalOutputState) error {
	return writeDurationSessionReplayMessage(out, messages.StreamMessage{
		Type: messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValueWithTerminal(
			"",
			string(SessionMaxDurationReason),
			string(SessionMaxDurationReason),
			SessionMaxDurationReason,
			messages.TerminalProvenanceLoop,
			outputState,
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
