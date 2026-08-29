package probe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultDuplexSampleRate is the PCM16 rate accepted by agent session's
	// raw --audio-in boundary.
	DefaultDuplexSampleRate = 16000
	// DefaultDuplexFrameSamples matches the shipped session audio source.
	DefaultDuplexFrameSamples = 480
	// DefaultDuplexFrameDuration is 30 ms at the standard session rate.
	DefaultDuplexFrameDuration = 30 * time.Millisecond
	// DefaultDuplexCaptureLimit bounds each captured child stream while the
	// pump continues draining the pipe after the limit is reached.
	DefaultDuplexCaptureLimit int64 = 16 << 20
	// DefaultDuplexShutdownGrace bounds each process/pump cleanup wait.
	DefaultDuplexShutdownGrace = 2 * time.Second
)

var (
	ErrDuplexConfigInvalid         = errors.New("duplex session configuration is invalid")
	ErrDuplexInputInvalid          = errors.New("duplex session PCM16 input is invalid")
	ErrDuplexOutputCaptureLimit    = errors.New("duplex session output capture limit exceeded")
	ErrDuplexProcessStart          = errors.New("duplex session child process failed to start")
	ErrDuplexProcessExit           = errors.New("duplex session child process exited unsuccessfully")
	ErrDuplexInputIncomplete       = errors.New("duplex session child exited before input completed")
	ErrDuplexDeadline              = errors.New("duplex session reached its deadline")
	ErrDuplexChildSurvivedDeadline = errors.New("duplex session child survived its termination deadline")
	ErrDuplexShutdown              = errors.New("duplex session shutdown did not complete")
	ErrDuplexPipe                  = errors.New("duplex session pipe failed")
	errDuplexInputComplete         = errors.New("duplex session input completed at an observed child boundary")
	errDuplexInputClosed           = errors.New("duplex session child closed input pipe")
)

// DuplexSessionConfig describes one real child-process session run. The
// runner always supplies the four product-boundary flags --audio-in -, --audio-
// out -, --record-dir, and --max-duration. APIKey is delivered through the
// provider's supported AGENT_* environment variable and is never included in
// argv or the returned evidence.
type DuplexSessionConfig struct {
	BinaryPath       string
	RecordDir        string
	WorkingDirectory string
	ConfigDir        string
	Provider         string
	Model            string
	BaseURL          string
	APIKey           string
	SystemPrompt     string
	MaxDuration      time.Duration

	// OnStart runs after the runner has established its monotonic origin and
	// before the child is started. It is intended for observers that need their
	// timestamps to share the same origin as DuplexRunResult.
	OnStart func(time.Time)

	// FrameDuration controls pacing, not the product's PCM format. A caller
	// can shorten it for hermetic tests while retaining incremental delivery.
	FrameDuration time.Duration
	SampleRate    int

	// AdditionalArgs are session flags such as --wait-for-close. Required
	// boundary flags are appended after these arguments so this runner retains
	// ownership of the product-under-test seam.
	AdditionalArgs []string
	Segments       []DuplexAudioSegment

	// BeforeInputClose runs after the final segment has been delivered and
	// before stdin is closed. It is a gate-only observation hook: it may wait
	// for already observable output, but it cannot inject another input frame.
	BeforeInputClose DuplexSegmentGate

	// Termination selects how the runner ends the child after the input
	// script. The zero value is natural completion. SIGINT requires one of the
	// output gates below and sends os.Interrupt once that observable product
	// output crosses the child stdout boundary.
	Termination                 TerminationMethod
	TerminationAfterOutputBytes int64
	TerminationAfterOutputReads int

	// Output and ErrorOutput receive the same bytes that the child writes to
	// stdout and stderr while the runner independently captures bounded copies.
	Output      io.Writer
	ErrorOutput io.Writer

	MaxCapturedOutputBytes int64
	ShutdownGrace          time.Duration
}

// DuplexAudioSegment is one continuously streamed portion of customer audio.
// PCM16 is written frame-by-frame; no segment is buffered by the child before
// delivery. SilenceFor appends digital-silence frames and is useful for
// exercising provider VAD-shaped speech boundaries while stdin stays open.
type DuplexAudioSegment struct {
	ID string

	PCM16       []byte
	SilenceFor  time.Duration
	DelayBefore time.Duration

	// WaitForOutputBytes and WaitForOutputReads provide a deterministic
	// customer gate. They are evaluated before this segment starts, allowing a
	// correction to cross the still-active assistant stream.
	WaitForOutputBytes int64
	WaitForOutputReads int
	Before             DuplexSegmentGate
}

// DuplexSegmentGate is invoked on the input pump immediately before a
// segment. The output pump remains active while the gate waits.
type DuplexSegmentGate func(context.Context, *DuplexProgress) error

// DuplexInputEvent records one successfully delivered PCM16 frame.
type DuplexInputEvent struct {
	SegmentID string        `json:"segment_id"`
	Frame     int           `json:"frame"`
	Bytes     int           `json:"bytes"`
	At        time.Duration `json:"at"`
	Timestamp time.Time     `json:"timestamp"`
	Silent    bool          `json:"silent"`
	SHA256    string        `json:"sha256"`
}

// DuplexOutputEvent records one read from the child stdout pipe. Read chunks
// are intentionally retained as events rather than being treated as one
// response-sized blob, so callers can prove that output was drained while
// input was still being delivered.
type DuplexOutputEvent struct {
	Read      int           `json:"read"`
	Bytes     int           `json:"bytes"`
	Total     int64         `json:"total"`
	At        time.Duration `json:"at"`
	Timestamp time.Time     `json:"timestamp"`
}

