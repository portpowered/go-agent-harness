// Package probe contains the process and artifact boundary for one blind
// customer-acceptance run. It deliberately has no knowledge of the repository
// under test; the only probe context is the resolved binary, goal, and empty
// working directory.
package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	loopprobe "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

var (
	ErrBinaryMissing            = errors.New("acceptance probe binary is missing")
	ErrBinaryNotExecutable      = errors.New("acceptance probe binary is not executable")
	ErrGoalMissing              = errors.New("acceptance probe goal is missing")
	ErrUnknownGoal              = errors.New("acceptance probe goal is unknown")
	ErrWorkingDirectoryInvalid  = errors.New("acceptance probe working directory is invalid")
	ErrWorkingDirectoryNotEmpty = errors.New("acceptance probe working directory is not empty")
	ErrProbeAgentCrashed        = errors.New("acceptance probe agent crashed")
	ErrProbeAgentStuck          = errors.New("acceptance probe agent is stuck")
	ErrReplayMismatch           = errors.New("acceptance probe replay input mismatch")
	ErrReplayFixtureInvalid     = errors.New("acceptance probe replay fixture is invalid")
	ErrArtifactWrite            = errors.New("acceptance probe artifact write failed")
	ErrUnsafeArtifactPath       = errors.New("acceptance probe artifact path is unsafe")
)

// InputError identifies which of the three blind-probe inputs was invalid.
type InputError struct {
	Field string
	Kind  error
	Cause error
}

func (e *InputError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Cause != nil {
		return fmt.Sprintf("acceptance probe %s: %v: %v", e.Field, e.Kind, e.Cause)
	}
	return fmt.Sprintf("acceptance probe %s: %v", e.Field, e.Kind)
}

func (e *InputError) Unwrap() error {
	if e == nil {
		return nil
	}
	if e.Cause != nil {
		return errors.Join(e.Kind, e.Cause)
	}
	return e.Kind
}

// ExecutionError preserves the typed reason for a transport failure while
// retaining the binary/goal context in the message.
type ExecutionError struct {
	Kind  error
	Cause error
}

func (e *ExecutionError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Kind == nil {
		if e.Cause == nil {
			return "acceptance probe execution failed"
		}
		return e.Cause.Error()
	}
	if e.Cause == nil {
		return e.Kind.Error()
	}
	return fmt.Sprintf("%s: %v", e.Kind, e.Cause)
}

func (e *ExecutionError) Unwrap() error {
	if e == nil {
		return nil
	}
	if e.Cause != nil {
		return errors.Join(e.Kind, e.Cause)
	}
	return e.Kind
}

// RunResult is the transport-neutral output of one probe-agent process. The
// runner persists every byte before asking the objective verifier to inspect
// the artifacts.
type RunResult struct {
	ExitCode   int
	Stdout     []byte
	Stderr     []byte
	Transcript []byte
	Report     loopprobe.AcceptanceAgentReport
}

// Transport drives one probe-agent run. LiveTransport and ReplayTransport
// implement the same interface so replay is a transport choice, not a second
// acceptance algorithm.
type Transport interface {
	Run(context.Context, loopprobe.AcceptanceInput, ArtifactSet) (RunResult, error)
}

// TransportFunc adapts a function to Transport.
type TransportFunc func(context.Context, loopprobe.AcceptanceInput, ArtifactSet) (RunResult, error)

func (f TransportFunc) Run(ctx context.Context, input loopprobe.AcceptanceInput, artifacts ArtifactSet) (RunResult, error) {
	if f == nil {
		return RunResult{}, &ExecutionError{Kind: ErrProbeAgentCrashed, Cause: errors.New("nil transport")}
	}
	return f(ctx, input, artifacts)
}

// ObjectiveVerifier checks recorded bytes rather than trusting the probe
// agent's claimed success. Returning an unverified evidence value is a normal
// failing verdict; an error is retained in that verdict for diagnosis.
type ObjectiveVerifier interface {
	Verify(context.Context, loopprobe.AcceptanceInput, ArtifactSet, loopprobe.AcceptanceAgentReport) (loopprobe.ObjectiveEvidence, error)
}

// ObjectiveVerifierFunc adapts a function to ObjectiveVerifier.
type ObjectiveVerifierFunc func(context.Context, loopprobe.AcceptanceInput, ArtifactSet, loopprobe.AcceptanceAgentReport) (loopprobe.ObjectiveEvidence, error)

