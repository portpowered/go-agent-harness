package agentruntime

import devicecontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/devices"

import sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// PrepareRuntimeAudioInputs loads a finite set of audio files using the same
// decoder and PCM contract as --audio-in-turn. The returned inputs are ready
// for an event-driven interruption source; no provider or browser is touched.
func PrepareRuntimeAudioInputs(paths []string) ([]ScheduledAudioInput, error) {
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
//
//nolint:contextcheck // nil parent is normalized for compatibility.
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
	go releaseScheduledAudioInputs(ctx, cancel, events, toolName, cloned, out)
	return out, cancel
}

func releaseScheduledAudioInputs(ctx context.Context, cancel context.CancelFunc, events <-chan webmcp.BrokerEvent, toolName string, inputs []ScheduledAudioInput, out chan<- ScheduledAudioInput) {
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
			if !isScheduledAudioRelease(event, toolName) {
				continue
			}
			if !sendScheduledAudioInputs(ctx, inputs, out) {
				return
			}
			return
		}
	}
}

func isScheduledAudioRelease(event webmcp.BrokerEvent, toolName string) bool {
	return event.Type == webmcp.BrokerEventInvocationCreated && event.State == webmcp.InvocationDispatched && event.InvocationID != "" && event.ToolName != "" && (toolName == "" || event.ToolName == toolName)
}

func sendScheduledAudioInputs(ctx context.Context, inputs []ScheduledAudioInput, out chan<- ScheduledAudioInput) bool {
	for _, input := range inputs {
		select {
		case out <- input:
		case <-ctx.Done():
			return false
		}
	}
	return true
}

// RuntimeAudioInput carries the command-line presence bit separately from the
// value so --audio-in= can be rejected instead of treated as an omitted flag.
type RuntimeAudioInput struct {
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
	SendEndOfTurn         func(context.Context) error
	EmitBoundaryOnSilence bool
	Present               bool
	DevicePresent         bool
}

// RuntimeAudioInputErrorKind identifies the failed session audio boundary.
type RuntimeAudioInputErrorKind string

const runtimeNilText = "<nil>"

const (
	RuntimeAudioInputEmpty      RuntimeAudioInputErrorKind = "empty"
	RuntimeAudioInputMissing    RuntimeAudioInputErrorKind = "missing"
	RuntimeAudioInputUnreadable RuntimeAudioInputErrorKind = "unreadable"
	RuntimeAudioInputFormat     RuntimeAudioInputErrorKind = "format"
	RuntimeAudioInputConflict   RuntimeAudioInputErrorKind = "conflict"
	RuntimeAudioInputRead       RuntimeAudioInputErrorKind = "read"
	RuntimeAudioInputSend       RuntimeAudioInputErrorKind = "send"
	RuntimeAudioInputClose      RuntimeAudioInputErrorKind = "close"
)

type runtimeAudioInputCode string

func (e runtimeAudioInputCode) Error() string { return string(e) }

const (
	ErrRuntimeAudioInputEmpty           runtimeAudioInputCode = "session audio input path is empty"
	ErrRuntimeAudioInputMissing         runtimeAudioInputCode = "session audio input is missing"
	ErrRuntimeAudioInputUnreadable      runtimeAudioInputCode = "session audio input is unreadable"
	ErrRuntimeAudioInputFormat          runtimeAudioInputCode = "session audio input format is unsupported"
	ErrRuntimeAudioInputConflict                              = devicecontract.ErrSessionAudioInputConflict
	ErrRuntimeAudioInputRead            runtimeAudioInputCode = "session audio input read failed"
	ErrRuntimeAudioInputSend            runtimeAudioInputCode = "session audio input send failed"
	ErrRuntimeAudioInputClose           runtimeAudioInputCode = "session audio input close failed"
	ErrRuntimeAudioInputUninterruptible runtimeAudioInputCode = "session audio input reader cannot be interrupted safely"
	// ErrRuntimeAudioInputEndOfTurnLost reports that a cancellation raced the
	// end-of-turn signal, so commit + response.create may never have reached
	// the provider. It is surfaced as an explicit failure instead of being
	// silently swallowed as an ordinary session cancellation.
	ErrRuntimeAudioInputEndOfTurnLost runtimeAudioInputCode = "end-of-turn signal was not delivered before session shutdown"
)

