package services

import (
	"bytes"
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
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

// PrepareSessionAudioInputs loads a finite set of audio files using the same
// decoder and PCM contract as --audio-in-turn. The returned inputs are ready
// for an event-driven interruption source; no provider or browser is touched.
func PrepareSessionAudioInputs(paths []string) ([]ScheduledAudioInput, error) {
	return prepareScheduledAudioInputs(paths)
}

// StartSessionAudioInterruptionsOnBrowserInvocation creates the shared-loop
// interruption source used by the live conversational acceptance runner. The
// first observed dispatched browser invocation releases the finite audio
// inputs, so overlap is synchronized to a semantic browser event rather than a
// wall-clock delay. The returned stop function is idempotent in effect and
// should be called when the owning session boundary closes.
func StartSessionAudioInterruptionsOnBrowserInvocation(
	parent context.Context,
	events <-chan webmcp.BrokerEvent,
	inputs []ScheduledAudioInput,
) (<-chan ScheduledAudioInput, func()) {
	return StartSessionAudioInterruptionsOnBrowserTool(parent, events, "", inputs)
}

// StartSessionAudioInterruptionsOnBrowserTool is the tool-specific form of
// StartSessionAudioInterruptionsOnBrowserInvocation. An empty toolName keeps
// the first-invocation behavior; otherwise only a matching dispatched browser
// invocation releases the finite interruption audio.
func StartSessionAudioInterruptionsOnBrowserTool(
	parent context.Context,
	events <-chan webmcp.BrokerEvent,
	toolName string,
	inputs []ScheduledAudioInput,
) (<-chan ScheduledAudioInput, func()) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	out := make(chan ScheduledAudioInput, len(inputs))
	cloned := cloneScheduledAudioInputs(inputs)
	go func() {
		defer close(out)
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-events:
				if !ok {
					return
				}
				// Broker-owned calls first publish a queued admission event and
				// publish this same lifecycle event type again when the browser
				// invocation is actually dispatched. Require the identity-bearing
				// start observation so a malformed or unrelated lifecycle event
				// cannot release audio.
				if event.Type != webmcp.BrokerEventInvocationCreated || event.State != webmcp.InvocationDispatched || event.InvocationID == "" || event.ToolName == "" || (toolName != "" && event.ToolName != toolName) {
					continue
				}
				for _, input := range cloned {
					select {
					case out <- input:
					case <-ctx.Done():
						return
					}
				}
				return
			}
		}
	}()
	return out, cancel
}

// SessionAudioInput carries the command-line presence bit separately from the
// value so --audio-in= can be rejected instead of treated as an omitted flag.
type SessionAudioInput struct {
	Path  string
	Stdin io.Reader
	// SourceSampleRate declares raw or injected PCM. Zero selects the legacy
	// 16 kHz file/test-source contract; WAV files retain their header rate.
	SourceSampleRate int
	// CloseStdinOnCancel allows the process-owned `--audio-in -` descriptor to
	// interrupt a blocked read when the CLI session is cancelled. Callers that
	// provide a shared or caller-owned stdin must leave this false.
	CloseStdinOnCancel bool
	// MaxDuration bounds an audio-enabled session through the shared loop
	// options when the caller supplies one.
	MaxDuration time.Duration
	// Source and SendAudioInput are optional deterministic service-test seams.
	// CLI callers leave them nil so paths use the file-backed sources and
	// frames use the AgentLoop's SendAudioInput method.
	Source         audio.AudioSource
	SendAudioInput func(context.Context, []byte) error
	// SendEndOfTurn is an optional deterministic service-test seam invoked
	// once after the finite source reaches EOF. CLI callers leave it nil so
	// the loop's SendSessionEvent carries the end-of-turn boundary.
	SendEndOfTurn func(context.Context) error
	Present       bool
	DevicePresent bool
}

// SessionAudioInputErrorKind identifies the failed session audio boundary.
type SessionAudioInputErrorKind string

const (
	SessionAudioInputEmpty      SessionAudioInputErrorKind = "empty"
	SessionAudioInputMissing    SessionAudioInputErrorKind = "missing"
	SessionAudioInputUnreadable SessionAudioInputErrorKind = "unreadable"
	SessionAudioInputFormat     SessionAudioInputErrorKind = "format"
	SessionAudioInputConflict   SessionAudioInputErrorKind = "conflict"
	SessionAudioInputRead       SessionAudioInputErrorKind = "read"
	SessionAudioInputSend       SessionAudioInputErrorKind = "send"
	SessionAudioInputClose      SessionAudioInputErrorKind = "close"
)

