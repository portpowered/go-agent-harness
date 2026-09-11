package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

type evidence struct {
	destination string
	startedAt   time.Time
	apiKey      string
	provider    string
	model       string
	maxDuration time.Duration
	maxTurns    int
	sides       [2]*sideEvidence

	mu           sync.Mutex
	recordErr    error
	finalizeOnce sync.Once
	finalizeErr  error
}

type sideEvidence struct {
	id             string
	role           string
	persona        string
	wavPath        string
	diagnosticPath string
	streamPath     string
	audio          *selfplay.WAVWriter
	diagnostics    *selfplay.JSONLWriter
	streamDeltas   *selfplay.JSONLWriter
	maxTurns       int

	mu            sync.Mutex
	terminalSeen  bool
	terminalClean bool
	terminalError string
}

type diagnosticLine struct {
	Event  string            `json:"event"`
	Fields map[string]string `json:"fields,omitempty"`
}

func newEvidence(destination string, options selfplay.RunOptions, startedAt time.Time) (*evidence, error) {
	evidence := &evidence{
		destination: destination,
		startedAt:   startedAt.UTC(),
		apiKey:      options.APIKey,
		provider:    options.Provider,
		model:       options.Model,
		maxDuration: options.MaxDuration,
		maxTurns:    options.MaxTurns,
	}
	configs := []struct {
		id, role, persona, wav, diagnostics, stream string
	}{
		{"agent-a", "customer", selfplay.SelfPlayCustomerPersona, selfplay.AgentAWAVPath, selfplay.AgentADiagnosticsPath, selfplay.AgentAStreamDeltasPath},
		{"agent-b", "assistant", selfplay.SelfPlayAssistantPersona, selfplay.AgentBWAVPath, selfplay.AgentBDiagnosticsPath, selfplay.AgentBStreamDeltasPath},
	}
	for index, config := range configs {
		side := &sideEvidence{
			id: config.id, role: config.role, persona: config.persona,
			wavPath:        filepath.Join(destination, config.wav),
			diagnosticPath: filepath.Join(destination, config.diagnostics),
			streamPath:     filepath.Join(destination, config.stream), maxTurns: options.MaxTurns,
		}
		evidence.sides[index] = side
		var err error
		side.audio, err = selfplay.NewWAVWriter(side.wavPath, selfplay.EvidenceSampleRate, options.EvidenceLimits)
		if err != nil {
			evidence.cleanupSetup()
			return nil, fmt.Errorf("create %s WAV evidence: %w", config.id, err)
		}
		side.diagnostics, err = selfplay.NewJSONLWriter(side.diagnosticPath, options.EvidenceLimits)
		if err != nil {
			evidence.sides[index] = side
			evidence.cleanupSetup()
			return nil, fmt.Errorf("create %s diagnostics evidence: %w", config.id, err)
		}
		side.streamDeltas, err = selfplay.NewJSONLWriter(side.streamPath, options.EvidenceLimits)
		if err != nil {
			evidence.sides[index] = side
			evidence.cleanupSetup()
			return nil, fmt.Errorf("create %s stream evidence: %w", config.id, err)
		}
	}
	return evidence, nil
}

func (e *evidence) cleanupSetup() {
	if e == nil {
		return
	}
	for _, side := range e.sides {
		if side == nil {
			continue
		}
		if side.audio != nil {
			_ = side.audio.Close()
		}
		if side.diagnostics != nil {
			_ = side.diagnostics.Close()
		}
		if side.streamDeltas != nil {
			_ = side.streamDeltas.Close()
		}
		for _, path := range []string{side.wavPath, side.diagnosticPath, side.streamPath} {
			_ = os.Remove(path)
		}
	}
}

func (e *evidence) side(index int) *sideEvidence {
	if e == nil || index < 0 || index >= len(e.sides) {
		return nil
	}
	return e.sides[index]
}

func (e *evidence) fail(err error) {
	if e == nil || err == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.recordErr == nil {
		e.recordErr = err
	}
}

func (e *evidence) err() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.recordErr
}

func (e *evidence) observeDiagnostic(index int, diagnostic selfplay.Diagnostic) error {
	side := e.side(index)
	if side == nil || side.diagnostics == nil {
		return errors.New("self-play diagnostics sink is not initialized")
	}
	fields := selfplay.CloneStringMap(diagnostic.Fields)
	if diagnostic.Event == "session_turn_completed" && side.maxTurns > 0 && turnIndex(fields) > side.maxTurns {
		return nil
	}
	if err := side.diagnostics.Write(diagnosticLine{Event: diagnostic.Event, Fields: fields}); err != nil {
		return err
	}
	side.mu.Lock()
	defer side.mu.Unlock()
	switch diagnostic.Event {
	case "session_terminal":
		if !side.terminalSeen {
			side.terminalSeen = true
			side.terminalClean = true
		}
	case "session_failure":
		if !side.terminalSeen {
			side.terminalSeen = true
			side.terminalClean = false
			if code := strings.TrimSpace(fields["provider_error_code"]); code != "" {
				side.terminalError = code
			}
		}
	}
	return nil
}

func (e *evidence) observeStream(index int, msg messages.StreamMessage) error {
	side := e.side(index)
	if side == nil || side.streamDeltas == nil {
		return errors.New("self-play stream sink is not initialized")
	}
	payload, err := gwtesting.MarshalStreamMessage(msg)
	if err != nil {
		return fmt.Errorf("marshal stream delta: %w", err)
	}
	return side.streamDeltas.WriteRaw(payload)
}