// RuntimeAudioInputError adds the command boundary and preserves the
// underlying audio, filesystem, context, and loop error identity.
type RuntimeAudioInputError struct {
	Kind RuntimeAudioInputErrorKind
	Path string
	Err  error
}

func (e *RuntimeAudioInputError) Error() string {
	if e == nil {
		return runtimeNilText
	}
	if e.Err == nil {
		return fmt.Sprintf("agent session --audio-in %q: %s", e.Path, e.Kind)
	}
	return fmt.Sprintf("agent session --audio-in %q: %s: %v", e.Path, e.Kind, e.Err)
}

func (e *RuntimeAudioInputError) Unwrap() error {
	if e == nil {
		return nil
	}
	return errors.Join(runtimeAudioInputKindError(e.Kind), e.Err)
}

func emptyRuntimeAudioInput(path string) error {
	return &RuntimeAudioInputError{
		Kind: RuntimeAudioInputEmpty,
		Path: path,
		Err:  fmt.Errorf("no audio frames were sent; refusing to commit an empty user turn: %w", ErrRuntimeAudioInputEmpty),
	}
}

func runtimeAudioInputKindError(kind RuntimeAudioInputErrorKind) error {
	switch kind {
	case RuntimeAudioInputEmpty:
		return ErrRuntimeAudioInputEmpty
	case RuntimeAudioInputMissing:
		return ErrRuntimeAudioInputMissing
	case RuntimeAudioInputUnreadable:
		return ErrRuntimeAudioInputUnreadable
	case RuntimeAudioInputFormat:
		return ErrRuntimeAudioInputFormat
	case RuntimeAudioInputConflict:
		return ErrRuntimeAudioInputConflict
	case RuntimeAudioInputRead:
		return ErrRuntimeAudioInputRead
	case RuntimeAudioInputSend:
		return ErrRuntimeAudioInputSend
	case RuntimeAudioInputClose:
		return ErrRuntimeAudioInputClose
	default:
		return nil
	}
}

// RunSessionWithAudioInput runs the shared session runtime while streaming the
// selected file or raw stdin through the agent loop's session audio inbox.
// The ordinary session path remains untouched when the flag is absent.
func RunSessionWithAudioInput(ctx context.Context, out io.Writer, opts SessionRunOptions, input RuntimeAudioInput) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	if !runtimeAudioInputSelected(input) {
		return RunSession(ctx, out, opts)
	}
	if err := validateRuntimeAudioInput(input); err != nil {
		return err
	}
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, claim.release()) }()
	opts.ClientOwnsAudioTurnBoundaries = true
	return runSessionWithAudioInputPlan(ctx, out, input, "", SessionTextSeed{}, func() (sessionRuntimePlan, error) { //nolint:contextcheck // legacy planner compatibility seam has no context parameter.
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
func RunSessionWithInstructionsAndAudioInputAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration, seed SessionTextSeed, input RuntimeAudioInput, systemPrompt string) error {
	return RunSessionWithInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration(ctx, out, opts, "", maxDuration, seed, input, systemPrompt)
}

// RunSessionWithInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration
// composes the instructions, text-seed, duration, audio-input, and
// audio-output extensions on the command surface. An empty audioOutPath
// preserves the established audio-input-only behavior.
func RunSessionWithInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, audioOutPath string, maxDuration time.Duration, seed SessionTextSeed, input RuntimeAudioInput, systemPrompt string) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	return runSessionWithAudioInputAndOutput(ctx, out, opts, audioOutPath, maxDuration, seed, input, systemPrompt)
}