func (f ObjectiveVerifierFunc) Verify(ctx context.Context, input loopprobe.AcceptanceInput, artifacts ArtifactSet, report loopprobe.AcceptanceAgentReport) (loopprobe.ObjectiveEvidence, error) {
	if f == nil {
		return loopprobe.ObjectiveEvidence{}, loopprobe.ErrObjectiveEvidenceAbsent
	}
	return f(ctx, input, artifacts, report)
}

// ArtifactSet describes the stable files emitted for one run. Paths in a
// verdict are relative to Root, so the result can be moved or consumed by a
// downstream fleet lane without rewriting evidence references.
type ArtifactSet struct {
	Root             string
	WorkingDirectory string
	StdoutPath       string
	StderrPath       string
	TranscriptPath   string
	ExitStatusPath   string
	ReportPath       string
	InputPath        string
}

func newArtifactSet(root, workingDirectory string) ArtifactSet {
	return ArtifactSet{
		Root:             root,
		WorkingDirectory: workingDirectory,
		StdoutPath:       "stdout.txt",
		StderrPath:       "stderr.txt",
		TranscriptPath:   "transcript.jsonl",
		ExitStatusPath:   "exit-status.json",
		ReportPath:       "agent-report.json",
		InputPath:        "input.json",
	}
}

// Path resolves a relative artifact reference and rejects absolute paths or
// traversal outside the run root.
func (a ArtifactSet) Path(relative string) (string, error) {
	if strings.TrimSpace(relative) == "" || filepath.IsAbs(relative) {
		return "", ErrUnsafeArtifactPath
	}
	clean := filepath.Clean(relative)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", ErrUnsafeArtifactPath
	}
	root, err := filepath.Abs(a.Root)
	if err != nil {
		return "", fmt.Errorf("resolve artifact root: %w", err)
	}
	path, err := filepath.Abs(filepath.Join(root, clean))
	if err != nil {
		return "", fmt.Errorf("resolve artifact %q: %w", relative, err)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrUnsafeArtifactPath
	}
	return path, nil
}

// RecordedArtifactVerifier is the default generic verifier. A probe reports a
// relative artifact and an exact claim; this verifier reads the actual file,
// rejects empty/symlinked artifacts, and requires the claim to occur in the
// recorded bytes. Goal-specific catalog lanes can provide a stricter verifier
// without changing the runner or CLI contract.
type RecordedArtifactVerifier struct{}