var (
	ErrSessionAudioInputEmpty           = errors.New("session audio input path is empty")
	ErrSessionAudioInputMissing         = errors.New("session audio input is missing")
	ErrSessionAudioInputUnreadable      = errors.New("session audio input is unreadable")
	ErrSessionAudioInputFormat          = errors.New("session audio input format is unsupported")
	ErrSessionAudioInputConflict        = errors.New("--audio-in and --audio-in-device (audio device input) cannot be used together")
	ErrSessionAudioInputRead            = errors.New("session audio input read failed")
	ErrSessionAudioInputSend            = errors.New("session audio input send failed")
	ErrSessionAudioInputClose           = errors.New("session audio input close failed")
	ErrSessionAudioInputUninterruptible = errors.New("session audio input reader cannot be interrupted safely")
	// ErrSessionAudioInputEndOfTurnLost reports that a cancellation raced the
	// end-of-turn signal, so commit + response.create may never have reached
	// the provider. It is surfaced as an explicit failure instead of being
	// silently swallowed as an ordinary session cancellation.
	ErrSessionAudioInputEndOfTurnLost = errors.New("end-of-turn signal was not delivered before session shutdown")
)

// SessionAudioInputError adds the command boundary and preserves the
// underlying audio, filesystem, context, and loop error identity.
type SessionAudioInputError struct {
	Kind SessionAudioInputErrorKind
	Path string
	Err  error
}

func (e *SessionAudioInputError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return fmt.Sprintf("agent session --audio-in %q: %s", e.Path, e.Kind)
	}
	return fmt.Sprintf("agent session --audio-in %q: %s: %v", e.Path, e.Kind, e.Err)
}

func (e *SessionAudioInputError) Unwrap() error {
	if e == nil {
		return nil
	}
	return errors.Join(sessionAudioInputKindError(e.Kind), e.Err)
}

func emptySessionAudioInput(path string) error {
	return &SessionAudioInputError{
		Kind: SessionAudioInputEmpty,
		Path: path,
		Err:  fmt.Errorf("no audio frames were sent; refusing to commit an empty user turn: %w", ErrSessionAudioInputEmpty),
	}
}

func sessionAudioInputKindError(kind SessionAudioInputErrorKind) error {
	switch kind {
	case SessionAudioInputEmpty:
		return ErrSessionAudioInputEmpty
	case SessionAudioInputMissing:
		return ErrSessionAudioInputMissing
	case SessionAudioInputUnreadable:
		return ErrSessionAudioInputUnreadable
	case SessionAudioInputFormat:
		return ErrSessionAudioInputFormat
	case SessionAudioInputConflict:
		return ErrSessionAudioInputConflict
	case SessionAudioInputRead:
		return ErrSessionAudioInputRead
	case SessionAudioInputSend:
		return ErrSessionAudioInputSend
	case SessionAudioInputClose:
		return ErrSessionAudioInputClose
	default:
		return nil
	}
}

// RunSessionWithAudioInput runs the shared session runtime while streaming the
// selected file or raw stdin through the agent loop's session audio inbox.
// The ordinary session path remains untouched when the flag is absent.
func RunSessionWithAudioInput(ctx context.Context, out io.Writer, opts SessionRunOptions, input SessionAudioInput) (runErr error) {
	var coordinator *SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	if !sessionAudioInputSelected(input) {
		return RunSession(ctx, out, opts)
	}
	if err := validateSessionAudioInput(input); err != nil {
		return err
	}
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return err
	}
	defer func() { _ = claim.release() }()
	opts.ClientOwnsAudioTurnBoundaries = true
	return runSessionWithAudioInputPlan(ctx, out, input, "", SessionTextSeed{}, func() (sessionRuntimePlan, error) {
		if err := validateSessionRunOptions(opts); err != nil {
			return sessionRuntimePlan{}, err
		}
		return planSessionRuntime(opts)
	})
}

// RunSessionWithInstructionsAndAudioInputAndTextSeedAndMaxDuration composes
// the instructions, text-seed, duration, and audio-input extensions on the
// command surface. The no-audio path remains on the established instructions
// entry point so its replay and duration artifact behavior stays unchanged.
func RunSessionWithInstructionsAndAudioInputAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration, seed SessionTextSeed, input SessionAudioInput, systemPrompt string) error {
	return RunSessionWithInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration(ctx, out, opts, "", maxDuration, seed, input, systemPrompt)
}

// RunSessionWithInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration
// composes the instructions, text-seed, duration, audio-input, and
// audio-output extensions on the command surface. An empty audioOutPath
// preserves the established audio-input-only behavior.
func RunSessionWithInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, audioOutPath string, maxDuration time.Duration, seed SessionTextSeed, input SessionAudioInput, systemPrompt string) (runErr error) {
	var coordinator *SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	if err := ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	if !sessionAudioInputSelected(input) {
		return RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration(ctx, out, opts, audioOutPath, maxDuration, seed, systemPrompt)
	}
	if audioOutPath != "" {
		opts.AudioOutputRequested = true
	}
	if seed.Present {
		opts.Prompt = seed.Value
		opts.PromptProvided = true
	}
	input.MaxDuration = maxDuration
	if err := validateSessionAudioInput(input); err != nil {
		return err
	}
	if err := validateSessionAudioInputFileExists(input); err != nil {
		return err
	}
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return err
	}
	defer func() { _ = claim.release() }()
	// This source owns the explicit end-of-turn marker below. Ask the provider
	// runtime to disable server-side VAD before the session is connected so a
	// finite stream cannot be auto-committed and then committed again at EOF.
	opts.ClientOwnsAudioTurnBoundaries = true
	if opts.ReplayPath != "" && (opts.SessionInferencer == nil || strings.TrimSpace(systemPrompt) == "") {
		return runSessionWithAudioInputPlan(ctx, out, input, audioOutPath, seed, func() (sessionRuntimePlan, error) {
			if err := validateSessionRunOptions(opts); err != nil {
				return sessionRuntimePlan{}, err
			}
			return planSessionRuntime(opts)
		})
	}
	return runSessionWithAudioInputPlan(ctx, out, input, audioOutPath, seed, func() (sessionRuntimePlan, error) {
		if err := validateSessionRunOptions(opts); err != nil {
			return sessionRuntimePlan{}, err
		}
		instructions, err := resolveSessionInstructions(opts, systemPrompt)
		if err != nil {
			return sessionRuntimePlan{}, err
		}
		return planSessionWithResolvedInstructions(opts, instructions)
	})
}

