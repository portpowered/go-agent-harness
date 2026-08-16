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
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// SessionAudioInput carries the command-line presence bit separately from the
// value so --audio-in= can be rejected instead of treated as an omitted flag.
type SessionAudioInput struct {
	Path  string
	Stdin io.Reader
	// MaxDuration bounds an audio-enabled session when the shared duration
	// wrapper cannot own this parallel audio runner.
	MaxDuration time.Duration
	// Source and SendAudioInput are optional deterministic service-test seams.
	// CLI callers leave them nil so paths use FileSource and frames use the
	// AgentLoop's SendAudioInput method.
	Source         audio.AudioSource
	SendAudioInput func(context.Context, []byte) error
	Present        bool
	DevicePresent  bool
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
	ErrSessionAudioInputEmpty      = errors.New("session audio input path is empty")
	ErrSessionAudioInputMissing    = errors.New("session audio input is missing")
	ErrSessionAudioInputUnreadable = errors.New("session audio input is unreadable")
	ErrSessionAudioInputFormat     = errors.New("session audio input format is unsupported")
	ErrSessionAudioInputConflict   = errors.New("--audio-in and --audio-in-device (audio device input) cannot be used together")
	ErrSessionAudioInputRead       = errors.New("session audio input read failed")
	ErrSessionAudioInputSend       = errors.New("session audio input send failed")
	ErrSessionAudioInputClose      = errors.New("session audio input close failed")
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

// RunSessionWithAudioInput runs an existing session runtime while streaming
// the selected file or raw stdin through the agent loop's session audio inbox.
// The ordinary session path remains untouched when the flag is absent.
func RunSessionWithAudioInput(ctx context.Context, out io.Writer, opts SessionRunOptions, input SessionAudioInput) (runErr error) {
	present := input.Present || input.Path != ""
	if !present {
		return RunSession(ctx, out, opts)
	}

	if err := validateSessionAudioInput(input); err != nil {
		return err
	}
	return runSessionAudioInputPlan(ctx, out, input, func() (sessionRuntimePlan, error) {
		if err := validateSessionRunOptions(opts); err != nil {
			return sessionRuntimePlan{}, err
		}
		return planSessionRuntime(opts)
	})
}

// RunSessionWithAudioInputAndTextSeed composes the session text-seed and audio
// input extensions when both flags are available on the command surface.
// Keeping the no-audio branch on RunSessionWithTextSeed preserves its explicit
// empty-prompt semantics for the already-merged text-seed lane.
func RunSessionWithAudioInputAndTextSeed(ctx context.Context, out io.Writer, opts SessionRunOptions, seed SessionTextSeed, input SessionAudioInput) error {
	if !input.Present && input.Path == "" {
		return RunSessionWithTextSeed(ctx, out, opts, seed)
	}
	if seed.Present {
		opts.Prompt = seed.Value
	}
	return RunSessionWithAudioInput(ctx, out, opts, input)
}

// RunSessionWithAudioInputAndTextSeedAndMaxDuration preserves the merged
// session duration behavior when audio input is absent and applies the same
// bound to the audio runner when the audio extension is selected.
func RunSessionWithAudioInputAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration, seed SessionTextSeed, input SessionAudioInput) error {
	if err := ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	if !input.Present && input.Path == "" {
		return RunSessionWithTextSeedAndMaxDuration(ctx, out, opts, maxDuration, seed)
	}
	input.MaxDuration = maxDuration
	return RunSessionWithAudioInputAndTextSeed(ctx, out, opts, seed, input)
}

