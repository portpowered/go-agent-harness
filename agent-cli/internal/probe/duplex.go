package probe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/childproc"
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
	// WaitForOutputSequence gates the segment until this exact byte sequence
	// crosses the child stdout boundary. Matching survives split stdout reads.
	WaitForOutputSequence []byte
	WaitForOutputReads    int
	Before                DuplexSegmentGate
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
		Command:       childproc.FormatCommand(normalized.BinaryPath, sanitizedArgs),
		SanitizedArgs: sanitizedArgs,
		PID:           -1,
		ExitCode:      -1,
	}

	child := exec.Command(normalized.BinaryPath, args...)
	child.Dir = normalized.WorkingDirectory
	child.Env = duplexChildEnvironment(normalized)
	childproc.Prepare(child)

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
	session := newDuplexSession(runCtx, cancelRun, normalized, child, stdin, startedAt)
	deadline := time.AfterFunc(normalized.MaxDuration, func() {
		session.deadlineReached.Store(true)
		cancelRun()
	})
	defer deadline.Stop()

	session.startPumps(stdout, stderr)
	if normalized.Termination == TerminationSIGINT {
		session.terminationWG.Add(1)
		go session.runSIGINTTermination()
	}
	waitDone := session.startWait()

	processWaitOK, pumpsJoined, waitErr := waitForDuplexChild(runCtx, session.closeStdin, session.terminate, waitDone, &session.pumps, &session.terminationWG, cancelRun, normalized.ShutdownGrace)

	session.fillResult(ctx, &result, processWaitOK, waitErr)
	failures := session.shutdownFailures(result, processWaitOK, waitErr)
	failures = append(failures, session.exitFailures(ctx, result, processWaitOK, pumpsJoined, waitErr)...)
	return result, errors.Join(failures...)
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
	binaryPath, err := validateDuplexConfigRequirements(config)
	if err != nil {
		return normalizedDuplexConfig{}, func() {}, err
	}
	if err := applyDuplexConfigDefaults(&config); err != nil {
		return normalizedDuplexConfig{}, func() {}, err
	}
	if err := normalizeDuplexTermination(&config); err != nil {
		return normalizedDuplexConfig{}, func() {}, err
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
	configDir, cleanup, err := prepareDuplexConfigDirectory(config.ConfigDir)
	if err != nil {
		return normalizedDuplexConfig{}, func() {}, err
	}
	segments, err := normalizeDuplexSegments(config.Segments)
	if err != nil {
		cleanup()
		return normalizedDuplexConfig{}, func() {}, err
	}
	config.Segments = segments
	config.RecordDir = recordDir
	config.WorkingDirectory = workingDir
	config.ConfigDir = configDir
	config.BinaryPath = binaryPath
	return normalizedDuplexConfig{DuplexSessionConfig: config, BinaryPath: binaryPath}, cleanup, nil
}

// validateDuplexConfigRequirements checks the mandatory fields and returns
// the resolved binary path.
func validateDuplexConfigRequirements(config DuplexSessionConfig) (string, error) {
	if strings.TrimSpace(config.BinaryPath) == "" {
		return "", fmt.Errorf("%w: binary path is empty", ErrDuplexConfigInvalid)
	}
	binaryPath, err := resolveBinary(config.BinaryPath)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrDuplexConfigInvalid, err)
	}
	if strings.TrimSpace(config.RecordDir) == "" {
		return "", fmt.Errorf("%w: record directory is empty", ErrDuplexConfigInvalid)
	}
	if strings.TrimSpace(config.Provider) == "" || strings.TrimSpace(config.Model) == "" {
		return "", fmt.Errorf("%w: provider and model are required", ErrDuplexConfigInvalid)
	}
	if config.MaxDuration <= 0 {
		return "", fmt.Errorf("%w: maximum duration must be positive", ErrDuplexConfigInvalid)
	}
	if len(config.Segments) == 0 {
		return "", fmt.Errorf("%w: at least one audio segment is required", ErrDuplexConfigInvalid)
	}
	return binaryPath, nil
}