func sessionAudioInputSelected(input SessionAudioInput) bool {
	return input.Present || input.Path != ""
}

// runSessionWithAudioInputPlan validates and opens the audio input before the
// plan is built so every preflight failure happens before any provider dial,
// then hands the opened source to the shared session lifecycle through the
// loop options. A non-empty audioOutPath additionally records assistant audio
// received after the end-of-turn commit. No session behavior changes when the
// flag is absent.
func runSessionWithAudioInputPlan(ctx context.Context, out io.Writer, input SessionAudioInput, audioOutPath string, seed SessionTextSeed, planFactory func() (sessionRuntimePlan, error)) (runErr error) {
	source, err := openSessionAudioInput(input)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := source.Close(); closeErr != nil {
			runErr = errors.Join(runErr, closeErr)
		}
	}()
	plan, err := planFactory()
	if err != nil {
		return err
	}
	source.bindRuntime(plan.runtime)
	source.bindProviderRate(plan.inputAudioSampleRate)
	if input.MaxDuration > 0 {
		plan.loop.MaxDuration = input.MaxDuration
	}

	sessionOut := out
	var audioOutput *sessionAudioOutput
	var audioWrapped *sessionAudioOutputInferencer
	if audioOutPath != "" {
		sink, sinkErr := newSessionAudioSinkAtRate(audioOutPath, out, plan.outputAudioSampleRate)
		if sinkErr != nil {
			return fmt.Errorf("--audio-out %q: %w", audioOutPath, sinkErr)
		}
		audioOutput = &sessionAudioOutput{sink: sink, runtime: plan.runtime}
		defer func() {
			if closeErr := audioOutput.close(); closeErr != nil {
				runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", audioOutPath, closeErr))
			}
		}()
		wrapped := newSessionAudioOutputInferencer(plan.inferencer, audioOutput, "", seed.Value)
		plan.inferencer = wrapped
		audioWrapped = wrapped
		plan.loop.AudioOutputError = func() error {
			audioWrapped.wait()
			return audioWrapped.err()
		}
		if audioOutPath == "-" {
			sessionOut = io.Discard
		}
	}

	// A finite audio source is the input lifetime. Do not close immediately on
	// SESSION.OPEN; allow every source frame to reach the loop first.
	plan.loop.CloseAfterOpen = false
	plan.loop.RequireAssistantResponse = true
	plan.loop.AudioIn = source
	runErr = plan.run(ctx, sessionOut)
	if audioWrapped != nil {
		audioWrapped.wait()
		if outputErr := audioWrapped.err(); outputErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", audioOutPath, outputErr))
		}
	}
	return runErr
}

func validateSessionAudioInput(input SessionAudioInput) error {
	if input.DevicePresent {
		return &SessionAudioInputError{
			Kind: SessionAudioInputConflict,
			Path: input.Path,
			Err:  ErrSessionAudioInputConflict,
		}
	}
	if strings.TrimSpace(input.Path) == "" {
		return &SessionAudioInputError{
			Kind: SessionAudioInputEmpty,
			Path: input.Path,
			Err:  ErrSessionAudioInputEmpty,
		}
	}
	return nil
}

// validateSessionAudioInputFileExists is a lightweight existence preflight
// for a file-backed --audio-in path. It intentionally runs before the
// generic validateSessionRunOptions check (which requires --record or
// --replay) so a missing --audio-in file is reported as a missing file
// instead of being masked by that unrelated requirement — a bare `session
// --audio-in <missing file>` invocation would otherwise report "requires
// --record or --replay" and never reach the file-open code that would name
// the real problem. Stdin ("-") and an injected test Source are exempt: there
// is no filesystem path to check.
func validateSessionAudioInputFileExists(input SessionAudioInput) error {
	if input.Source != nil || input.Path == "" || input.Path == "-" {
		return nil
	}
	if _, err := os.Stat(input.Path); err != nil {
		return classifySessionAudioOpenError(input.Path, err)
	}
	return nil
}