func (RecordedArtifactVerifier) Verify(_ context.Context, _ loopprobe.AcceptanceInput, artifacts ArtifactSet, report loopprobe.AcceptanceAgentReport) (loopprobe.ObjectiveEvidence, error) {
	relative := strings.TrimSpace(report.ObjectiveArtifactPath)
	claim := strings.TrimSpace(report.CheckedClaim)
	if relative == "" || claim == "" {
		return loopprobe.ObjectiveEvidence{}, loopprobe.ErrObjectiveEvidenceAbsent
	}
	path, err := artifacts.Path(relative)
	if err != nil {
		return loopprobe.ObjectiveEvidence{ArtifactPath: relative, CheckedClaim: claim}, fmt.Errorf("%w: %v", loopprobe.ErrObjectiveEvidenceMismatch, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return loopprobe.ObjectiveEvidence{ArtifactPath: relative, CheckedClaim: claim}, fmt.Errorf("%w: read %q: %v", loopprobe.ErrObjectiveEvidenceAbsent, relative, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return loopprobe.ObjectiveEvidence{ArtifactPath: relative, CheckedClaim: claim}, fmt.Errorf("%w: %q is not a regular artifact", loopprobe.ErrObjectiveEvidenceMismatch, relative)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return loopprobe.ObjectiveEvidence{ArtifactPath: relative, CheckedClaim: claim}, fmt.Errorf("%w: read %q: %v", loopprobe.ErrObjectiveEvidenceAbsent, relative, err)
	}
	if len(data) == 0 || !bytes.Contains(data, []byte(claim)) {
		return loopprobe.ObjectiveEvidence{ArtifactPath: relative, CheckedClaim: claim}, fmt.Errorf("%w: artifact %q does not contain checked claim", loopprobe.ErrObjectiveEvidenceMismatch, relative)
	}
	return loopprobe.ObjectiveEvidence{ArtifactPath: relative, CheckedClaim: claim, Verified: true}, nil
}

// Runner executes one acceptance probe and persists its process evidence.
type Runner struct {
	Transport     Transport
	Verifier      ObjectiveVerifier
	TransportKind loopprobe.AcceptanceTransport
	ArtifactRoot  string
	// ValidateGoal is optional because the goal catalog is owned by a
	// downstream lane. When supplied, its failure is surfaced as a typed input
	// error without adding catalog hints to the blind process.
	ValidateGoal func(string) error
}

// NewRunner constructs a runner. A nil verifier uses RecordedArtifactVerifier;
// a nil transport uses LiveTransport.
func NewRunner(transport Transport, verifier ObjectiveVerifier) *Runner {
	if transport == nil {
		transport = LiveTransport{}
	}
	if verifier == nil {
		verifier = RecordedArtifactVerifier{}
	}
	kind := loopprobe.AcceptanceTransportLive
	switch transport.(type) {
	case *ReplayTransport:
		kind = loopprobe.AcceptanceTransportReplay
	}
	return &Runner{Transport: transport, Verifier: verifier, TransportKind: kind}
}

// NewLiveRunner constructs the production live-transport runner.
func NewLiveRunner(verifier ObjectiveVerifier) *Runner {
	return NewRunner(LiveTransport{}, verifier)
}

// NewReplayRunner loads a replay fixture and returns a runner using the same
// acceptance pipeline as live execution.
func NewReplayRunner(path string, verifier ObjectiveVerifier) (*Runner, error) {
	transport, err := NewReplayTransport(path)
	if err != nil {
		return nil, err
	}
	return NewRunner(transport, verifier), nil
}

// Run validates the three blind inputs, provisions a fresh empty working
// directory when needed, drives the selected transport, records stdout/stderr/
// transcript/exit status, and evaluates objective evidence from those files.
// A normal failed acceptance outcome returns a verdict and nil error. Process
// crashes and stuck transports additionally return a typed ExecutionError so
// callers can distinguish a bad product result from a broken probe harness.
func (r *Runner) Run(ctx context.Context, input loopprobe.AcceptanceInput) (loopprobe.AcceptanceVerdict, error) {
	if r == nil {
		return loopprobe.AcceptanceVerdict{}, &ExecutionError{Kind: ErrProbeAgentCrashed, Cause: errors.New("nil runner")}
	}
	resolved, err := resolveInput(input)
	if err != nil {
		return loopprobe.AcceptanceVerdict{}, err
	}
	if r.ValidateGoal != nil {
		if goalErr := r.ValidateGoal(resolved.Goal); goalErr != nil {
			kind := ErrUnknownGoal
			if errors.Is(goalErr, ErrGoalMissing) {
				kind = ErrGoalMissing
			}
			return loopprobe.AcceptanceVerdict{}, &InputError{Field: "goal", Kind: kind, Cause: goalErr}
		}
	}
	root, workdir, cleanup, err := prepareRunDirectories(resolved.WorkingDirectory, r.ArtifactRoot)
	if err != nil {
		return loopprobe.AcceptanceVerdict{}, err
	}
	defer cleanup()
	resolved.WorkingDirectory = workdir
	artifacts := newArtifactSet(root, workdir)

	transport := r.Transport
	if transport == nil {
		transport = LiveTransport{}
	}
	report := loopprobe.AcceptanceAgentReport{}
	runResult, transportErr := transport.Run(ctx, resolved, artifacts)
	report = runResult.Report
	if report.TerminalState == "" {
		report.TerminalState = inferredTerminalState(ctx, runResult.ExitCode, transportErr)
	}
	if transportErr != nil {
		if report.TerminalState == loopprobe.AcceptanceStuckPendingDownstream || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			report.TerminalState = loopprobe.AcceptanceStuckPendingDownstream
		} else {
			report.TerminalState = loopprobe.AcceptanceErrored
		}
	} else if runResult.ExitCode != 0 {
		report.TerminalState = loopprobe.AcceptanceErrored
	}
	runResult.Report = report
	if snapshotErr := snapshotWorkingDirectory(artifacts); snapshotErr != nil {
		return loopprobe.AcceptanceVerdict{}, &ExecutionError{Kind: ErrArtifactWrite, Cause: snapshotErr}
	}
	if writeErr := writeRunArtifacts(artifacts, resolved, runResult, transportErr); writeErr != nil {
		return loopprobe.AcceptanceVerdict{}, writeErr
	}

	verifier := r.Verifier
	if verifier == nil {
		verifier = RecordedArtifactVerifier{}
	}
	evidence, verifyErr := verifier.Verify(ctx, resolved, artifacts, report)
	verdict := loopprobe.EvaluateAcceptance(resolved.Goal, report, evidence, r.transportKind())
	verdict.RunDirectory = artifacts.Root
	if verifyErr != nil {
		verdict.Pass = false
		verdict.ScenarioResult.Error = joinVerdictError(verdict.ScenarioResult.Error, verifyErr.Error())
	}
	if transportErr != nil {
		verdict.Pass = false
		kind := classifyTransportError(ctx, transportErr, report.TerminalState)
		verdict.ScenarioResult.Error = joinVerdictError(verdict.ScenarioResult.Error, (&ExecutionError{Kind: kind, Cause: transportErr}).Error())
		return verdict, &ExecutionError{Kind: kind, Cause: transportErr}
	}
	return verdict, nil
}

func (r *Runner) transportKind() loopprobe.AcceptanceTransport {
	if r.TransportKind != "" {
		return r.TransportKind
	}
	return loopprobe.AcceptanceTransportLive
}

func joinVerdictError(existing, addition string) string {
	if existing == "" {
		return addition
	}
	if addition == "" {
		return existing
	}
	return existing + "; " + addition
}

func inferredTerminalState(ctx context.Context, exitCode int, runErr error) loopprobe.AcceptanceTerminalState {
	if runErr != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return loopprobe.AcceptanceStuckPendingDownstream
	}
	if runErr != nil || exitCode != 0 {
		return loopprobe.AcceptanceErrored
	}
	return loopprobe.AcceptanceCompleted
}