func applyDuplexConfigDefaults(config *DuplexSessionConfig) error {
	if config.FrameDuration == 0 {
		config.FrameDuration = DefaultDuplexFrameDuration
	}
	if config.FrameDuration <= 0 {
		return fmt.Errorf("%w: frame duration must be positive", ErrDuplexConfigInvalid)
	}
	if config.SampleRate == 0 {
		config.SampleRate = DefaultDuplexSampleRate
	}
	if config.SampleRate <= 0 {
		return fmt.Errorf("%w: sample rate must be positive", ErrDuplexConfigInvalid)
	}
	if config.SampleRate != DefaultDuplexSampleRate {
		return fmt.Errorf("%w: sample rate must be %d Hz", ErrDuplexConfigInvalid, DefaultDuplexSampleRate)
	}
	if config.MaxCapturedOutputBytes == 0 {
		config.MaxCapturedOutputBytes = DefaultDuplexCaptureLimit
	}
	if config.MaxCapturedOutputBytes < 0 {
		return fmt.Errorf("%w: output capture limit must not be negative", ErrDuplexConfigInvalid)
	}
	if config.ShutdownGrace == 0 {
		config.ShutdownGrace = DefaultDuplexShutdownGrace
	}
	if config.ShutdownGrace <= 0 {
		return fmt.Errorf("%w: shutdown grace must be positive", ErrDuplexConfigInvalid)
	}
	return nil
}

func normalizeDuplexTermination(config *DuplexSessionConfig) error {
	if config.Termination == "" {
		config.Termination = TerminationNatural
	}
	if !config.Termination.valid() {
		return fmt.Errorf("%w: termination must be natural or sigint", ErrDuplexConfigInvalid)
	}
	if config.TerminationAfterOutputBytes < 0 || config.TerminationAfterOutputReads < 0 {
		return fmt.Errorf("%w: termination output gates must not be negative", ErrDuplexConfigInvalid)
	}
	if config.TerminationAfterOutputBytes > 0 && config.TerminationAfterOutputReads > 0 {
		return fmt.Errorf("%w: configure one termination output gate", ErrDuplexConfigInvalid)
	}
	gated := config.TerminationAfterOutputBytes > 0 || config.TerminationAfterOutputReads > 0
	if config.Termination == TerminationSIGINT && !gated {
		return fmt.Errorf("%w: sigint termination requires an output gate", ErrDuplexConfigInvalid)
	}
	if config.Termination == TerminationNatural && gated {
		return fmt.Errorf("%w: natural termination cannot configure an output gate", ErrDuplexConfigInvalid)
	}
	return nil
}

// prepareDuplexConfigDirectory returns the child's config directory and a
// cleanup for an isolated temporary directory created on the caller's behalf.
func prepareDuplexConfigDirectory(raw string) (string, func(), error) {
	if strings.TrimSpace(raw) != "" {
		configDir, err := prepareDuplexDirectory(raw, "config directory")
		return configDir, func() {}, err
	}
	configDir, err := os.MkdirTemp("", "agent-cli-duplex-config-")
	if err != nil {
		return "", func() {}, fmt.Errorf("%w: create isolated config directory: %w", ErrDuplexConfigInvalid, err)
	}
	return configDir, func() { removeDuplexConfigDirectory(configDir) }, nil
}

// removeDuplexConfigDirectory is best-effort cleanup of the isolated config
// directory after the run result has been captured; a leftover temporary
// directory must not change the reported session outcome.
func removeDuplexConfigDirectory(dir string) {
	if err := os.RemoveAll(dir); err != nil {
		return
	}
}