// DuplexProgressSnapshot is a point-in-time view available to segment gates.
type DuplexProgressSnapshot struct {
	At            time.Duration
	InputBytes    int64
	InputFrames   int
	OutputBytes   int64
	OutputReads   int
	InputSegments int
	OutputClosed  bool
}

// DuplexProgress exposes only observable stream progress to a segment gate.
// It does not expose the child process or a direct product/runtime call.
type DuplexProgress struct {
	state *duplexProgressState
}

// Snapshot returns the progress observed so far.
func (p *DuplexProgress) Snapshot() DuplexProgressSnapshot {
	if p == nil || p.state == nil {
		return DuplexProgressSnapshot{}
	}
	return p.state.snapshot()
}

// WaitForOutputBytes waits until at least minimum output bytes crossed the
// child stdout boundary. A non-positive minimum returns immediately.
func (p *DuplexProgress) WaitForOutputBytes(ctx context.Context, minimum int64) error {
	if minimum <= 0 {
		return nil
	}
	if p == nil || p.state == nil {
		return fmt.Errorf("%w: output progress is unavailable", ErrDuplexPipe)
	}
	return p.state.waitForOutput(ctx, minimum, false)
}

// WaitForOutputReads waits until at least minimum stdout reads have completed.
func (p *DuplexProgress) WaitForOutputReads(ctx context.Context, minimum int) error {
	if minimum <= 0 {
		return nil
	}
	if p == nil || p.state == nil {
		return fmt.Errorf("%w: output progress is unavailable", ErrDuplexPipe)
	}
	return p.state.waitForOutput(ctx, int64(minimum), true)
}

// Elapsed returns the runner's monotonic elapsed time at the instant of the
// snapshot. Segment gates use this to drive event-based policies while the
// child remains open.
func (p *DuplexProgress) Elapsed() time.Duration {
	if p == nil || p.state == nil {
		return 0
	}
	return p.state.elapsed()
}

// OutputEvents returns a copy of every stdout read observed so far. The
// events retain their process-relative timestamps so callers can correlate
// incremental output with another event ledger without assuming one read is
// one response.
func (p *DuplexProgress) OutputEvents() []DuplexOutputEvent {
	if p == nil || p.state == nil {
		return nil
	}
	return p.state.outputEvents()
}

// WaitForChange blocks until the child produces another observed output read
// or the stdout pump closes. It is the non-polling wake-up primitive used by
// the patience controller while the input pump keeps the process alive.
func (p *DuplexProgress) WaitForChange(ctx context.Context) error {
	if p == nil || p.state == nil {
		return fmt.Errorf("%w: progress is unavailable", ErrDuplexPipe)
	}
	return p.state.waitForChange(ctx)
}

// OutputClosed reports that the stdout pump has observed EOF or stopped after
// cancellation. It is an observable terminal boundary, not a claim that the
// child has been reaped.
func (p *DuplexProgress) OutputClosed() bool {
	if p == nil || p.state == nil {
		return false
	}
	return p.state.outputIsClosed()
}

// DuplexRunResult contains process, pipe, and timing evidence. Stdout and
// Stderr are bounded captures; Output and ErrorOutput, when configured, still
// receive the complete drained streams until their own writer reports an
// error.
type DuplexRunResult struct {
	Command            string        `json:"command"`
	SanitizedArgs      []string      `json:"sanitized_args"`
	PID                int           `json:"pid"`
	ExitCode           int           `json:"exit_code"`
	ExitClassification string        `json:"exit_classification"`
	Duration           time.Duration `json:"duration"`
	TimedOut           bool          `json:"timed_out"`
	Cancelled          bool          `json:"cancelled"`
	Signal             string        `json:"signal,omitempty"`
	SignalSent         bool          `json:"signal_sent"`
	SignalAt           time.Duration `json:"signal_at,omitempty"`
	ChildWaited        bool          `json:"child_waited"`
	WaitCount          int           `json:"wait_count"`
	DescendantsAlive   bool          `json:"descendants_alive"`

	InputClosed   bool `json:"input_closed"`
	InputFinished bool `json:"input_finished"`
	StdoutClosed  bool `json:"stdout_closed"`
	StderrClosed  bool `json:"stderr_closed"`

	CapturedOutputTruncated bool                `json:"captured_output_truncated"`
	Stdout                  []byte              `json:"-"`
	Stderr                  []byte              `json:"-"`
	Input                   []DuplexInputEvent  `json:"input"`
	Output                  []DuplexOutputEvent `json:"output"`
}

// DuplexRunner owns one bounded shipped-CLI child run. It has no mutable
// per-run state, so one runner can safely be reused by scenario families.
type DuplexRunner struct{}

// NewDuplexRunner returns the process-boundary runner.
func NewDuplexRunner() *DuplexRunner { return &DuplexRunner{} }