func classifyTransportError(ctx context.Context, runErr error, state loopprobe.AcceptanceTerminalState) error {
	if state == loopprobe.AcceptanceStuckPendingDownstream ||
		(errors.Is(ctx.Err(), context.DeadlineExceeded) && errors.Is(runErr, context.DeadlineExceeded)) {
		return ErrProbeAgentStuck
	}
	for _, kind := range []error{
		ErrReplayMismatch,
		ErrReplayFixtureInvalid,
		ErrArtifactWrite,
		ErrUnknownGoal,
		ErrProbeAgentStuck,
		ErrProbeAgentCrashed,
	} {
		if errors.Is(runErr, kind) {
			return kind
		}
	}
	return ErrProbeAgentCrashed
}

func resolveInput(input loopprobe.AcceptanceInput) (loopprobe.AcceptanceInput, error) {
	if strings.TrimSpace(input.Goal) == "" {
		return loopprobe.AcceptanceInput{}, &InputError{Field: "goal", Kind: ErrGoalMissing}
	}
	binaryPath, err := resolveBinary(input.BinaryPath)
	if err != nil {
		return loopprobe.AcceptanceInput{}, err
	}
	input.BinaryPath = binaryPath
	if input.WorkingDirectory != "" {
		path, absErr := filepath.Abs(input.WorkingDirectory)
		if absErr != nil {
			return loopprobe.AcceptanceInput{}, &InputError{Field: "working directory", Kind: ErrWorkingDirectoryInvalid, Cause: absErr}
		}
		input.WorkingDirectory = path
	}
	return input, nil
}

func resolveBinary(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", &InputError{Field: "binary", Kind: ErrBinaryMissing}
	}
	path := value
	if !filepath.IsAbs(value) && !strings.ContainsRune(value, filepath.Separator) {
		found, err := exec.LookPath(value)
		if err != nil {
			return "", &InputError{Field: "binary", Kind: ErrBinaryMissing, Cause: err}
		}
		path = found
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", &InputError{Field: "binary", Kind: ErrBinaryMissing, Cause: err}
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", &InputError{Field: "binary", Kind: ErrBinaryMissing, Cause: err}
	}
	if info.IsDir() || info.Mode()&0111 == 0 {
		return "", &InputError{Field: "binary", Kind: ErrBinaryNotExecutable}
	}
	return abs, nil
}