// RunSessionWithInstructionsAndAudioInputAndTextSeedAndMaxDuration composes
// the instructions, text-seed, duration, and audio-input extensions. The
// no-audio path remains on the established instructions entry point so its
// replay and duration artifact behavior stays unchanged.
func RunSessionWithInstructionsAndAudioInputAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration, seed SessionTextSeed, input SessionAudioInput, systemPrompt string) error {
	if err := ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	if !input.Present && input.Path == "" {
		return RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration(ctx, out, opts, "", maxDuration, seed, systemPrompt)
	}
	if systemPrompt == "" {
		return RunSessionWithAudioInputAndTextSeedAndMaxDuration(ctx, out, opts, maxDuration, seed, input)
	}
	input.MaxDuration = maxDuration
	if seed.Present {
		opts.Prompt = seed.Value
	}
	if err := validateSessionAudioInput(input); err != nil {
		return err
	}
	if opts.ReplayPath != "" && opts.SessionInferencer == nil {
		return RunSessionWithAudioInput(ctx, out, opts, input)
	}
	return runSessionAudioInputPlan(ctx, out, input, func() (sessionRuntimePlan, error) {
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

func runSessionAudioInputPlan(ctx context.Context, out io.Writer, input SessionAudioInput, planFactory func() (sessionRuntimePlan, error)) (runErr error) {
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
	if input.MaxDuration > 0 {
		plan.loop.MaxDuration = input.MaxDuration
	}
	// A finite audio source is the input lifetime. Do not close immediately on
	// SESSION.OPEN; allow every source frame to reach the loop first.
	plan.loop.CloseAfterOpen = false
	return runSessionPlanWithAudioInput(ctx, out, plan, source)
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
	if input.Source == nil && input.Path != "-" && strings.EqualFold(filepath.Ext(input.Path), ".wav") {
		return &SessionAudioInputError{
			Kind: SessionAudioInputFormat,
			Path: input.Path,
			Err:  fmt.Errorf("%w: .wav input is not incrementally supported by this command; use .pcm, .raw, or -", audio.ErrUnsupportedFormat),
		}
	}
	return nil
}

func openSessionAudioInput(input SessionAudioInput) (*sessionAudioSource, error) {
	if input.Source != nil {
		return &sessionAudioSource{source: input.Source, path: input.Path, send: input.SendAudioInput}, nil
	}

	stdin := input.Stdin
	var inputReader *sessionAudioReader
	if input.Path == "-" {
		if stdin == nil {
			return nil, classifySessionAudioOpenError(input.Path, audio.ErrNilStream)
		}
		inputReader = newSessionAudioReader(stdin)
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
				Err:  fmt.Errorf("path is a directory; provide a .pcm or .raw file"),
			}
		}
	}
	return &sessionAudioSource{source: source, path: input.Path, reader: inputReader, send: input.SendAudioInput}, nil
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
	source audio.AudioSource
	path   string
	reader *sessionAudioReader
	send   func(context.Context, []byte) error
	once   sync.Once
	err    error
}

func (s *sessionAudioSource) bindContext(ctx context.Context) {
	if s.reader != nil {
		s.reader.bindContext(ctx)
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
// no cancellation method, so a reader may optionally implement ReadContext;
// os.File-style deadline readers are handled as a bounded fallback.
type sessionAudioReader struct {
	reader io.Reader
	mu     sync.RWMutex
	ctx    context.Context
}

type contextAudioReader interface {
	ReadContext(context.Context, []byte) (int, error)
}

type deadlineAudioReader interface {
	SetReadDeadline(time.Time) error
}

const sessionAudioReadDeadline = 250 * time.Millisecond

func newSessionAudioReader(reader io.Reader) *sessionAudioReader {
	return &sessionAudioReader{reader: reader}
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
	if deadliner, ok := reader.(deadlineAudioReader); ok {
		if err := deadliner.SetReadDeadline(time.Now().Add(sessionAudioReadDeadline)); err == nil {
			for {
				count, readErr := reader.Read(destination)
				if ctxErr := ctx.Err(); ctxErr != nil {
					return count, ctxErr
				}
				if errors.Is(readErr, os.ErrDeadlineExceeded) && count == 0 {
					if err := deadliner.SetReadDeadline(time.Now().Add(sessionAudioReadDeadline)); err != nil {
						return 0, err
					}
					continue
				}
				_ = deadliner.SetReadDeadline(time.Time{})
				return count, readErr
			}
		}
	}
	return reader.Read(destination)
}

func streamSessionAudioInput(ctx context.Context, loop *agentloop.AgentLoop, source *sessionAudioSource) (runErr error) {
	defer func() {
		if closeErr := source.Close(); closeErr != nil {
			runErr = errors.Join(runErr, closeErr)
		}
	}()

	frame := make([]int16, audio.FrameSize)
	for {
		clear(frame)
		if err := source.source.ReadFrame(ctx, frame); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return &SessionAudioInputError{Kind: SessionAudioInputRead, Path: source.path, Err: err}
		}

		pcm := make([]byte, len(frame)*2)
		for i, sample := range frame {
			binary.LittleEndian.PutUint16(pcm[i*2:], uint16(sample))
		}
		send := source.send
		if send == nil {
			send = loop.SendAudioInput
		}
		if err := send(ctx, pcm); err != nil {
			return &SessionAudioInputError{Kind: SessionAudioInputSend, Path: source.path, Err: err}
		}
	}
}

func runSessionPlanWithAudioInput(ctx context.Context, out io.Writer, plan sessionRuntimePlan, source *sessionAudioSource) error {
	if plan.announce != "" {
		if _, err := fmt.Fprintln(out, plan.announce); err != nil {
			return err
		}
	}

	loopOut := out
	if plan.loopOut != nil {
		loopOut = plan.loopOut
	}
	if plan.inferencer != nil {
		if err := runAgentLoopSessionWithAudioInput(ctx, loopOut, plan.inferencer, plan.loop, source); err != nil {
			if plan.flushCapture != nil {
				flushErr := plan.flushCapture()
				return wrapSessionRuntimeError(plan, errors.Join(
					wrapSessionPhaseError("run session loop", err),
					wrapSessionPhaseError("flush capture", flushErr),
				))
			}
			return wrapSessionRuntimeError(plan, err)
		}
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

func runAgentLoopSessionWithAudioInput(ctx context.Context, out io.Writer, sessionInferencer messages.SessionInferencer, opts sessionLoopOptions, source *sessionAudioSource) error {
	observedInferencer := newObservedSessionInferencer(sessionInferencer)
	loop, err := agentloop.New(
		agentloop.WithMode(engine.DuplexSession),
		agentloop.WithSessionInferencer(observedInferencer),
	)
	if err != nil {
		return fmt.Errorf("create session agent loop: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	source.bindContext(runCtx)
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- loop.Run(runCtx) }()

	var runErr error
	runDone := false
	waitRun := func() error {
		if !runDone {
			runErr = <-runErrCh
			runDone = true
		}
		return runErr
	}
	var audioCh <-chan error
	startAudio := func() {
		if audioCh != nil {
			return
		}
		audioErrCh := make(chan error, 1)
		audioCh = audioErrCh
		go func() { audioErrCh <- streamSessionAudioInput(runCtx, loop, source) }()
	}
	waitAudio := func() error {
		if audioCh == nil {
			return nil
		}
		err := <-audioCh
		audioCh = nil
		return err
	}
	stop := func() error {
		cancel()
		return joinSessionAudioTerminationErrors(waitRun(), waitAudio())
	}

	timeout := make(<-chan time.Time)
	if opts.MaxDuration > 0 {
		timeout = time.After(opts.MaxDuration)
	}
	promptSent := false
	closeSent := false
	audioDone := false
	done := opts.Done
	for {
		select {
		case audioErr := <-audioCh:
			audioCh = nil
			if audioErr != nil && !isSessionCancellation(audioErr) {
				cancel()
				return errors.Join(audioErr, joinSessionAudioTerminationErrors(waitRun(), nil))
			}
			audioDone = true
		case <-done:
			doneErr := error(nil)
			if opts.DoneErr != nil {
				doneErr = opts.DoneErr()
			}
			var initialDrainErr error
			if doneErr == nil {
				initialDrainErr = drainSessionLoopMessagesUntilIdle(out, loop, sessionReplayDoneDrainIdleDelay)
			}
			stopErr := stop()
			if drainErr := drainSessionLoopMessages(out, loop); drainErr != nil {
				stopErr = errors.Join(stopErr, drainErr)
			}
			if initialDrainErr != nil {
				stopErr = errors.Join(stopErr, initialDrainErr)
			}
			if doneErr != nil {
				stopErr = errors.Join(stopErr, doneErr)
			}
			return stopErr
		case <-timeout:
			stopErr := stop()
			if drainErr := drainSessionLoopMessages(out, loop); drainErr != nil {
				stopErr = errors.Join(stopErr, drainErr)
			}
			return stopErr
		case <-ctx.Done():
			stopErr := stop()
			if stopErr != nil {
				return stopErr
			}
			return ctx.Err()
		case <-observedInferencer.Done():
			drainErr := drainSessionLoopMessagesUntilQuiet(out, loop, 25*time.Millisecond)
			stopErr := stop()
			if drainErr != nil {
				stopErr = errors.Join(stopErr, drainErr)
			}
			if drainErr := drainSessionLoopMessages(out, loop); drainErr != nil {
				stopErr = errors.Join(stopErr, drainErr)
			}
			return stopErr
		case err := <-runErrCh:
			runErr = err
			runDone = true
			cancel()
			stopErr := joinSessionAudioTerminationErrors(runErr, waitAudio())
			if drainErr := drainSessionLoopMessages(out, loop); drainErr != nil {
				stopErr = errors.Join(stopErr, drainErr)
			}
			return stopErr
		case msg := <-loop.Deltas().Chan():
			if err := writeSessionReplayMessage(out, msg); err != nil {
				return errors.Join(err, stop())
			}
			if msg.Type == messages.StreamTypeSessionOpen {
				if opts.Prompt != "" && !promptSent {
					promptSent = true
					if err := loop.Send(runCtx, []messages.Message{messages.NewTextMessage(messages.RoleUser, opts.Prompt)}); err != nil {
						return errors.Join(fmt.Errorf("send session message: %w", err), stop())
					}
				}
				if opts.CloseAfterOpen && opts.Prompt == "" && !closeSent {
					closeSent = true
					if err := sendSessionClose(runCtx, loop); err != nil {
						return errors.Join(err, stop())
					}
				}
				startAudio()
			}
			if opts.CloseAfterOpen && opts.Prompt != "" && msg.Type == messages.StreamTypeMessageEnd && !closeSent {
				closeSent = true
				if err := sendSessionClose(runCtx, loop); err != nil {
					return errors.Join(err, stop())
				}
			}
			if shouldStopAudioInputSessionLoop(msg, opts, closeSent, audioDone) {
				stopErr := stop()
				if drainErr := drainSessionLoopMessages(out, loop); drainErr != nil {
					stopErr = errors.Join(stopErr, drainErr)
				}
				return stopErr
			}
		}
	}
}

func joinSessionAudioTerminationErrors(runErr, audioErr error) error {
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

func shouldStopAudioInputSessionLoop(msg messages.StreamMessage, opts sessionLoopOptions, closeSent, audioDone bool) bool {
	if !audioDone {
		return msg.Type == messages.StreamTypeSessionClose
	}
	return shouldStopSessionLoop(msg, opts, closeSent)
}