// Run starts the configured executable directly, without a shell, and drives
// its real session command through continuously open raw PCM16 pipes.
func (r *DuplexRunner) Run(ctx context.Context, config DuplexSessionConfig) (DuplexRunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, cleanup, err := normalizeDuplexConfig(config)
	if err != nil {
		return DuplexRunResult{}, err
	}
	defer cleanup()

	args := duplexSessionArgs(normalized)
	sanitizedArgs := SanitizeDuplexArgs(args, normalized.APIKey)
	result := DuplexRunResult{
		Command:       formatDuplexCommand(normalized.BinaryPath, sanitizedArgs),
		SanitizedArgs: sanitizedArgs,
		PID:           -1,
		ExitCode:      -1,
	}

	child := exec.Command(normalized.BinaryPath, args...)
	child.Dir = normalized.WorkingDirectory
	child.Env = duplexChildEnvironment(normalized)
	prepareDuplexCommand(child)

	stdin, err := child.StdinPipe()
	if err != nil {
		return result, duplexProcessError(ErrDuplexProcessStart, "open child stdin", err)
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return result, duplexProcessError(ErrDuplexProcessStart, "open child stdout", err)
	}
	stderr, err := child.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return result, duplexProcessError(ErrDuplexProcessStart, "open child stderr", err)
	}

	startedAt := time.Now()
	if normalized.OnStart != nil {
		normalized.OnStart(startedAt)
	}
	if err := child.Start(); err != nil {
		_ = stdin.Close()
		return result, duplexProcessError(ErrDuplexProcessStart, "start child", err)
	}
	result.PID = child.Process.Pid

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	var deadlineReached atomic.Bool
	deadline := time.AfterFunc(normalized.MaxDuration, func() {
		deadlineReached.Store(true)
		cancelRun()
	})
	defer deadline.Stop()

	progress := newDuplexProgressState()
	progress.setStartedAt(startedAt)
	stdoutCapture := newDuplexCapture(normalized.MaxCapturedOutputBytes)
	stderrCapture := newDuplexCapture(normalized.MaxCapturedOutputBytes)
	var inputEventsMu sync.Mutex
	var inputEvents []DuplexInputEvent
	var inputFinished atomic.Bool
	var inputClosed atomic.Bool
	var stdoutClosed atomic.Bool
	var stderrClosed atomic.Bool
	var waitCount atomic.Int32

	var closeStdinOnce sync.Once
	var closeStdinErr error
	closeStdin := func() error {
		closeStdinOnce.Do(func() {
			closeStdinErr = stdin.Close()
			inputClosed.Store(true)
		})
		return closeStdinErr
	}

	var terminateOnce sync.Once
	var terminateErr error
	terminate := func() error {
		terminateOnce.Do(func() { terminateErr = terminateDuplexCommand(child) })
		return terminateErr
	}

	var signalSent atomic.Bool
	var terminationRequested atomic.Bool
	var signalAt time.Duration
	var signalMu sync.Mutex

	failureCh := make(chan error, 4)
	var failureOnce sync.Once
	recordFailure := func(failure error) {
		if errors.Is(failure, errDuplexInputClosed) {
			// A provider SESSION.CLOSE can make the shipped child exit while the
			// runner is still writing the trailing PCM frame. Let child.Wait
			// establish the terminal process result; an early child exit with no
			// observable output still fails closed as ErrDuplexInputIncomplete.
			return
		}
		if failure == nil || isExpectedDuplexCancellation(failure, runCtx) || (terminationRequested.Load() && isExpectedDuplexSignalShutdown(failure)) {
			return
		}
		failureOnce.Do(func() {
			failureCh <- failure
			cancelRun()
		})
	}

	var pumps sync.WaitGroup
	pumps.Add(3)
	go func() {
		defer pumps.Done()
		if err := pumpDuplexOutput(runCtx, stdout, normalized.Output, stdoutCapture, progress, startedAt, true); err != nil {
			recordFailure(err)
		}
		progress.noteOutputClosed()
		stdoutClosed.Store(true)
	}()
	go func() {
		defer pumps.Done()
		if err := pumpDuplexOutput(runCtx, stderr, normalized.ErrorOutput, stderrCapture, progress, startedAt, false); err != nil {
			recordFailure(err)
		}
		stderrClosed.Store(true)
	}()
	go func() {
		defer pumps.Done()
		if err := pumpDuplexInput(runCtx, stdin, normalized, progress, startedAt, &inputEventsMu, &inputEvents, &inputFinished, closeStdin); err != nil {
			recordFailure(err)
		}
	}()

	var terminationWG sync.WaitGroup
	if normalized.Termination == TerminationSIGINT {
		terminationWG.Add(1)
		go func() {
			defer terminationWG.Done()
			var waitErr error
			switch {
			case normalized.TerminationAfterOutputBytes > 0:
				waitErr = progress.waitForOutput(runCtx, normalized.TerminationAfterOutputBytes, false)
			case normalized.TerminationAfterOutputReads > 0:
				waitErr = progress.waitForOutput(runCtx, int64(normalized.TerminationAfterOutputReads), true)
			}
			if waitErr != nil {
				return
			}
			terminationRequested.Store(true)
			sent, err := sendDuplexSIGINT(child)
			if err != nil {
				terminationRequested.Store(false)
				recordFailure(duplexPipeError("send SIGINT", err))
				return
			}
			if !sent {
				terminationRequested.Store(false)
				return
			}
			signalSent.Store(true)
			signalMu.Lock()
			signalAt = time.Since(startedAt)
			signalMu.Unlock()
			// Stop feeding a process that has been asked to terminate. The input
			// pump treats the resulting closed-pipe write as expected signal
			// shutdown, while stdout/stderr continue draining concurrently.
			_ = closeStdin()
		}()
	}

	waitDone := make(chan error, 1)
	go func() {
		waitCount.Add(1)
		waitDone <- child.Wait()
	}()

	processWaitOK, pumpsJoined, waitErr := waitForDuplexChild(runCtx, closeStdin, terminate, waitDone, &pumps, &terminationWG, cancelRun, normalized.ShutdownGrace)

	result.Duration = time.Since(startedAt)
	result.ExitCode = duplexExitCode(child, waitErr)
	result.TimedOut = deadlineReached.Load()
	result.Cancelled = ctx.Err() != nil && !result.TimedOut
	result.SignalSent = signalSent.Load()
	if result.SignalSent {
		result.Signal = duplexSIGINTName
		signalMu.Lock()
		result.SignalAt = signalAt
		signalMu.Unlock()
	}
	result.ChildWaited = processWaitOK
	result.WaitCount = int(waitCount.Load())
	result.InputClosed = inputClosed.Load()
	result.InputFinished = inputFinished.Load()
	result.StdoutClosed = stdoutClosed.Load()
	result.StderrClosed = stderrClosed.Load()
	result.DescendantsAlive = duplexDescendantsAlive(child, processWaitOK)
	result.ExitClassification = duplexExitClassification(result, normalized.Termination, waitErr)
	result.Output = progress.outputEvents()
	if !inputFinished.Load() && result.ExitClassification == "normal" && result.StdoutClosed && len(result.Output) > 0 {
		// A normal child exit after observable stdout is a provider-owned
		// session boundary. The input pump may have been cancelled by the
		// runner's post-wait cleanup before it could mark the final byte as
		// finished, but the product response itself crossed the boundary.
		result.InputFinished = true
	}
	result.CapturedOutputTruncated = stdoutCapture.truncated() || stderrCapture.truncated()
	result.Stdout = stdoutCapture.bytes()
	result.Stderr = stderrCapture.bytes()
	inputEventsMu.Lock()
	result.Input = append([]DuplexInputEvent(nil), inputEvents...)
	inputEventsMu.Unlock()

	var failures []error
	select {
	case failure := <-failureCh:
		failures = append(failures, failure)
	default:
	}
	// exec.Cmd.Wait closes the child-side pipe after the process exits. An
	// explicit close racing that cleanup can therefore report os.ErrClosed even
	// though the process was fully reaped; only surface close failures while the
	// child is still alive.
	if closeStdinErr != nil && !processWaitOK {
		failures = append(failures, duplexPipeError("close stdin", closeStdinErr))
	}
	if terminateErr != nil && !processWaitOK {
		failures = append(failures, duplexPipeError("terminate child", terminateErr))
	}
	if !processWaitOK {
		if result.TimedOut {
			failures = append(failures, fmt.Errorf("%w after %s", ErrDuplexChildSurvivedDeadline, normalized.MaxDuration))
		} else {
			failures = append(failures, fmt.Errorf("%w: %v", ErrDuplexShutdown, waitErr))
		}
	} else if result.TimedOut {
		failures = append(failures, fmt.Errorf("%w after %s", ErrDuplexDeadline, normalized.MaxDuration))
	}
	if waitErr != nil && processWaitOK && !result.TimedOut && result.ExitClassification != "sigint" && !isExpectedDuplexWaitClose(result, waitErr) {
		if result.ExitCode != 0 {
			failures = append(failures, fmt.Errorf("%w: exit code %d", ErrDuplexProcessExit, result.ExitCode))
		} else {
			failures = append(failures, duplexPipeError("wait for child", waitErr))
		}
	}
	if result.Cancelled {
		failures = append(failures, ctx.Err())
	}
	if processWaitOK && !result.InputFinished && !result.TimedOut && !result.Cancelled && result.ExitClassification != "sigint" {
		failures = append(failures, ErrDuplexInputIncomplete)
	}
	if !pumpsJoined {
		failures = append(failures, fmt.Errorf("%w after %s", ErrDuplexShutdown, normalized.ShutdownGrace))
	}
	if result.CapturedOutputTruncated {
		failures = append(failures, ErrDuplexOutputCaptureLimit)
	}
	return result, errors.Join(failures...)
}