func prepareRunDirectories(workdir, artifactRoot string) (string, string, func(), error) {
	if workdir != "" {
		info, err := os.Lstat(workdir)
		if err != nil {
			return "", "", func() {}, &InputError{Field: "working directory", Kind: ErrWorkingDirectoryInvalid, Cause: err}
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", "", func() {}, &InputError{Field: "working directory", Kind: ErrWorkingDirectoryInvalid}
		}
		entries, err := os.ReadDir(workdir)
		if err != nil {
			return "", "", func() {}, &InputError{Field: "working directory", Kind: ErrWorkingDirectoryInvalid, Cause: err}
		}
		if len(entries) != 0 {
			return "", "", func() {}, &InputError{Field: "working directory", Kind: ErrWorkingDirectoryNotEmpty}
		}
	}
	parent := strings.TrimSpace(artifactRoot)
	if parent == "" {
		parent = os.TempDir()
	}
	root, err := os.MkdirTemp(parent, "agent-acceptance-probe-")
	if err != nil {
		return "", "", func() {}, fmt.Errorf("create acceptance probe run directory: %w", err)
	}
	cleanup := func() {}
	if workdir == "" {
		workdir = filepath.Join(root, "workdir")
		if err := os.Mkdir(workdir, 0o700); err != nil {
			_ = os.RemoveAll(root)
			return "", "", func() {}, fmt.Errorf("create acceptance probe working directory: %w", err)
		}
	}
	return root, workdir, cleanup, nil
}

type exitStatusArtifact struct {
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"`
}

func writeRunArtifacts(artifacts ArtifactSet, input loopprobe.AcceptanceInput, result RunResult, runErr error) error {
	transcript := result.Transcript
	if len(transcript) == 0 && len(result.Stdout) > 0 {
		transcript = result.Stdout
	}
	status := exitStatusArtifact{ExitCode: result.ExitCode}
	if runErr != nil {
		status.Error = runErr.Error()
	}
	report, err := json.Marshal(result.Report)
	if err != nil {
		return &ExecutionError{Kind: ErrArtifactWrite, Cause: err}
	}
	inputBytes, err := json.Marshal(input)
	if err != nil {
		return &ExecutionError{Kind: ErrArtifactWrite, Cause: err}
	}
	statusBytes, err := json.Marshal(status)
	if err != nil {
		return &ExecutionError{Kind: ErrArtifactWrite, Cause: err}
	}
	files := []struct {
		path string
		data []byte
	}{
		{artifacts.StdoutPath, result.Stdout},
		{artifacts.StderrPath, result.Stderr},
		{artifacts.TranscriptPath, transcript},
		{artifacts.ExitStatusPath, statusBytes},
		{artifacts.ReportPath, report},
		{artifacts.InputPath, inputBytes},
	}
	for _, file := range files {
		path, pathErr := artifacts.Path(file.path)
		if pathErr != nil {
			return &ExecutionError{Kind: ErrArtifactWrite, Cause: pathErr}
		}
		if writeErr := os.WriteFile(path, file.data, 0o600); writeErr != nil {
			return &ExecutionError{Kind: ErrArtifactWrite, Cause: fmt.Errorf("write %s: %w", file.path, writeErr)}
		}
	}
	return nil
}

// snapshotWorkingDirectory copies files the blind process created in its
// working directory into the durable run root. The process only knows its
// empty cwd, while downstream verifiers consume paths relative to Root.
// Symlinks are intentionally not followed: an artifact must be created inside
// the blind workspace, not be a reference into the host filesystem.
func snapshotWorkingDirectory(artifacts ArtifactSet) error {
	if strings.TrimSpace(artifacts.WorkingDirectory) == "" {
		return &ExecutionError{Kind: ErrArtifactWrite, Cause: errors.New("working directory is empty")}
	}
	return filepath.WalkDir(artifacts.WorkingDirectory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk working directory: %w", walkErr)
		}
		if path == artifacts.WorkingDirectory {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		relative, err := filepath.Rel(artifacts.WorkingDirectory, path)
		if err != nil {
			return fmt.Errorf("resolve working-directory artifact: %w", err)
		}
		destination, err := artifacts.Path(relative)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if err := os.MkdirAll(destination, 0o700); err != nil {
				return fmt.Errorf("create artifact directory %q: %w", relative, err)
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat working-directory artifact %q: %w", relative, err)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read working-directory artifact %q: %w", relative, err)
		}
		if err := os.WriteFile(destination, data, 0o600); err != nil {
			return fmt.Errorf("write working-directory artifact %q: %w", relative, err)
		}
		return nil
	})
}

// LiveTransport runs the probe agent process without a shell. The process is
// given the resolved binary as argv[0], the plain-English goal as its sole
// argument, and the empty directory as its cwd; no scenario or repository
// hints are added.
type LiveTransport struct {
	// Launch is an injected effect seam used by tests and by acceptance hosts
	// that own a richer probe-agent process. Nil uses the exec implementation.
	Launch func(context.Context, loopprobe.AcceptanceInput, ArtifactSet) (RunResult, error)
}