func normalizeDuplexSegments(input []DuplexAudioSegment) ([]DuplexAudioSegment, error) {
	seenIDs := make(map[string]struct{}, len(input))
	segments := make([]DuplexAudioSegment, len(input))
	for index, segment := range input {
		if strings.TrimSpace(segment.ID) == "" {
			segment.ID = fmt.Sprintf("segment-%d", index+1)
		}
		if _, exists := seenIDs[segment.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate segment ID %q", ErrDuplexConfigInvalid, segment.ID)
		}
		seenIDs[segment.ID] = struct{}{}
		if err := validateDuplexSegment(segment); err != nil {
			return nil, err
		}
		segment.PCM16 = append([]byte(nil), segment.PCM16...)
		segments[index] = segment
	}
	return segments, nil
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

func pumpDuplexOutput(ctx context.Context, source io.Reader, destination io.Writer, capture *childproc.Capture, progress *duplexProgressState, startedAt time.Time, observe bool) error {
	buffer := make([]byte, 32*1024)
	for {
		count, readErr := source.Read(buffer)
		if count > 0 {
			data := append([]byte(nil), buffer[:count]...)
			capture.Append(data)
			if observe {
				now := time.Now()
				progress.noteOutput(DuplexOutputEvent{Bytes: count, At: now.Sub(startedAt), Timestamp: now}, data)
			}
			if destination != nil {
				if err := childproc.WriteAll(destination, data); err != nil {
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
			if childproc.IsPipeClosure(readErr) {
				return nil
			}
			if childproc.IsCancellation(ctx, readErr) {
				return nil
			}
			return duplexPipeError("read child output", readErr)
		}
	}
}

func pumpDuplexInput(ctx context.Context, destination io.Writer, config normalizedDuplexConfig, progress *duplexProgressState, startedAt time.Time, eventsMu *sync.Mutex, events *[]DuplexInputEvent, finished *atomic.Bool, closeStdin func() error) error {
	pump := &duplexInputPump{
		ctx: ctx, destination: destination, progress: progress, progressView: &DuplexProgress{state: progress},
		startedAt: startedAt, eventsMu: eventsMu, events: events, finished: finished, closeStdin: closeStdin,
		frameBytes: DefaultDuplexFrameSamples * 2, frameDuration: config.FrameDuration,
	}
	for _, segment := range config.Segments {
		if done, err := pump.deliverSegment(segment); done {
			return err
		}
	}
	if config.BeforeInputClose != nil {
		if err := config.BeforeInputClose(ctx, pump.progressView); err != nil {
			if errors.Is(err, errDuplexInputComplete) {
				return pump.finish("close stdin after input boundary")
			}
			return duplexPipeError("run before-input-close gate", err)
		}
	}
	return pump.finish("close stdin after input")
}

// duplexInputPump paces scripted PCM16 segments into the child's stdin and
// records one input event per written frame.
type duplexInputPump struct {
	ctx           context.Context
	destination   io.Writer
	progress      *duplexProgressState
	progressView  *DuplexProgress
	startedAt     time.Time
	eventsMu      *sync.Mutex
	events        *[]DuplexInputEvent
	finished      *atomic.Bool
	closeStdin    func() error
	frameBytes    int
	frameDuration time.Duration
	frameNumber   int
}

// finish marks input complete and closes stdin; operation names the boundary.
func (p *duplexInputPump) finish(operation string) error {
	p.finished.Store(true)
	if closeErr := p.closeStdin(); closeErr != nil && !childproc.IsPipeClosure(closeErr) {
		return duplexPipeError(operation, closeErr)
	}
	return nil
}

// deliverSegment gates, delays, and writes one segment. done reports that the
// pump must stop and return err.
func (p *duplexInputPump) deliverSegment(segment DuplexAudioSegment) (bool, error) {
	if err := p.progressView.waitForSegmentOutput(p.ctx, segment); err != nil {
		return true, err
	}
	if segment.Before != nil {
		if err := segment.Before(p.ctx, p.progressView); err != nil {
			if errors.Is(err, errDuplexInputComplete) {
				return true, p.finish("close stdin after segment boundary")
			}
			return true, duplexPipeError("run segment gate", err)
		}
	}
	if segment.DelayBefore > 0 {
		timer := time.NewTimer(segment.DelayBefore)
		select {
		case <-timer.C:
		case <-p.ctx.Done():
			timer.Stop()
			return true, duplexPipeError("delay segment", p.ctx.Err())
		}
	}
	data := append([]byte(nil), segment.PCM16...)
	if segment.SilenceFor > 0 {
		silenceBytes := duplexSilenceBytes(segment.SilenceFor, p.frameBytes, p.frameDuration)
		data = append(data, silenceBytes...)
	}
	if len(data) == 0 {
		return true, duplexPipeError("prepare segment", fmt.Errorf("segment %q has no frames", segment.ID))
	}
	if len(data)%2 != 0 {
		return true, fmt.Errorf("%w: segment %q produced odd PCM16 length %d", ErrDuplexInputInvalid, segment.ID, len(data))
	}
	p.progress.noteInputSegment()
	return p.writeFrames(segment.ID, data)
}

func (p *duplexInputPump) writeFrames(segmentID string, data []byte) (bool, error) {
	// A gate or deliberate inter-segment delay means the previous schedule
	// is no longer a useful wall-clock origin. Resetting here avoids a burst
	// of catch-up frames that would defeat the streaming proof.
	nextFrameAt := time.Now()
	for segmentFrame, offset := 0, 0; offset < len(data); segmentFrame, offset = segmentFrame+1, offset+p.frameBytes {
		if segmentFrame > 0 {
			nextFrameAt = nextFrameAt.Add(p.frameDuration)
			if err := childproc.WaitUntil(p.ctx, nextFrameAt); err != nil {
				return true, duplexPipeError("pace input frame", err)
			}
		}
		frame := make([]byte, p.frameBytes)
		copy(frame, data[offset:min(offset+p.frameBytes, len(data))])
		if done, err := p.writeFrame(segmentID, frame); done {
			return true, err
		}
	}
	return false, nil
}

func (p *duplexInputPump) writeFrame(segmentID string, frame []byte) (bool, error) {
	if err := childproc.WriteAll(p.destination, frame); err != nil {
		if childproc.IsCancellation(p.ctx, err) {
			return true, nil
		}
		if childproc.IsPipeClosure(err) {
			return true, fmt.Errorf("%w: %w", errDuplexInputClosed, err)
		}
		return true, duplexPipeError("write child stdin", err)
	}
	p.frameNumber++
	hash := sha256.Sum256(frame)
	now := time.Now()
	event := DuplexInputEvent{
		SegmentID: segmentID,
		Frame:     p.frameNumber,
		Bytes:     len(frame),
		At:        now.Sub(p.startedAt),
		Timestamp: now,
		Silent:    isDuplexSilence(frame),
		SHA256:    hex.EncodeToString(hash[:]),
	}
	p.eventsMu.Lock()
	*p.events = append(*p.events, event)
	p.eventsMu.Unlock()
	p.progress.noteInput(frame)
	return false, nil
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

func duplexPipeError(operation string, cause error) error {
	if cause == nil {
		return nil
	}
	return fmt.Errorf("%w: %s: %w", ErrDuplexPipe, operation, cause)
}

const DuplexSIGINTName = "SIGINT"

// Process exit classifications recorded in DuplexRunResult and ProcessFacts.
const (
	duplexExitNormal  = "normal"
	duplexExitFailed  = "failed"
	duplexExitTimeout = "timeout"
	duplexExitSIGINT  = "sigint"
	// duplexExitCancelled matches the cancelled terminal disposition.
	duplexExitCancelled = string(DispositionCancelled)
)

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

func duplexExitClassification(result DuplexRunResult, termination TerminationMethod, waitErr error) string {
	if result.TimedOut {
		return duplexExitTimeout
	}
	if result.Cancelled {
		return duplexExitCancelled
	}
	if result.SignalSent && termination == TerminationSIGINT {
		return duplexExitSIGINT
	}
	if result.ChildWaited && result.ExitCode == 0 && (waitErr == nil || isExpectedDuplexWaitClose(result, waitErr)) {
		return duplexExitNormal
	}
	return duplexExitFailed
}

// ProcessFactsFromDuplexResult keeps the runner's lifecycle fields and the
// evidence schema in lockstep for both termination shapes.
func ProcessFactsFromDuplexResult(result DuplexRunResult) ProcessFacts {
	return ProcessFacts{
		PID:                result.PID,
		ExitCode:           result.ExitCode,
		ExitClassification: result.ExitClassification,
		Signal:             result.Signal,
		SignalSent:         result.SignalSent,
		SignalAt:           result.SignalAt,
		ChildWaited:        result.ChildWaited,
		WaitCount:          result.WaitCount,
		DescendantsAlive:   result.DescendantsAlive,
		InputClosed:        result.InputClosed,
		InputFinished:      result.InputFinished,
		OutputClosed:       result.StdoutClosed && result.StderrClosed,
		StartedAt:          0,
		EndedAt:            result.Duration,
	}
}