func waitForDuplexChild(
	runCtx context.Context,
	closeStdin func() error,
	terminate func() error,
	waitDone <-chan error,
	pumps *sync.WaitGroup,
	terminationWG *sync.WaitGroup,
	cancelRun context.CancelFunc,
	shutdownGrace time.Duration,
) (processWaitOK, pumpsJoined bool, waitErr error) {
	// A pipe write or a segment gate cannot be left waiting after cancellation.
	// Closing stdin unblocks an in-flight write, and killing the process group
	// closes inherited stdout/stderr descriptors held by descendants.
	watchDone := make(chan struct{})
	var watchWG sync.WaitGroup
	watchWG.Add(1)
	go func() {
		defer watchWG.Done()
		select {
		case <-runCtx.Done():
			_ = closeStdin()
			_ = terminate()
		case <-watchDone:
		}
	}()

	select {
	case waitErr = <-waitDone:
		processWaitOK = true
	case <-runCtx.Done():
		processWaitOK, waitErr = waitForDuplexProcess(waitDone, shutdownGrace, terminate)
	}

	// The process may exit before the script has delivered every segment. Close
	// the caller-owned write end so the input pump observes the premature EOF
	// rather than remaining blocked on a dead child.
	_ = closeStdin()
	if processWaitOK {
		cancelRun()
	}
	close(watchDone)
	watchWG.Wait()

	pumpsDone := make(chan struct{})
	go func() {
		pumps.Wait()
		close(pumpsDone)
	}()
	select {
	case <-pumpsDone:
		pumpsJoined = true
	case <-time.After(shutdownGrace):
	}
	terminationWG.Wait()
	return processWaitOK, pumpsJoined, waitErr
}

// RunDuplexSession is the convenient function form for callers that do not
// need to retain a runner value.
func RunDuplexSession(ctx context.Context, config DuplexSessionConfig) (DuplexRunResult, error) {
	return NewDuplexRunner().Run(ctx, config)
}