func openSessionAudioInput(input SessionAudioInput) (*sessionAudioSource, error) {
	sourceRate := input.SourceSampleRate
	if sourceRate == 0 {
		sourceRate = audio.SampleRate
	}
	if input.Source != nil {
		return &sessionAudioSource{source: input.Source, path: input.Path, sourceRate: sourceRate, send: input.SendAudioInput, endOfTurn: input.SendEndOfTurn}, nil
	}

	if strings.EqualFold(filepath.Ext(input.Path), ".wav") {
		source, err := openSessionWAVSource(input.Path)
		if err != nil {
			return nil, err
		}
		wavRate := sessionAudioSourceSampleRate(source, audio.SampleRate)
		return &sessionAudioSource{source: source, path: input.Path, sourceRate: wavRate, paced: true, send: input.SendAudioInput, endOfTurn: input.SendEndOfTurn}, nil
	}

	stdin := input.Stdin
	var inputReader *sessionAudioReader
	if input.Path == "-" {
		if stdin == nil {
			return nil, classifySessionAudioOpenError(input.Path, audio.ErrNilStream)
		}
		inputReader = newSessionAudioReader(stdin, input.CloseStdinOnCancel)
		stdin = inputReader
	}
	source, err := audio.NewFileSource(input.Path, stdin)
	if err != nil {
		return nil, classifySessionAudioOpenError(input.Path, err)
	}
	if input.Path != "-" {
		info, statErr := os.Stat(input.Path)
		if statErr != nil {
			_ = source.Close()
			return nil, classifySessionAudioOpenError(input.Path, statErr)
		}
		if info.IsDir() {
			_ = source.Close()
			return nil, &SessionAudioInputError{
				Kind: SessionAudioInputUnreadable,
				Path: input.Path,
				Err:  fmt.Errorf("path is a directory; provide a .wav, .pcm, or .raw file"),
			}
		}
	}
	return &sessionAudioSource{source: source, path: input.Path, reader: inputReader, sourceRate: sourceRate, paced: true, send: input.SendAudioInput, endOfTurn: input.SendEndOfTurn}, nil
}

