package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	loopprobe "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

// TransportFunc adapts a function to Transport.
type TransportFunc func(context.Context, loopprobe.AcceptanceInput, ArtifactSet) (RunResult, error)

func (f TransportFunc) Run(ctx context.Context, input loopprobe.AcceptanceInput, artifacts ArtifactSet) (RunResult, error) {
	if f == nil {
		return RunResult{}, &ExecutionError{Kind: ErrProbeAgentCrashed, Cause: errors.New("nil transport")}
	}
	return f(ctx, input, artifacts)
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

// ReplayFixture is the recorded process boundary consumed by ReplayTransport.
// Input fields are optional so one fixture can be reused for dynamic temporary
// directories, but any field present is matched exactly.
type ReplayFixture struct {
	Input          *loopprobe.AcceptanceInput      `json:"input,omitempty"`
	Stdout         string                          `json:"stdout"`
	Stderr         string                          `json:"stderr"`
	Transcript     string                          `json:"transcript,omitempty"`
	ExitCode       int                             `json:"exit_code"`
	Report         loopprobe.AcceptanceAgentReport `json:"report"`
	WorkspaceFiles map[string]string               `json:"workspace_files,omitempty"`
	Error          string                          `json:"error,omitempty"`
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

func (t *ReplayTransport) Run(ctx context.Context, input loopprobe.AcceptanceInput, artifacts ArtifactSet) (RunResult, error) {
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
	if err := materializeReplayWorkspaceFiles(artifacts, t.Fixture.WorkspaceFiles); err != nil {
		return RunResult{}, &ExecutionError{Kind: ErrReplayFixtureInvalid, Cause: err}
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

// AcceptanceTransportKind marks runners built on this transport as replay runs.
func (t *ReplayTransport) AcceptanceTransportKind() loopprobe.AcceptanceTransport {
	return loopprobe.AcceptanceTransportReplay
}

func materializeReplayWorkspaceFiles(artifacts ArtifactSet, files map[string]string) error {
	for relative, data := range files {
		path, err := artifacts.Path(relative)
		if err != nil {
			return fmt.Errorf("workspace file %q: %w", relative, err)
		}
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("workspace file %q is declared more than once", relative)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect workspace file %q: %w", relative, err)
		}
		if err := os.MkdirAll(filepath.Dir(path), privateDirMode); err != nil {
			return fmt.Errorf("create workspace file directory for %q: %w", relative, err)
		}
		if err := os.WriteFile(path, []byte(data), privateFileMode); err != nil {
			return fmt.Errorf("write workspace file %q: %w", relative, err)
		}
	}
	return nil
}