func (t LiveTransport) Run(ctx context.Context, input loopprobe.AcceptanceInput, artifacts ArtifactSet) (RunResult, error) {
	if t.Launch != nil {
		return t.Launch(ctx, input, artifacts)
	}
	return runLiveProcess(ctx, input)
}

func runLiveProcess(ctx context.Context, input loopprobe.AcceptanceInput) (RunResult, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, input.BinaryPath, input.Goal)
	cmd.Dir = input.WorkingDirectory
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = sanitizedEnvironment(input.WorkingDirectory)
	err := cmd.Run()
	result := RunResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	result.Report = parseAgentReport(result.Stdout)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return result, nil
		}
		return result, &ExecutionError{Kind: ErrProbeAgentCrashed, Cause: err}
	}
	return result, nil
}

// sanitizedEnvironment is the complete child-environment policy for a live
// acceptance probe. The probe receives its only documented runtime context
// through argv and cwd; PWD is retained solely as the conventional spelling
// of that cwd for programs that inspect their process environment. In
// particular, no parent environment or path-bearing checkout metadata is
// forwarded to the blind process.
func sanitizedEnvironment(workingDirectory string) []string {
	return []string{"PWD=" + workingDirectory}
}

func parseAgentReport(data []byte) loopprobe.AcceptanceAgentReport {
	lines := bytes.Split(data, []byte("\n"))
	for index := len(lines) - 1; index >= 0; index-- {
		line := bytes.TrimSpace(lines[index])
		if len(line) == 0 {
			continue
		}
		var report loopprobe.AcceptanceAgentReport
		if json.Unmarshal(line, &report) == nil {
			return report
		}
	}
	return loopprobe.AcceptanceAgentReport{}
}

// ReplayFixture is the recorded process boundary consumed by ReplayTransport.
// Input fields are optional so one fixture can be reused for dynamic temporary
// directories, but any field present is matched exactly.
type ReplayFixture struct {
	Input      *loopprobe.AcceptanceInput      `json:"input,omitempty"`
	Stdout     string                          `json:"stdout"`
	Stderr     string                          `json:"stderr"`
	Transcript string                          `json:"transcript,omitempty"`
	ExitCode   int                             `json:"exit_code"`
	Report     loopprobe.AcceptanceAgentReport `json:"report"`
	Error      string                          `json:"error,omitempty"`
}

// ReplayTransport returns recorded process observations without dialing a
// provider or changing the acceptance runner.
type ReplayTransport struct {
	Fixture ReplayFixture
}

func NewReplayTransport(path string) (*ReplayTransport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &ExecutionError{Kind: ErrReplayFixtureInvalid, Cause: err}
	}
	var fixture ReplayFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		return nil, &ExecutionError{Kind: ErrReplayFixtureInvalid, Cause: err}
	}
	return &ReplayTransport{Fixture: fixture}, nil
}

func (t *ReplayTransport) Run(ctx context.Context, input loopprobe.AcceptanceInput, _ ArtifactSet) (RunResult, error) {
	if err := ctx.Err(); err != nil {
		return RunResult{}, err
	}
	if t == nil {
		return RunResult{}, &ExecutionError{Kind: ErrReplayFixtureInvalid, Cause: errors.New("nil replay transport")}
	}
	if expected := t.Fixture.Input; expected != nil {
		if (expected.BinaryPath != "" && expected.BinaryPath != input.BinaryPath) ||
			(expected.Goal != "" && expected.Goal != input.Goal) ||
			(expected.WorkingDirectory != "" && expected.WorkingDirectory != input.WorkingDirectory) {
			return RunResult{}, &ExecutionError{Kind: ErrReplayMismatch, Cause: fmt.Errorf("fixture input does not match resolved probe input")}
		}
	}
	result := RunResult{
		ExitCode:   t.Fixture.ExitCode,
		Stdout:     []byte(t.Fixture.Stdout),
		Stderr:     []byte(t.Fixture.Stderr),
		Transcript: []byte(t.Fixture.Transcript),
		Report:     t.Fixture.Report,
	}
	if len(result.Transcript) == 0 && len(result.Stdout) > 0 {
		result.Transcript = result.Stdout
	}
	if t.Fixture.Error != "" {
		return result, &ExecutionError{Kind: ErrProbeAgentCrashed, Cause: errors.New(t.Fixture.Error)}
	}
	return result, nil
}