// prepareScheduledAudioInputs loads a finite sequence of audio files for one
// persistent session. Each file becomes one queued user turn; the runtime
// emits its MESSAGE.END boundary after the bytes so the next file is not
// merged into the same provider response.
func prepareScheduledAudioInputs(paths []string) ([]ScheduledAudioInput, error) {
	inputs := make([]ScheduledAudioInput, 0, len(paths))
	for index, path := range paths {
		input := SessionAudioInput{Path: path, Present: true}
		if err := validateSessionAudioInput(input); err != nil {
			return nil, err
		}
		pcm, sourceRate, err := readSessionAudioInputPCM(input)
		if err != nil {
			return nil, fmt.Errorf("load audio turn %d from %q: %w", index+1, path, err)
		}
		if len(pcm) == 0 {
			return nil, fmt.Errorf("load audio turn %d from %q: %w", index+1, path, emptySessionAudioInput(path))
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

// readSessionAudioInputPCM decodes one CLI audio input using the same source
// implementation as --audio-in, but returns its normalized 16 kHz PCM bytes
// for a scheduled persistent-session turn.
func readSessionAudioInputPCM(input SessionAudioInput) (pcm []byte, sourceRate int, runErr error) {
	source, err := openSessionAudioInput(input)
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
		if err := source.source.ReadFrame(context.Background(), frame); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, 0, &SessionAudioInputError{Kind: SessionAudioInputRead, Path: input.Path, Err: err}
		}
		frameBytes := make([]byte, len(frame)*2)
		for index, sample := range frame {
			binary.LittleEndian.PutUint16(frameBytes[index*2:], uint16(sample))
		}
		_, _ = encoded.Write(frameBytes)
	}
	return encoded.Bytes(), source.sourceRate, nil
}

func classifySessionAudioOpenError(path string, err error) error {
	kind := SessionAudioInputUnreadable
	switch {
	case errors.Is(err, audio.ErrUnsupportedFormat):
		kind = SessionAudioInputFormat
	case errors.Is(err, os.ErrNotExist):
		kind = SessionAudioInputMissing
	case errors.Is(err, audio.ErrNilStream):
		kind = SessionAudioInputUnreadable
	}
	return &SessionAudioInputError{Kind: kind, Path: path, Err: err}
}

type sessionAudioSource struct {
	source       audio.AudioSource
	path         string
	sourceRate   int
	providerRate int
	reader       *sessionAudioReader
	// paced marks file-backed finite sources whose frames must be delivered
	// at the encoded real-time rate. Synthetic test sources injected through
	// the SessionAudioInput.Source seam are never paced so tests control
	// their own timing.
	paced     bool
	send      func(context.Context, []byte) error
	endOfTurn func(context.Context) error
	runtime   *sessionRuntimeObservationRecorder
	once      sync.Once
	err       error
}

func (s *sessionAudioSource) bindProviderRate(rate int) {
	if s != nil {
		s.providerRate = rate
	}
}

func (s *sessionAudioSource) bindContext(ctx context.Context) {
	if s.reader != nil {
		s.reader.bindContext(ctx)
	}
}

func (s *sessionAudioSource) bindRuntime(runtime *sessionRuntimeObservationRecorder) {
	if s != nil {
		s.runtime = runtime
	}
}

func (s *sessionAudioSource) Close() error {
	s.once.Do(func() { s.err = s.source.Close() })
	if s.err == nil {
		return nil
	}
	return &SessionAudioInputError{Kind: SessionAudioInputClose, Path: s.path, Err: s.err}
}

// sessionAudioReader carries cancellation into readers that can honor it
// without closing the caller-owned stdin. The standard io.Reader contract has
// no cancellation method, so a reader must implement ReadContext or support
// read deadlines once the session context is bound. Calling an arbitrary
// blocking Read in a helper goroutine would leak that goroutine when stdin is
// caller-owned and cannot be closed.
type sessionAudioReader struct {
	reader        io.Reader
	closeOnCancel bool
	mu            sync.RWMutex
	ctx           context.Context
}

type contextAudioReader interface {
	ReadContext(context.Context, []byte) (int, error)
}

type deadlineAudioReader interface {
	SetReadDeadline(time.Time) error
}

const sessionAudioReadDeadline = 250 * time.Millisecond

func newSessionAudioReader(reader io.Reader, closeOnCancel bool) *sessionAudioReader {
	return &sessionAudioReader{reader: reader, closeOnCancel: closeOnCancel}
}

func (r *sessionAudioReader) bindContext(ctx context.Context) {
	r.mu.Lock()
	r.ctx = ctx
	r.mu.Unlock()
}

func (r *sessionAudioReader) Read(destination []byte) (int, error) {
	r.mu.RLock()
	ctx := r.ctx
	reader := r.reader
	r.mu.RUnlock()
	if ctx == nil {
		return reader.Read(destination)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if cancellable, ok := reader.(contextAudioReader); ok {
		return cancellable.ReadContext(ctx, destination)
	}
	// These standard in-memory readers have a finite, synchronous Read
	// contract and are used by injected command stdin in tests and embedders.
	// They cannot block waiting for more input, so they do not need a helper
	// goroutine or a close operation to make cancellation safe.
	switch reader.(type) {
	case *bytes.Reader, *bytes.Buffer, *strings.Reader:
		return reader.Read(destination)
	}
	deadliner, ok := reader.(deadlineAudioReader)
	if !ok {
		if r.closeOnCancel {
			closer, closeOK := reader.(io.Closer)
			if closeOK {
				return readAudioReaderWithCancellation(ctx, reader, closer, destination)
			}
		}
		return 0, fmt.Errorf("%w: stdin must implement ReadContext or SetReadDeadline", ErrSessionAudioInputUninterruptible)
	}
	if err := deadliner.SetReadDeadline(time.Now().Add(sessionAudioReadDeadline)); err != nil {
		if r.closeOnCancel {
			if closer, closeOK := reader.(io.Closer); closeOK {
				return readAudioReaderWithCancellation(ctx, reader, closer, destination)
			}
		}
		return 0, errors.Join(
			ErrSessionAudioInputUninterruptible,
			fmt.Errorf("stdin read deadline setup failed: %w", err),
		)
	}
	for {
		count, readErr := reader.Read(destination)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return count, ctxErr
		}
		if errors.Is(readErr, os.ErrDeadlineExceeded) && count == 0 {
			if err := deadliner.SetReadDeadline(time.Now().Add(sessionAudioReadDeadline)); err != nil {
				return 0, errors.Join(
					ErrSessionAudioInputUninterruptible,
					fmt.Errorf("stdin read deadline renewal failed: %w", err),
				)
			}
			continue
		}
		_ = deadliner.SetReadDeadline(time.Time{})
		return count, readErr
	}
}

type audioReadResult struct {
	count int
	err   error
}

// readAudioReaderWithCancellation is reserved for descriptors explicitly
// owned by the CLI process. Closing the descriptor is the only portable way
// to interrupt a blocked pipe read on platforms where SetReadDeadline is not
// implemented for os.File pipes.
func readAudioReaderWithCancellation(ctx context.Context, reader io.Reader, closer io.Closer, destination []byte) (int, error) {
	resultCh := make(chan audioReadResult, 1)
	go func() {
		count, err := reader.Read(destination)
		resultCh <- audioReadResult{count: count, err: err}
	}()
	select {
	case result := <-resultCh:
		return result.count, result.err
	case <-ctx.Done():
		_ = closer.Close()
		result := <-resultCh
		if result.count > 0 {
			return result.count, ctx.Err()
		}
		return 0, ctx.Err()
	}
}

func streamSessionAudioInput(ctx context.Context, loop *agentloop.AgentLoop, source *sessionAudioSource) (runErr error) {
	defer func() {
		if closeErr := source.Close(); closeErr != nil {
			runErr = errors.Join(runErr, closeErr)
		}
	}()

	// File-backed finite sources are delivered at their encoded real-time
	// rate. Bursting a whole recording into the session outbound path faster
	// than the provider connection drains it silently drops frames at the
	// bounded session send queue, so the model never hears the complete
	// utterance and end-of-turn fires over truncated audio. Pacing keeps the
	// outbound queue occupancy near zero for the whole file.
	start := time.Now()
	sourceRate := source.sourceRate
	if sourceRate == 0 {
		sourceRate = audio.SampleRate
	}
	providerRate := source.providerRate
	if providerRate == 0 {
		providerRate = sourceRate
	}
	frameDuration := time.Duration(audio.FrameSize) * time.Second / time.Duration(sourceRate)
	framer := newSessionProviderInputFramer(sourceRate, providerRate)

	frame := make([]int16, audio.FrameSize)
	receivedAudio := false
	for frameIndex := 0; ; frameIndex++ {
		if source.paced && frameIndex > 0 {
			target := start.Add(time.Duration(frameIndex) * frameDuration)
			if delay := time.Until(target); delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return &SessionAudioInputError{Kind: SessionAudioInputRead, Path: source.path, Err: ctx.Err()}
				case <-timer.C:
				}
			}
		}
		clear(frame)
		if err := source.source.ReadFrame(ctx, frame); err != nil {
			if errors.Is(err, audio.ErrEndOfTurn) {
				if !receivedAudio {
					return emptySessionAudioInput(source.path)
				}
				if flush := framer.flush(); flush != nil {
					if sendErr := sendSessionAudioFrames(ctx, loop, source, [][]byte{flush}); sendErr != nil {
						return sendErr
					}
				}
				if endErr := sendSessionAudioEndOfTurn(ctx, loop, source); endErr != nil {
					return endErr
				}
				receivedAudio = false
				continue
			}
			if errors.Is(err, io.EOF) {
				if !receivedAudio {
					return emptySessionAudioInput(source.path)
				}
				if flush := framer.flush(); flush != nil {
					if sendErr := sendSessionAudioFrames(ctx, loop, source, [][]byte{flush}); sendErr != nil {
						return sendErr
					}
				}
				return sendSessionAudioEndOfTurn(ctx, loop, source)
			}
			return &SessionAudioInputError{Kind: SessionAudioInputRead, Path: source.path, Err: err}
		}

		pcm := make([]byte, len(frame)*2)
		for i, sample := range frame {
			binary.LittleEndian.PutUint16(pcm[i*2:], uint16(sample))
		}
		providerFrames, err := framer.push(pcm)
		if err != nil {
			return &SessionAudioInputError{Kind: SessionAudioInputFormat, Path: source.path, Err: err}
		}
		receivedAudio = true
		if err := sendSessionAudioFrames(ctx, loop, source, providerFrames); err != nil {
			return err
		}
	}
}