type normalizedDuplexConfig struct {
	DuplexSessionConfig
	BinaryPath string
}

func normalizeDuplexConfig(config DuplexSessionConfig) (normalizedDuplexConfig, func(), error) {
	if strings.TrimSpace(config.BinaryPath) == "" {
		return normalizedDuplexConfig{}, func() {}, fmt.Errorf("%w: binary path is empty", ErrDuplexConfigInvalid)
	}
	binaryPath, err := resolveBinary(config.BinaryPath)
	if err != nil {
		return normalizedDuplexConfig{}, func() {}, fmt.Errorf("%w: %v", ErrDuplexConfigInvalid, err)
	}
	if strings.TrimSpace(config.RecordDir) == "" {
		return normalizedDuplexConfig{}, func() {}, fmt.Errorf("%w: record directory is empty", ErrDuplexConfigInvalid)
	}
	if strings.TrimSpace(config.Provider) == "" || strings.TrimSpace(config.Model) == "" {
		return normalizedDuplexConfig{}, func() {}, fmt.Errorf("%w: provider and model are required", ErrDuplexConfigInvalid)
	}
	if config.MaxDuration <= 0 {
		return normalizedDuplexConfig{}, func() {}, fmt.Errorf("%w: maximum duration must be positive", ErrDuplexConfigInvalid)
	}
	if len(config.Segments) == 0 {
		return normalizedDuplexConfig{}, func() {}, fmt.Errorf("%w: at least one audio segment is required", ErrDuplexConfigInvalid)
	}
	if config.FrameDuration == 0 {
		config.FrameDuration = DefaultDuplexFrameDuration
	}
	if config.FrameDuration <= 0 {
		return normalizedDuplexConfig{}, func() {}, fmt.Errorf("%w: frame duration must be positive", ErrDuplexConfigInvalid)
	}
	if config.SampleRate == 0 {
		config.SampleRate = DefaultDuplexSampleRate
	}
	if config.SampleRate <= 0 {
		return normalizedDuplexConfig{}, func() {}, fmt.Errorf("%w: sample rate must be positive", ErrDuplexConfigInvalid)
	}
	if config.SampleRate != DefaultDuplexSampleRate {
		return normalizedDuplexConfig{}, func() {}, fmt.Errorf("%w: sample rate must be %d Hz", ErrDuplexConfigInvalid, DefaultDuplexSampleRate)
	}
	if config.MaxCapturedOutputBytes == 0 {
		config.MaxCapturedOutputBytes = DefaultDuplexCaptureLimit
	}
	if config.MaxCapturedOutputBytes < 0 {
		return normalizedDuplexConfig{}, func() {}, fmt.Errorf("%w: output capture limit must not be negative", ErrDuplexConfigInvalid)
	}
	if config.ShutdownGrace == 0 {
		config.ShutdownGrace = DefaultDuplexShutdownGrace
	}
	if config.ShutdownGrace <= 0 {
		return normalizedDuplexConfig{}, func() {}, fmt.Errorf("%w: shutdown grace must be positive", ErrDuplexConfigInvalid)
	}
	if config.Termination == "" {
		config.Termination = TerminationNatural
	}
	if !config.Termination.valid() {
		return normalizedDuplexConfig{}, func() {}, fmt.Errorf("%w: termination must be natural or sigint", ErrDuplexConfigInvalid)
	}
	if config.TerminationAfterOutputBytes < 0 || config.TerminationAfterOutputReads < 0 {
		return normalizedDuplexConfig{}, func() {}, fmt.Errorf("%w: termination output gates must not be negative", ErrDuplexConfigInvalid)
	}
	if config.TerminationAfterOutputBytes > 0 && config.TerminationAfterOutputReads > 0 {
		return normalizedDuplexConfig{}, func() {}, fmt.Errorf("%w: configure one termination output gate", ErrDuplexConfigInvalid)
	}
	if config.Termination == TerminationSIGINT && config.TerminationAfterOutputBytes == 0 && config.TerminationAfterOutputReads == 0 {
		return normalizedDuplexConfig{}, func() {}, fmt.Errorf("%w: sigint termination requires an output gate", ErrDuplexConfigInvalid)
	}
	if config.Termination == TerminationNatural && (config.TerminationAfterOutputBytes > 0 || config.TerminationAfterOutputReads > 0) {
		return normalizedDuplexConfig{}, func() {}, fmt.Errorf("%w: natural termination cannot configure an output gate", ErrDuplexConfigInvalid)
	}
	if err := validateDuplexAdditionalArgs(config.AdditionalArgs, config.APIKey); err != nil {
		return normalizedDuplexConfig{}, func() {}, err
	}

	recordDir, err := prepareDuplexDirectory(config.RecordDir, "record directory")
	if err != nil {
		return normalizedDuplexConfig{}, func() {}, err
	}
	workingDir := config.WorkingDirectory
	if strings.TrimSpace(workingDir) == "" {
		workingDir = recordDir
	} else {
		workingDir, err = prepareDuplexDirectory(workingDir, "working directory")
		if err != nil {
			return normalizedDuplexConfig{}, func() {}, err
		}
	}

	cleanup := func() {}
	configDir := config.ConfigDir
	if strings.TrimSpace(configDir) == "" {
		configDir, err = os.MkdirTemp("", "agent-cli-duplex-config-")
		if err != nil {
			return normalizedDuplexConfig{}, func() {}, fmt.Errorf("%w: create isolated config directory: %v", ErrDuplexConfigInvalid, err)
		}
		cleanup = func() { _ = os.RemoveAll(configDir) }
	} else {
		configDir, err = prepareDuplexDirectory(configDir, "config directory")
		if err != nil {
			return normalizedDuplexConfig{}, func() {}, err
		}
	}

	seenIDs := make(map[string]struct{}, len(config.Segments))
	segments := make([]DuplexAudioSegment, len(config.Segments))
	for index, segment := range config.Segments {
		if strings.TrimSpace(segment.ID) == "" {
			segment.ID = fmt.Sprintf("segment-%d", index+1)
		}
		if _, exists := seenIDs[segment.ID]; exists {
			cleanup()
			return normalizedDuplexConfig{}, func() {}, fmt.Errorf("%w: duplicate segment ID %q", ErrDuplexConfigInvalid, segment.ID)
		}
		seenIDs[segment.ID] = struct{}{}
		if len(segment.PCM16)%2 != 0 {
			cleanup()
			return normalizedDuplexConfig{}, func() {}, fmt.Errorf("%w: segment %q has odd PCM16 length %d", ErrDuplexInputInvalid, segment.ID, len(segment.PCM16))
		}
		if segment.DelayBefore < 0 || segment.SilenceFor < 0 || segment.WaitForOutputBytes < 0 || segment.WaitForOutputReads < 0 {
			cleanup()
			return normalizedDuplexConfig{}, func() {}, fmt.Errorf("%w: segment %q has a negative delay, silence, or output gate", ErrDuplexConfigInvalid, segment.ID)
		}
		if len(segment.PCM16) == 0 && segment.SilenceFor <= 0 {
			cleanup()
			return normalizedDuplexConfig{}, func() {}, fmt.Errorf("%w: segment %q has no PCM16 or silence duration", ErrDuplexInputInvalid, segment.ID)
		}
		segment.PCM16 = append([]byte(nil), segment.PCM16...)
		segments[index] = segment
	}
	config.Segments = segments
	config.RecordDir = recordDir
	config.WorkingDirectory = workingDir
	config.ConfigDir = configDir
	config.BinaryPath = binaryPath
	return normalizedDuplexConfig{DuplexSessionConfig: config, BinaryPath: binaryPath}, cleanup, nil
}