func (e *evidence) observeAudio(index int, ctx context.Context, pcm []byte) error {
	side := e.side(index)
	if side == nil || side.audio == nil {
		return errors.New("self-play WAV sink is not initialized")
	}
	return side.audio.Write(ctx, pcm)
}

func (e *evidence) observeInput(_ int, _ []byte) {}

func (e *evidence) finalize(result selfplay.Result, runErr error, endedAt time.Time) error {
	if e == nil {
		return nil
	}
	e.finalizeOnce.Do(func() {
		var closeErr error
		for _, side := range e.sides {
			if side == nil {
				continue
			}
			if side.audio != nil {
				closeErr = errors.Join(closeErr, side.audio.Close())
			}
			if side.diagnostics != nil {
				closeErr = errors.Join(closeErr, side.diagnostics.Close())
			}
			if side.streamDeltas != nil {
				closeErr = errors.Join(closeErr, side.streamDeltas.Close())
			}
		}
		effectiveErr := errors.Join(runErr, e.err(), closeErr)
		if err := e.writeManifest(result, effectiveErr, endedAt.UTC()); err != nil {
			e.finalizeErr = errors.Join(closeErr, err)
			return
		}
		e.finalizeErr = closeErr
	})
	return e.finalizeErr
}

type manifest struct {
	SchemaVersion int                      `json:"schema_version"`
	Personas      manifestPersonas         `json:"personas"`
	OpeningSeed   string                   `json:"opening_seed"`
	Provider      string                   `json:"provider"`
	Model         string                   `json:"model"`
	Timing        manifestTiming           `json:"timing"`
	Bounds        manifestBounds           `json:"bounds"`
	StopReason    selfplay.StopReason      `json:"stop_reason"`
	Agents        map[string]agentManifest `json:"agents"`
	Artifacts     map[string]string        `json:"artifacts"`
	Error         string                   `json:"error,omitempty"`
}

type manifestPersonas struct {
	Customer  string `json:"customer"`
	Assistant string `json:"assistant"`
}
type manifestTiming struct {
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
	Elapsed   string `json:"elapsed"`
}
type manifestBounds struct {
	MaxDuration string `json:"max_duration"`
	MaxTurns    int    `json:"max_turns"`
}
type agentManifest struct {
	Role           string                `json:"role"`
	Persona        string                `json:"persona"`
	CompletedTurns int                   `json:"completed_turns"`
	TerminalClean  bool                  `json:"terminal_clean"`
	TerminalError  string                `json:"terminal_error,omitempty"`
	Artifacts      agentArtifactManifest `json:"artifacts"`
}
type agentArtifactManifest struct {
	WAV          string `json:"wav"`
	Diagnostics  string `json:"diagnostics"`
	StreamDeltas string `json:"stream_deltas"`
}

func (e *evidence) writeManifest(result selfplay.Result, runErr error, endedAt time.Time) error {
	if result.StopReason == "" {
		result.StopReason = selfplay.StopFailure
	}
	value := manifest{
		SchemaVersion: selfplay.EvidenceSchemaVersion,
		Personas:      manifestPersonas{Customer: selfplay.SelfPlayCustomerPersona, Assistant: selfplay.SelfPlayAssistantPersona},
		OpeningSeed:   selfplay.SelfPlayOpeningSeed, Provider: e.provider, Model: e.model,
		Timing: manifestTiming{StartedAt: e.startedAt.UTC().Format(time.RFC3339Nano), EndedAt: endedAt.UTC().Format(time.RFC3339Nano), Elapsed: endedAt.Sub(e.startedAt).String()},
		Bounds: manifestBounds{MaxDuration: e.maxDuration.String(), MaxTurns: e.maxTurns}, StopReason: result.StopReason,
		Agents: make(map[string]agentManifest, len(e.sides)),
		Artifacts: map[string]string{
			"agent_a_wav": selfplay.AgentAWAVPath, "agent_b_wav": selfplay.AgentBWAVPath,
			"agent_a_diagnostics": selfplay.AgentADiagnosticsPath, "agent_b_diagnostics": selfplay.AgentBDiagnosticsPath,
			"agent_a_stream_deltas": selfplay.AgentAStreamDeltasPath, "agent_b_stream_deltas": selfplay.AgentBStreamDeltasPath,
		},
	}
	if runErr != nil {
		value.Error = selfplay.RedactError(runErr.Error(), e.apiKey)
	}
	for index, side := range e.sides {
		if side == nil {
			continue
		}
		side.mu.Lock()
		terminalSeen, terminalClean, terminalError := side.terminalSeen, side.terminalClean, side.terminalError
		side.mu.Unlock()
		turns := result.AssistantTurns
		if index == 0 {
			turns = result.CustomerTurns
		}
		if !terminalClean && runErr != nil {
			terminalError = runErr.Error()
		}
		if terminalError != "" {
			terminalError = selfplay.RedactError(terminalError, e.apiKey)
		}
		terminalOK := (terminalSeen && terminalClean) || (runErr == nil && result.StopReason != selfplay.StopFailure)
		value.Agents[side.id] = agentManifest{
			Role: side.role, Persona: side.persona, CompletedTurns: turns,
			TerminalClean: terminalOK, TerminalError: terminalError,
			Artifacts: agentArtifactManifest{WAV: filepath.Base(side.wavPath), Diagnostics: filepath.Base(side.diagnosticPath), StreamDeltas: filepath.Base(side.streamPath)},
		}
	}
	return selfplay.WriteAtomicJSON(filepath.Join(e.destination, selfplay.ManifestPath), value)
}