func sendSessionAudioFrames(ctx context.Context, loop *agentloop.AgentLoop, source *sessionAudioSource, frames [][]byte) error {
	for _, pcm := range frames {
		send := source.send
		if send == nil {
			send = loop.SendAudioInput
		}
		if err := send(ctx, pcm); err != nil {
			return &SessionAudioInputError{Kind: SessionAudioInputSend, Path: source.path, Err: err}
		}
		source.runtime.audioInput(pcm)
	}
	return nil
}

// sendSessionAudioEndOfTurn signals end-of-turn after a finite audio source
// is exhausted: MESSAGE.END flows to the realtime provider as
// input_audio_buffer.commit followed by response.create, so the server stops
// waiting for more audio and produces a response.
func sendSessionAudioEndOfTurn(ctx context.Context, loop *agentloop.AgentLoop, source *sessionAudioSource) error {
	send := source.endOfTurn
	if send == nil {
		send = func(ctx context.Context) error {
			return loop.SendSessionEvent(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd})
		}
	}
	if err := send(ctx); err != nil {
		if isSessionCancellation(err) {
			return &SessionAudioInputError{
				Kind: SessionAudioInputSend,
				Path: source.path,
				Err:  fmt.Errorf("%w (%v)", ErrSessionAudioInputEndOfTurnLost, err),
			}
		}
		return &SessionAudioInputError{Kind: SessionAudioInputSend, Path: source.path, Err: fmt.Errorf("end-of-turn signaling: %w", err)}
	}
	return nil
}

// joinSessionTerminationErrors drops expected cancellations and preserves the
// identity of real loop and audio producer failures.
func joinSessionTerminationErrors(runErr, audioErr error) error {
	var errs []error
	if runErr != nil && !isSessionCancellation(runErr) {
		errs = append(errs, fmt.Errorf("session error: %w", runErr))
	}
	if audioErr != nil && !isSessionCancellation(audioErr) {
		errs = append(errs, audioErr)
	}
	return errors.Join(errs...)
}