func prepareDuplexDirectory(raw, label string) (string, error) {
	path, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("%w: resolve %s: %v", ErrDuplexConfigInvalid, label, err)
	}
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("%w: %s %q is not a directory", ErrDuplexConfigInvalid, label, path)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", fmt.Errorf("%w: inspect %s %q: %v", ErrDuplexConfigInvalid, label, path, statErr)
	} else if err := os.MkdirAll(path, 0o700); err != nil {
		return "", fmt.Errorf("%w: create %s %q: %v", ErrDuplexConfigInvalid, label, path, err)
	}
	return path, nil
}

func duplexSessionArgs(config normalizedDuplexConfig) []string {
	args := []string{"--config-dir", config.ConfigDir, "session"}
	args = append(args, config.AdditionalArgs...)
	args = append(args,
		"--audio-in", "-",
		"--audio-out", "-",
		"--record-dir", config.RecordDir,
		"--provider", config.Provider,
		"--model", config.Model,
		"--max-duration", config.MaxDuration.String(),
	)
	if strings.TrimSpace(config.BaseURL) != "" {
		args = append(args, "--base-url", config.BaseURL)
	}
	if strings.TrimSpace(config.SystemPrompt) != "" {
		args = append(args, "--system-prompt", config.SystemPrompt)
	}
	return args
}

func validateDuplexAdditionalArgs(args []string, apiKey string) error {
	ownedFlags := map[string]struct{}{
		"--audio-in": {}, "--audio-out": {}, "--record-dir": {},
		"--provider": {}, "--model": {}, "--max-duration": {},
	}
	secretFlags := map[string]struct{}{
		"--api-key": {}, "--token": {}, "--access-token": {},
		"--authorization": {}, "--password": {}, "--secret": {},
	}
	for index, arg := range args {
		flag := arg
		if equals := strings.IndexByte(flag, '='); equals >= 0 {
			flag = flag[:equals]
		}
		if _, ok := ownedFlags[flag]; ok {
			return fmt.Errorf("%w: additional argument %q is owned by the duplex runner", ErrDuplexConfigInvalid, arg)
		}
		if _, ok := secretFlags[flag]; ok {
			return fmt.Errorf("%w: additional argument %q may carry credentials", ErrDuplexConfigInvalid, arg)
		}
		if apiKey != "" && arg == apiKey {
			return fmt.Errorf("%w: API key cannot be passed through additional arguments at index %d", ErrDuplexConfigInvalid, index)
		}
	}
	return nil
}

// SanitizeDuplexArgs redacts values following common secret flags and inline
// secret assignments. It is exported so PR/report code can reuse the same
// policy when rendering the recorded command.
func SanitizeDuplexArgs(args []string, secrets ...string) []string {
	redactFlags := map[string]struct{}{
		"--api-key": {}, "--token": {}, "--access-token": {},
		"--authorization": {}, "--password": {}, "--secret": {},
	}
	secretSet := make(map[string]struct{}, len(secrets))
	for _, secret := range secrets {
		if secret != "" {
			secretSet[secret] = struct{}{}
		}
	}
	result := make([]string, len(args))
	redactNext := false
	for index, arg := range args {
		if redactNext {
			result[index] = "<redacted>"
			redactNext = false
			continue
		}
		if _, ok := secretSet[arg]; ok {
			result[index] = "<redacted>"
			continue
		}
		if _, ok := redactFlags[arg]; ok {
			result[index] = arg
			redactNext = true
			continue
		}
		redacted := arg
		for flag := range redactFlags {
			prefix := flag + "="
			if strings.HasPrefix(arg, prefix) {
				redacted = prefix + "<redacted>"
				break
			}
		}
		result[index] = redacted
	}
	return result
}

