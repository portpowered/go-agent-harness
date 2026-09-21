package internal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
)

type manifestValue struct {
	SchemaVersion int                 `json:"schema_version"`
	Personas      personaValues       `json:"personas"`
	OpeningSeed   string              `json:"opening_seed"`
	Provider      string              `json:"provider"`
	Model         string              `json:"model"`
	Timing        timingValues        `json:"timing"`
	Bounds        boundValues         `json:"bounds"`
	StopReason    selfplay.StopReason `json:"stop_reason"`
	Agents        [2]agentValue       `json:"agents"`
	Artifacts     map[string]string   `json:"artifacts"`
	Error         string              `json:"error,omitempty"`
}

type personaValues struct {
	Customer  string `json:"customer"`
	Assistant string `json:"assistant"`
}

type timingValues struct {
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
	Elapsed   string `json:"elapsed"`
}

type boundValues struct {
	MaxDuration      string `json:"max_duration"`
	MaxTurns         int    `json:"max_turns"`
	WAVBytes         int64  `json:"wav_pcm_bytes_per_side"`
	DiagnosticsBytes int64  `json:"diagnostics_bytes_per_side"`
	StreamBytes      int64  `json:"stream_bytes_per_side"`
}

type agentValue struct {
	Role           string                `json:"role"`
	CompletedTurns int                   `json:"completed_turns"`
	Terminal       selfplay.SideTerminal `json:"terminal"`
	TerminalError  string                `json:"terminal_error,omitempty"`
	Artifacts      sideArtifactNames     `json:"artifacts"`
}

type sideArtifactNames struct {
	WAV          string `json:"wav"`
	Diagnostics  string `json:"diagnostics"`
	StreamDeltas string `json:"stream_deltas"`
}

func makeManifest(e *evidence, result selfplay.Result, runErr error, secret string) manifestValue {
	value := manifestValue{
		SchemaVersion: manifestSchemaVersion,
		Personas:      personaValues{Customer: customerPersona, Assistant: assistantPersona},
		OpeningSeed:   openingSeed,
		Provider:      e.request.Provider,
		Model:         e.request.Model,
		Timing:        timingValues{StartedAt: result.StartedAt.UTC().Format(time.RFC3339Nano), EndedAt: result.EndedAt.UTC().Format(time.RFC3339Nano), Elapsed: result.Elapsed.String()},
		Bounds:        boundValues{MaxDuration: e.request.MaxDuration.String(), MaxTurns: e.request.MaxTurns, WAVBytes: maxPCMBytes, DiagnosticsBytes: maxDiagnosticBytes, StreamBytes: maxStreamBytes},
		StopReason:    result.StopReason,
		Artifacts: map[string]string{
			"agent_a_wav":           agentAWAVPath,
			"agent_a_diagnostics":   agentADiagnosticsPath,
			"agent_a_stream_deltas": agentAStreamDeltasPath,
			"agent_b_wav":           agentBWAVPath,
			"agent_b_diagnostics":   agentBDiagnosticsPath,
			"agent_b_stream_deltas": agentBStreamDeltasPath,
		},
	}
	if runErr != nil {
		value.Error = redactError(runErr.Error(), secret)
	}
	for index, side := range e.sides {
		if side == nil {
			continue
		}
		turns, terminal, terminalError := result.Customer.CompletedTurns, result.Customer.Terminal, result.Customer.TerminalError
		if index == 1 {
			turns, terminal, terminalError = result.Assistant.CompletedTurns, result.Assistant.Terminal, result.Assistant.TerminalError
		}
		value.Agents[index] = agentValue{Role: string(side.role), CompletedTurns: turns, Terminal: terminal, TerminalError: redactError(terminalError, secret), Artifacts: sideArtifactNames{WAV: side.wavPath, Diagnostics: side.diagnosticsPath, StreamDeltas: side.streamPath}}
	}
	return value
}

func sideOutcome(files fileSystem, destination string, side *sideEvidence) selfplay.SideResult {
	value := selfplay.SideResult{Role: side.role, Terminal: side.terminal, TerminalError: side.terminalErr}
	value.WAV = artifactOutcome(files, destination, side.wavPath, side.wav.close() == nil)
	value.Diagnostics = artifactOutcome(files, destination, side.diagnostics.path, side.diagnostics.close() == nil)
	value.StreamDeltas = artifactOutcome(files, destination, side.stream.path, side.stream.close() == nil)
	return value
}

func mergeSideResult(base, evidence selfplay.SideResult) selfplay.SideResult {
	base.Role = evidence.Role
	base.Terminal = evidence.Terminal
	base.TerminalError = evidence.TerminalError
	base.WAV = evidence.WAV
	base.Diagnostics = evidence.Diagnostics
	base.StreamDeltas = evidence.StreamDeltas
	return base
}

func artifactOutcome(files fileSystem, destination, name string, closed bool) selfplay.ArtifactOutcome {
	info, err := files.Stat(filepath.Join(destination, name))
	if err != nil {
		return selfplay.ArtifactOutcome{Path: name, Complete: false}
	}
	return selfplay.ArtifactOutcome{Path: name, Bytes: info.Size(), Complete: closed}
}

func atomicManifest(files fileSystem, path string, value manifestValue) (resultErr error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	data = append(data, '\n')
	file, err := files.CreateTemp(filepath.Dir(path), ".run-manifest-*.tmp")
	if err != nil {
		return fmt.Errorf("create manifest temporary: %w", err)
	}
	temporary := file
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary && temporaryPath != "" {
			if err := files.Remove(temporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				resultErr = errors.Join(resultErr, fmt.Errorf("remove temporary manifest %q: %w", temporaryPath, err))
			}
		}
	}()
	if _, err := writeAll(temporary, data); err != nil {
		return errors.Join(fmt.Errorf("write manifest temporary: %w", err), temporary.Close())
	}
	if err := temporary.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync manifest temporary: %w", err), temporary.Close())
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close manifest temporary: %w", err)
	}
	if temporaryPath == "" {
		return errors.New("temporary manifest has no file name")
	}
	if err := files.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace manifest: %w", err)
	}
	removeTemporary = false
	return nil
}