func isSessionCancellation(err error) bool {
	return err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// shouldStopAudioInputSessionLoop applies audio-aware stop rules. Before the
// end-of-turn signal is accepted, only a provider-initiated SESSION.CLOSE may
// stop the run. Once awaitingResponse is set (end-of-turn delivered after
// local EOF), only a completed non-tool assistant response, a terminal ERROR,
// or a provider SESSION.CLOSE ends the session. When the session has a real
// executor, the provider's tool-call MESSAGE.END and the ToolRunner's
// RoleTool MESSAGE.END are intermediate boundaries and must not stop the
// session before the follow-up assistant response is consumed.
func shouldStopAudioInputSessionLoop(msg messages.StreamMessage, opts sessionLoopOptions, closeSent, awaitingResponse bool) bool {
	if !awaitingResponse {
		return msg.Type == messages.StreamTypeSessionClose
	}
	if msg.Type == messages.StreamTypeMessageEnd && opts.observer != nil {
		if opts.observer.hasTerminalToolContinuationFailure() || opts.observer.hasTerminalScheduledResponseFailure() {
			return true
		}
	}
	if opts.WaitForClose {
		return isTerminalErrorMessage(msg) || msg.Type == messages.StreamTypeSessionClose
	}
	switch msg.Type {
	case messages.StreamTypeMessageEnd:
		if opts.observer != nil && !opts.observer.lastMessageEndAdmitted() {
			return false
		}
		if opts.RequireAssistantResponse {
			if msg.Role == messages.RoleTool || opts.observer == nil || !opts.observer.assistantResponseCompleted() {
				return false
			}
		}
		return true
	case messages.StreamTypeSessionClose:
		return true
	default:
		return isTerminalErrorMessage(msg)
	}
}

// Incremental WAV streaming lives beside the session audio boundary so
// .wav input streams frame-by-frame through the same raw PCM16 path without
// buffering the whole payload. The shared pkg/wavio decoder reads whole files
// and is deliberately not used here for harness-rate input; only non-harness
// rates (24 kHz) are decoded fully once so they can be resampled to the
// harness rate before streaming.
const (
	sessionWAVDescriptorBytes  = 12
	sessionWAVChunkHeaderBytes = 8
	sessionWAVFmtChunkMinBytes = 16
	sessionWAVAudioFormatPCM   = 1
	sessionWAVBitsPerSample    = 16
)

// sessionWAVSource streams PCM16 frames from a RIFF WAVE file incrementally.
// Header chunks are parsed once at open; data-chunk bytes are read one frame
// at a time by ReadFrame.
type sessionWAVSource struct {
	path       string
	file       io.ReadSeekCloser
	remaining  int64
	sampleRate int
	done       bool
	closed     bool
	mu         sync.Mutex
	closeOnce  sync.Once
	closeErr   error
}

var _ audio.AudioSource = (*sessionWAVSource)(nil)

func (s *sessionWAVSource) SampleRate() int {
	if s == nil {
		return 0
	}
	return s.sampleRate
}

type sessionAudioRateSource interface {
	SampleRate() int
}

func sessionAudioSourceSampleRate(source audio.AudioSource, fallback int) int {
	if rated, ok := source.(sessionAudioRateSource); ok && rated.SampleRate() > 0 {
		return rated.SampleRate()
	}
	return fallback
}

func sessionWAVFormatError(path string, reason string) error {
	return &SessionAudioInputError{
		Kind: SessionAudioInputFormat,
		Path: path,
		Err: &audio.FormatError{
			Path:      path,
			Extension: ".wav",
			Format:    "wav",
			Reason:    reason,
		},
	}
}

// openSessionWAVSource validates the RIFF/fmt contract before returning so
// every rejected format fails during preflight with zero delivered frames.
func openSessionWAVSource(path string) (audio.AudioSource, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, classifySessionAudioOpenError(path, err)
	}
	source, err := newSessionWAVSource(path, file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return source, nil
}

// newSessionWAVSource parses the RIFF descriptor and chunk headers from r and
// returns a source positioned at the first data-chunk byte. Harness-rate
// payloads stream frame by frame; 24 kHz payloads are decoded once and
// resampled to the harness rate. The payload is otherwise never read here.
func newSessionWAVSource(path string, r io.ReadSeekCloser) (audio.AudioSource, error) {
	fail := func(err error) (*sessionWAVSource, error) {
		return nil, err
	}

	descriptor := make([]byte, sessionWAVDescriptorBytes)
	if _, err := io.ReadFull(r, descriptor); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return fail(sessionWAVFormatError(path, "file is too short for a RIFF WAVE descriptor"))
		}
		return fail(classifySessionAudioOpenError(path, err))
	}
	if string(descriptor[0:4]) != "RIFF" || string(descriptor[8:12]) != "WAVE" {
		return fail(sessionWAVFormatError(path, `missing RIFF/WAVE descriptor`))
	}

	fmtSeen := false
	var fmtRate uint32
	skip := func(count int64) error {
		if count <= 0 {
			return nil
		}
		_, err := r.Seek(count, io.SeekCurrent)
		return err
	}
	for {
		var header [sessionWAVChunkHeaderBytes]byte
		if _, err := io.ReadFull(r, header[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return fail(sessionWAVFormatError(path, "missing fmt or data chunk"))
			}
			return fail(classifySessionAudioOpenError(path, err))
		}
		id := string(header[0:4])
		size := int64(binary.LittleEndian.Uint32(header[4:8]))
		switch id {
		case "fmt ":
			if size < sessionWAVFmtChunkMinBytes {
				return fail(sessionWAVFormatError(path, "fmt chunk is truncated"))
			}
			body := make([]byte, sessionWAVFmtChunkMinBytes)
			if _, err := io.ReadFull(r, body); err != nil {
				return fail(sessionWAVFormatError(path, "fmt chunk is truncated"))
			}
			compression := binary.LittleEndian.Uint16(body[0:2])
			channels := binary.LittleEndian.Uint16(body[2:4])
			rate := binary.LittleEndian.Uint32(body[4:8])
			bits := binary.LittleEndian.Uint16(body[14:16])
			switch {
			case compression != sessionWAVAudioFormatPCM:
				return fail(sessionWAVFormatError(path, fmt.Sprintf("WAV compression format %d is not PCM", compression)))
			case channels != audio.Channels:
				return fail(sessionWAVFormatError(path, fmt.Sprintf("channel count is %d; want exactly %d", channels, audio.Channels)))
			case rate != audio.SampleRate && rate != wavio.Rate24kHz:
				return fail(sessionWAVFormatError(path, fmt.Sprintf("sample rate is %d Hz; want exactly %d Hz or %d Hz", rate, audio.SampleRate, wavio.Rate24kHz)))
			case bits != sessionWAVBitsPerSample:
				return fail(sessionWAVFormatError(path, fmt.Sprintf("bit depth is %d; want exactly %d-bit PCM", bits, sessionWAVBitsPerSample)))
			}
			fmtSeen = true
			fmtRate = rate
			if err := skip(size - sessionWAVFmtChunkMinBytes); err != nil {
				return fail(classifySessionAudioOpenError(path, err))
			}
		case "data":
			if !fmtSeen {
				return fail(sessionWAVFormatError(path, "data chunk appears before fmt chunk"))
			}
			switch {
			case fmtRate == audio.SampleRate || fmtRate == wavio.Rate24kHz:
				return &sessionWAVSource{path: path, file: r, remaining: size, sampleRate: int(fmtRate)}, nil
			default:
				return fail(sessionWAVFormatError(path, fmt.Sprintf("sample rate is %d Hz; want exactly %d Hz or %d Hz", fmtRate, audio.SampleRate, wavio.Rate24kHz)))
			}
		default:
			if err := skip(size); err != nil {
				return fail(classifySessionAudioOpenError(path, err))
			}
		}
		// RIFF chunks are word aligned: odd lengths carry one pad byte.
		if size%2 == 1 {
			if err := skip(1); err != nil {
				return fail(classifySessionAudioOpenError(path, err))
			}
		}
	}
}