func duplexChildEnvironment(config normalizedDuplexConfig) []string {
	environment := []string{"PWD=" + config.WorkingDirectory}
	if path, ok := os.LookupEnv("PATH"); ok {
		environment = append(environment, "PATH="+path)
	}
	if systemRoot, ok := os.LookupEnv("SYSTEMROOT"); ok {
		environment = append(environment, "SYSTEMROOT="+systemRoot)
	}
	if strings.TrimSpace(config.APIKey) != "" {
		switch strings.ToLower(strings.TrimSpace(config.Provider)) {
		case "openai":
			environment = append(environment, "AGENT_MODEL__OPENAI__API_KEY="+config.APIKey)
		case "grok":
			environment = append(environment, "AGENT_MODEL__GROK__API_KEY="+config.APIKey)
		}
	}
	return environment
}

type duplexCapture struct {
	mu             sync.Mutex
	limit          int64
	data           bytes.Buffer
	truncatedValue bool
}

func newDuplexCapture(limit int64) *duplexCapture { return &duplexCapture{limit: limit} }

func (c *duplexCapture) append(data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.limit < 0 {
		return
	}
	remaining := c.limit - int64(c.data.Len())
	if remaining <= 0 {
		if len(data) > 0 {
			c.truncatedValue = true
		}
		return
	}
	if int64(len(data)) > remaining {
		_, _ = c.data.Write(data[:remaining])
		c.truncatedValue = true
		return
	}
	_, _ = c.data.Write(data)
}

func (c *duplexCapture) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.data.Bytes()...)
}

func (c *duplexCapture) truncated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.truncatedValue
}