func runSessionWithAudioInputAndOutput(ctx context.Context, out io.Writer, opts SessionRunOptions, audioOutPath string, maxDuration time.Duration, seed SessionTextSeed, input RuntimeAudioInput, systemPrompt string) (runErr error) {
	if err := sessioncontract.ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	if !runtimeAudioInputSelected(input) {
		return RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration(ctx, out, opts, audioOutPath, maxDuration, seed, systemPrompt)
	}
	prepareAudioInputOptions(&opts, &input, audioOutPath, maxDuration, seed)
	if err := validateRuntimeAudioInput(input); err != nil {
		return err
	}
	if err := validateRuntimeAudioInputFileExists(input); err != nil {
		return err
	}
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, claim.release()) }()
	return runSessionWithAudioInputPlan(ctx, out, input, audioOutPath, seed, audioInputPlanFactory(ctx, opts, systemPrompt))
}

func prepareAudioInputOptions(opts *SessionRunOptions, input *RuntimeAudioInput, audioOutPath string, maxDuration time.Duration, seed SessionTextSeed) {
	if audioOutPath != "" {
		opts.AudioOutputRequested = true
		input.EmitBoundaryOnSilence = true
	}
	if seed.Present {
		opts.Prompt = seed.Value
		opts.PromptProvided = true
	}
	input.MaxDuration = maxDuration
	opts.ClientOwnsAudioTurnBoundaries = true
}

func audioInputPlanFactory(ctx context.Context, opts SessionRunOptions, systemPrompt string) func() (sessionRuntimePlan, error) {
	if opts.ReplayPath != "" && (opts.SessionInferencer == nil || strings.TrimSpace(systemPrompt) == "") {
		return func() (sessionRuntimePlan, error) { return planSessionRuntime(opts) } //nolint:contextcheck // legacy planner compatibility seam has no context parameter.
	}
	return func() (sessionRuntimePlan, error) {
		instructions, err := resolveSessionInstructionsContext(ctx, opts, systemPrompt)
		if err != nil {
			return sessionRuntimePlan{}, err
		}
		return planSessionWithResolvedInstructions(opts, instructions) //nolint:contextcheck // legacy planner compatibility seam has no context parameter.
	}
}

func runtimeAudioInputSelected(input RuntimeAudioInput) bool {
	return input.Present || input.Path != ""
}

// runSessionWithAudioInputPlan validates and opens the audio input before the
// plan is built so every preflight failure happens before any provider dial,
// then hands the opened source to the shared session lifecycle through the
// loop options. A non-empty audioOutPath additionally records assistant audio
// received after the end-of-turn commit. No session behavior changes when the
// flag is absent.
func runSessionWithAudioInputPlan(ctx context.Context, out io.Writer, input RuntimeAudioInput, audioOutPath string, seed SessionTextSeed, planFactory func() (sessionRuntimePlan, error)) (runErr error) {
	source, err := openRuntimeAudioInput(input)
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
	source.bindRuntime(plan.runtime, plan.clockSource)
	source.bindProviderRate(plan.inputAudioSampleRate)
	if input.MaxDuration > 0 {
		plan.loop.MaxDuration = input.MaxDuration
	}

	sessionOut := out
	var audioOutput *runtimeAudioOutput
	var audioWrapped *runtimeAudioOutputInferencer
	if audioOutPath != "" {
		var sinkErr error
		audioOutput, sinkErr = newRuntimeAudioOutputForPlanContext(ctx, &plan, audioOutPath, out, nil)
		if sinkErr != nil {
			return fmt.Errorf("--audio-out %q: %w", audioOutPath, sinkErr)
		}
		defer func() {
			if closeErr := audioOutput.close(); closeErr != nil {
				runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", audioOutPath, closeErr))
			}
		}()
		wrapped := newRuntimeAudioOutputInferencer(plan.inferencer, audioOutput, "", seed.Value)
		plan.inferencer = wrapped
		audioWrapped = wrapped
		plan.loop.AudioOutputError = func() error {
			audioWrapped.wait()
			return joinSessionAudioOutputError(nil, audioOutPath, audioWrapped.err())
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
		runErr = joinSessionAudioOutputError(runErr, audioOutPath, audioWrapped.err())
	}
	return runErr
}