// sessionDecodedWAVSource serves a fully decoded, harness-rate sample buffer
// through the AudioSource contract. It mirrors sessionWAVSource semantics:
// ReadFrame zero-pads a final short frame and returns io.EOF once the payload
// is exhausted.
type sessionDecodedWAVSource struct {
	path     string
	samples  []int16
	position int
	done     bool
	closed   bool
	mu       sync.Mutex
}

var _ audio.AudioSource = (*sessionDecodedWAVSource)(nil)

func (s *sessionDecodedWAVSource) ReadFrame(ctx context.Context, buf []int16) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if len(buf) != audio.FrameSize {
		return &audio.FrameSizeError{Operation: "read", Got: len(buf), Want: audio.FrameSize}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return &audio.ClosedError{Operation: "read", Path: s.path}
	}
	if s.done {
		return io.EOF
	}
	clear(buf)
	copy(buf, s.samples[s.position:])
	s.position += audio.FrameSize
	if s.position >= len(s.samples) {
		s.done = true
	}
	return nil
}

// Close marks the decoded source closed. It is safe to call more than once.
func (s *sessionDecodedWAVSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.done = true
	return nil
}

// ReadFrame fills buf with the next data-chunk frame, zero-padding a final
// short frame. Once the payload is exhausted it returns io.EOF. Each call
// consumes at most FrameSize*2 payload bytes, never the remaining file.
func (s *sessionWAVSource) ReadFrame(ctx context.Context, buf []int16) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if len(buf) != audio.FrameSize {
		return &audio.FrameSizeError{Operation: "read", Got: len(buf), Want: audio.FrameSize}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return &audio.ClosedError{Operation: "read", Path: s.path}
	}
	if s.done {
		return io.EOF
	}

	want := int64(audio.FrameSize * 2)
	count := want
	if s.remaining < count {
		count = s.remaining
	}
	encoded := make([]byte, count)
	if _, err := io.ReadFull(s.file, encoded); err != nil {
		s.done = true
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return &audio.TruncatedPCMError{Path: s.path, Bytes: int(count % 2)}
		}
		return &SessionAudioInputError{Kind: SessionAudioInputRead, Path: s.path, Err: err}
	}
	s.remaining -= count
	if count%2 != 0 {
		s.done = true
		return &audio.TruncatedPCMError{Path: s.path, Bytes: 1}
	}
	clear(buf)
	for index := range int(count) / 2 {
		buf[index] = int16(binary.LittleEndian.Uint16(encoded[index*2:]))
	}
	if s.remaining == 0 {
		s.done = true
	}
	return nil
}

// Close releases the owned file. It is safe to call more than once.
func (s *sessionWAVSource) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.done = true
		err := s.file.Close()
		s.mu.Unlock()
		if err != nil {
			s.closeErr = &SessionAudioInputError{Kind: SessionAudioInputClose, Path: s.path, Err: err}
		}
	})
	return s.closeErr
}