func pumpDuplexOutput(ctx context.Context, source io.Reader, destination io.Writer, capture *duplexCapture, progress *duplexProgressState, startedAt time.Time, observe bool) error {
	buffer := make([]byte, 32*1024)
	for {
		count, readErr := source.Read(buffer)
		if count > 0 {
			data := append([]byte(nil), buffer[:count]...)
			capture.append(data)
			if observe {
				now := time.Now()
				progress.noteOutput(DuplexOutputEvent{Bytes: count, At: now.Sub(startedAt), Timestamp: now})
			}
			if destination != nil {
				if err := writeDuplexAll(destination, data); err != nil {
					return duplexPipeError("write output sink", err)
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			// exec.Cmd.Wait closes runner-owned stdout after the child has
			// exited. A blocked reader can observe that close as os.ErrClosed
			// instead of EOF; it is the same completed pipe boundary and must
			// not cancel a run that is still collecting final evidence.
			if isExpectedDuplexPipeClosure(readErr) {
				return nil
			}
			if isExpectedDuplexCancellation(readErr, ctx) {
				return nil
			}
			return duplexPipeError("read child output", readErr)
		}
	}
}

func pumpDuplexInput(ctx context.Context, destination io.Writer, config normalizedDuplexConfig, progress *duplexProgressState, startedAt time.Time, eventsMu *sync.Mutex, events *[]DuplexInputEvent, finished *atomic.Bool, closeStdin func() error) error {
	progressView := &DuplexProgress{state: progress}
	frameBytes := DefaultDuplexFrameSamples * 2
	frameDuration := config.FrameDuration
	frameNumber := 0
	for _, segment := range config.Segments {
		if segment.WaitForOutputBytes > 0 {
			if err := progressView.WaitForOutputBytes(ctx, segment.WaitForOutputBytes); err != nil {
				return duplexPipeError("wait for output bytes", err)
			}
		}
		if segment.WaitForOutputReads > 0 {
			if err := progressView.WaitForOutputReads(ctx, segment.WaitForOutputReads); err != nil {
				return duplexPipeError("wait for output reads", err)
			}
		}
		if segment.Before != nil {
			if err := segment.Before(ctx, progressView); err != nil {
				if errors.Is(err, errDuplexInputComplete) {
					finished.Store(true)
					if closeErr := closeStdin(); closeErr != nil && !isExpectedDuplexPipeClosure(closeErr) {
						return duplexPipeError("close stdin after segment boundary", closeErr)
					}
					return nil
				}
				return duplexPipeError("run segment gate", err)
			}
		}
		if segment.DelayBefore > 0 {
			timer := time.NewTimer(segment.DelayBefore)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return duplexPipeError("delay segment", ctx.Err())
			}
		}

		data := append([]byte(nil), segment.PCM16...)
		if segment.SilenceFor > 0 {
			silenceBytes := duplexSilenceBytes(segment.SilenceFor, frameBytes, frameDuration)
			data = append(data, silenceBytes...)
		}
		if len(data) == 0 {
			return duplexPipeError("prepare segment", fmt.Errorf("segment %q has no frames", segment.ID))
		}
		if len(data)%2 != 0 {
			return fmt.Errorf("%w: segment %q produced odd PCM16 length %d", ErrDuplexInputInvalid, segment.ID, len(data))
		}
		progress.noteInputSegment()

		// A gate or deliberate inter-segment delay means the previous schedule
		// is no longer a useful wall-clock origin. Resetting here avoids a burst
		// of catch-up frames that would defeat the streaming proof.
		nextFrameAt := time.Now()
		for segmentFrame, offset := 0, 0; offset < len(data); segmentFrame, offset = segmentFrame+1, offset+frameBytes {
			if segmentFrame > 0 {
				nextFrameAt = nextFrameAt.Add(frameDuration)
				if err := waitDuplexUntil(ctx, nextFrameAt); err != nil {
					return duplexPipeError("pace input frame", err)
				}
			}
			frame := make([]byte, frameBytes)
			copy(frame, data[offset:minInt(offset+frameBytes, len(data))])
			if err := writeDuplexAll(destination, frame); err != nil {
				if isExpectedDuplexCancellation(err, ctx) {
					return nil
				}
				if isExpectedDuplexPipeClosure(err) {
					return fmt.Errorf("%w: %v", errDuplexInputClosed, err)
				}
				return duplexPipeError("write child stdin", err)
			}
			frameNumber++
			hash := sha256.Sum256(frame)
			now := time.Now()
			event := DuplexInputEvent{
				SegmentID: segment.ID,
				Frame:     frameNumber,
				Bytes:     len(frame),
				At:        now.Sub(startedAt),
				Timestamp: now,
				Silent:    isDuplexSilence(frame),
				SHA256:    hex.EncodeToString(hash[:]),
			}
			eventsMu.Lock()
			*events = append(*events, event)
			eventsMu.Unlock()
			progress.noteInput(frame)
		}
	}
	if config.BeforeInputClose != nil {
		if err := config.BeforeInputClose(ctx, progressView); err != nil {
			if errors.Is(err, errDuplexInputComplete) {
				finished.Store(true)
				if closeErr := closeStdin(); closeErr != nil && !isExpectedDuplexPipeClosure(closeErr) {
					return duplexPipeError("close stdin after input boundary", closeErr)
				}
				return nil
			}
			return duplexPipeError("run before-input-close gate", err)
		}
	}
	finished.Store(true)
	if closeErr := closeStdin(); closeErr != nil && !isExpectedDuplexPipeClosure(closeErr) {
		return duplexPipeError("close stdin after input", closeErr)
	}
	return nil
}

func duplexSilenceBytes(duration time.Duration, frameBytes int, frameDuration time.Duration) []byte {
	frames := int((duration + frameDuration - 1) / frameDuration)
	if frames < 1 {
		frames = 1
	}
	return make([]byte, frames*frameBytes)
}

func isDuplexSilence(data []byte) bool {
	for _, value := range data {
		if value != 0 {
			return false
		}
	}
	return true
}

func waitDuplexUntil(ctx context.Context, target time.Time) error {
	delay := time.Until(target)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func writeDuplexAll(destination io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := destination.Write(data)
		if written > 0 {
			data = data[written:]
		}
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func waitForDuplexProcess(waitDone <-chan error, grace time.Duration, terminate func() error) (bool, error) {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case err := <-waitDone:
		return true, err
	case <-timer.C:
		_ = terminate()
	}
	timer.Reset(grace)
	defer timer.Stop()
	select {
	case err := <-waitDone:
		return true, err
	case <-timer.C:
		return false, fmt.Errorf("%w after %s", ErrDuplexChildSurvivedDeadline, grace)
	}
}

func duplexExitCode(child *exec.Cmd, waitErr error) int {
	if child != nil && child.ProcessState != nil {
		return child.ProcessState.ExitCode()
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func duplexProcessError(kind error, operation string, cause error) error {
	return fmt.Errorf("%w: %s: %w", kind, operation, cause)
}

// exec.Cmd.Wait may report the runtime closing one of the runner-owned pipe
// descriptors after a child has already exited successfully. The close error
// is not a product failure when the child was reaped with exit code zero; the
// runner still retains the explicit pipe-closed and input-finished facts.
func isExpectedDuplexWaitClose(result DuplexRunResult, waitErr error) bool {
	return result.ExitCode == 0 && (errors.Is(waitErr, os.ErrClosed) || errors.Is(waitErr, io.ErrClosedPipe))
}

func isExpectedDuplexPipeClosure(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, os.ErrClosed) || errors.Is(err, io.ErrClosedPipe) || strings.Contains(strings.ToLower(err.Error()), "broken pipe")
}

func duplexPipeError(operation string, cause error) error {
	if cause == nil {
		return nil
	}
	return fmt.Errorf("%w: %s: %w", ErrDuplexPipe, operation, cause)
}

func isExpectedDuplexCancellation(err error, ctx context.Context) bool {
	if err == nil || ctx == nil || ctx.Err() == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrClosed) || errors.Is(err, io.ErrClosedPipe)
}

const DuplexSIGINTName = "SIGINT"

const duplexSIGINTName = DuplexSIGINTName

func sendDuplexSIGINT(command *exec.Cmd) (bool, error) {
	if command == nil || command.Process == nil {
		return false, fmt.Errorf("%w: child process is unavailable", ErrDuplexProcessExit)
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func isExpectedDuplexSignalShutdown(err error) bool {
	return errors.Is(err, os.ErrClosed) || errors.Is(err, io.ErrClosedPipe)
}

func duplexExitClassification(result DuplexRunResult, termination TerminationMethod, waitErr error) string {
	if result.TimedOut {
		return "timeout"
	}
	if result.Cancelled {
		return "cancelled"
	}
	if result.SignalSent && termination == TerminationSIGINT {
		return "sigint"
	}
	if result.ChildWaited && result.ExitCode == 0 && (waitErr == nil || isExpectedDuplexWaitClose(result, waitErr)) {
		return "normal"
	}
	return "failed"
}

func formatDuplexCommand(command string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, command)
	for _, arg := range args {
		parts = append(parts, strconv.Quote(arg))
	}
	return strings.Join(parts, " ")
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
