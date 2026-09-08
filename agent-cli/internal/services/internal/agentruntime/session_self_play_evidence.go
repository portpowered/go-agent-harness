package agentruntime

import "github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	// These paths are part of the Phase 1 evidence bundle contract. They are
	// relative to SelfPlayRunOptions.OutputDir and contain no user input.
	SelfPlayAgentAWAVPath          = "agent-a.wav"
	SelfPlayAgentBWAVPath          = "agent-b.wav"
	SelfPlayAgentADiagnosticsPath  = "agent-a-diagnostics.jsonl"
	SelfPlayAgentBDiagnosticsPath  = "agent-b-diagnostics.jsonl"
	SelfPlayAgentAStreamDeltasPath = "agent-a-stream-deltas.jsonl"
	SelfPlayAgentBStreamDeltasPath = "agent-b-stream-deltas.jsonl"
	SelfPlayManifestPath           = "run-manifest.json"

	selfPlayManifestSchemaVersion = 1
	selfPlayWAVHeaderSize         = 44
	selfPlaySampleRate            = 24000
)

type selfPlayEvidence struct {
	destination string
	startedAt   time.Time
	apiKey      string
	provider    string
	model       string
	maxDuration time.Duration
	maxTurns    int
	sides       [2]*selfPlaySideEvidence

	mu        sync.Mutex
	recordErr error

	finalizeOnce sync.Once
	finalizeErr  error
}

type selfPlaySideEvidence struct {
	id      string
	role    string
	persona string

	audio         *selfPlayWAVRecorder
	diagnostics   *selfPlayJSONLWriter
	streamDeltas  *selfPlayJSONLWriter
	runtime       *selfPlayRuntimeEvidence
	runtimeRecord *sessionRuntimeObservationRecorder
	diagnosticErr func(error)
	maxTurns      int
}

type selfPlayRuntimeEvidence struct {
	mu sync.Mutex

	turns        int
	terminalSeen bool
	terminal     SessionRuntimeObservation
}

var _ SessionRuntimeObserver = (*selfPlayRuntimeEvidence)(nil)

func (r *selfPlayRuntimeEvidence) ObserveSessionRuntime(observation SessionRuntimeObservation) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch observation.Kind {
	case SessionRuntimeObservationTurnCompleted:
		if observation.TurnsCompleted > r.turns {
			r.turns = observation.TurnsCompleted
		}
	case SessionRuntimeObservationTerminal:
		r.terminalSeen = true
		r.terminal = observation
		if observation.TurnsCompleted > r.turns {
			r.turns = observation.TurnsCompleted
		}
	}
}

func (r *selfPlayRuntimeEvidence) snapshot() (turns int, terminalSeen bool, terminal SessionRuntimeObservation) {
	if r == nil {
		return 0, false, SessionRuntimeObservation{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.turns, r.terminalSeen, r.terminal
}

func newSelfPlayEvidence(destination string, opts SelfPlayRunOptions, startedAt time.Time) (*selfPlayEvidence, error) {
	evidence := &selfPlayEvidence{
		destination: destination,
		startedAt:   startedAt.UTC(),
		apiKey:      opts.APIKey,
		provider:    opts.Provider,
		model:       opts.Model,
		maxDuration: opts.MaxDuration,
		maxTurns:    opts.MaxTurns,
	}

	configs := []struct {
		id           string
		role         string
		persona      string
		wavPath      string
		diagnostics  string
		streamDeltas string
	}{
		{
			id:           "agent-a",
			role:         "customer",
			persona:      SelfPlayCustomerPersona,
			wavPath:      SelfPlayAgentAWAVPath,
			diagnostics:  SelfPlayAgentADiagnosticsPath,
			streamDeltas: SelfPlayAgentAStreamDeltasPath,
		},
		{
			id:           "agent-b",
			role:         "assistant",
			persona:      SelfPlayAssistantPersona,
			wavPath:      SelfPlayAgentBWAVPath,
			diagnostics:  SelfPlayAgentBDiagnosticsPath,
			streamDeltas: SelfPlayAgentBStreamDeltasPath,
		},
	}

	for index, config := range configs {
		side := &selfPlaySideEvidence{
			id:       config.id,
			role:     config.role,
			persona:  config.persona,
			runtime:  &selfPlayRuntimeEvidence{},
			maxTurns: opts.MaxTurns,
		}
		var err error
		side.audio, err = newSelfPlayWAVRecorder(filepath.Join(destination, config.wavPath), selfPlaySampleRate)
		if err != nil {
			evidence.cleanupSetup()
			return nil, fmt.Errorf("create %s WAV evidence: %w", config.id, err)
		}
		side.diagnostics, err = newSelfPlayJSONLWriter(filepath.Join(destination, config.diagnostics))
		if err != nil {
			evidence.sides[index] = side
			evidence.cleanupSetup()
			return nil, fmt.Errorf("create %s diagnostics evidence: %w", config.id, err)
		}
		side.streamDeltas, err = newSelfPlayJSONLWriter(filepath.Join(destination, config.streamDeltas))
		if err != nil {
			evidence.sides[index] = side
			evidence.cleanupSetup()
			return nil, fmt.Errorf("create %s stream evidence: %w", config.id, err)
		}
		side.runtimeRecord = newSessionRuntimeObservationRecorder(side.runtime, opts.clock)
		evidence.sides[index] = side
	}
	return evidence, nil
}

func (e *selfPlayEvidence) cleanupSetup() {
	if e == nil {
		return
	}
	for _, side := range e.sides {
		if side == nil {
			continue
		}
		if side.audio != nil {
			_ = side.audio.close()
		}
		if side.diagnostics != nil {
			_ = side.diagnostics.close()
		}
		if side.streamDeltas != nil {
			_ = side.streamDeltas.close()
		}
		for _, path := range []string{
			filepath.Join(e.destination, side.id+".wav"),
			filepath.Join(e.destination, side.id+"-diagnostics.jsonl"),
			filepath.Join(e.destination, side.id+"-stream-deltas.jsonl"),
		} {
			_ = os.Remove(path)
		}
	}
}

func (e *selfPlayEvidence) side(index int) *selfPlaySideEvidence {
	if e == nil || index < 0 || index >= len(e.sides) {
		return nil
	}
	return e.sides[index]
}

func (e *selfPlayEvidence) fail(err error) {
	if e == nil || err == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.recordErr == nil {
		e.recordErr = err
	}
}

func (e *selfPlayEvidence) err() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.recordErr
}

func (s *selfPlaySideEvidence) observeDiagnostic(record SessionDiagnosticRecord) error {
	if s == nil || s.diagnostics == nil {
		return errors.New("self-play diagnostics sink is not initialized")
	}
	return s.diagnostics.write(selfPlayDiagnosticLine{
		Event:  record.Event,
		Fields: cloneSelfPlayStringMap(record.Fields),
	})
}

func (s *selfPlaySideEvidence) RecordSessionDiagnostic(record SessionDiagnosticRecord) {
	if record.Event == SessionDiagnosticEventTurn && s.maxTurns > 0 {
		if turnIndex, err := strconv.Atoi(record.Fields[fieldTurnIndex]); err == nil && turnIndex > s.maxTurns {
			// The session runner drains queued deltas after the coordinator has
			// reached its bound. Keep those raw stream deltas, but do not count
			// a post-bound response as part of the bounded run's turn evidence.
			return
		}
	}
	if err := s.observeDiagnostic(record); err != nil && s.diagnosticErr != nil {
		s.diagnosticErr(err)
	}
}

func (s *selfPlaySideEvidence) observeStreamDelta(msg messages.StreamMessage) error {
	if s == nil || s.streamDeltas == nil {
		return errors.New("self-play stream sink is not initialized")
	}
	payload, err := gwtesting.MarshalStreamMessage(msg)
	if err != nil {
		return fmt.Errorf("marshal stream delta: %w", err)
	}
	return s.streamDeltas.writeRaw(payload)
}

func (s *selfPlaySideEvidence) observeAudio(ctx context.Context, pcm []byte) error {
	if s == nil || s.audio == nil {
		return errors.New("self-play WAV sink is not initialized")
	}
	return s.audio.write(ctx, pcm)
}

func cloneSelfPlayStringMap(fields map[string]string) map[string]string {
	if fields == nil {
		return nil
	}
	copyOf := make(map[string]string, len(fields))
	for key, value := range fields {
		copyOf[key] = value
	}
	return copyOf
}

type selfPlayDiagnosticLine struct {
	Event  string            `json:"event"`
	Fields map[string]string `json:"fields,omitempty"`
}

type selfPlayJSONLWriter struct {
	path string
	file *os.File

	mu     sync.Mutex
	closed bool
	err    error
}

func newSelfPlayJSONLWriter(path string) (*selfPlayJSONLWriter, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &selfPlayJSONLWriter{path: path, file: file}, nil
}

func (w *selfPlayJSONLWriter) write(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal JSONL record: %w", err)
	}
	return w.writeRaw(data)
}

func (w *selfPlayJSONLWriter) writeRaw(data []byte) error {
	if w == nil {
		return errors.New("self-play JSONL writer is nil")
	}
	if !json.Valid(data) {
		return errors.New("self-play JSONL record is not valid JSON")
	}
	line := make([]byte, 0, len(data)+1)
	line = append(line, data...)
	line = append(line, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		if w.err != nil {
			return w.err
		}
		return errors.New("self-play JSONL writer is closed")
	}
	if w.err != nil {
		return w.err
	}
	if err := writeSelfPlayAll(w.file, line); err != nil {
		w.err = fmt.Errorf("write %s: %w", w.path, err)
		return w.err
	}
	return nil
}

func (w *selfPlayJSONLWriter) close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.err
	}
	w.closed = true
	if syncErr := w.file.Sync(); syncErr != nil {
		w.err = errors.Join(w.err, fmt.Errorf("sync %s: %w", w.path, syncErr))
	}
	if closeErr := w.file.Close(); closeErr != nil {
		w.err = errors.Join(w.err, fmt.Errorf("close %s: %w", w.path, closeErr))
	}
	return w.err
}

type selfPlayWAVRecorder struct {
	path       string
	sampleRate int
	file       *os.File

	mu        sync.Mutex
	dataBytes uint64
	closed    bool
	err       error
}

func newSelfPlayWAVRecorder(path string, sampleRate int) (*selfPlayWAVRecorder, error) {
	if sampleRate <= 0 {
		return nil, fmt.Errorf("WAV sample rate must be positive, got %d", sampleRate)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	recorder := &selfPlayWAVRecorder{path: path, sampleRate: sampleRate, file: file}
	header, err := wavio.PCM16Header(sampleRate, 0)
	if err == nil {
		_, err = writeSelfPlayAllCount(file, header[:])
	}
	if err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("write WAV header: %w", err)
	}
	return recorder, nil
}

func (w *selfPlayWAVRecorder) write(ctx context.Context, pcm []byte) error {
	if w == nil {
		return errors.New("self-play WAV recorder is nil")
	}
	if ctx != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	if len(pcm) == 0 {
		return nil
	}
	if len(pcm)%2 != 0 {
		return fmt.Errorf("PCM16 audio delta has odd byte length %d", len(pcm))
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		if w.err != nil {
			return w.err
		}
		return errors.New("self-play WAV recorder is closed")
	}
	if w.err != nil {
		return w.err
	}
	written, err := writeSelfPlayAllCount(w.file, pcm)
	w.dataBytes += uint64(written)
	if err != nil {
		w.err = fmt.Errorf("write %s: %w", w.path, err)
	}
	return w.err
}

func (w *selfPlayWAVRecorder) close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.err
	}
	w.closed = true

	if header, headerErr := wavio.PCM16Header(w.sampleRate, w.dataBytes); headerErr != nil {
		w.err = errors.Join(w.err, headerErr)
	} else if _, writeErr := w.file.Seek(0, io.SeekStart); writeErr != nil {
		w.err = errors.Join(w.err, fmt.Errorf("seek %s for WAV header: %w", w.path, writeErr))
	} else if _, writeErr := writeSelfPlayAllCount(w.file, header[:]); writeErr != nil {
		w.err = errors.Join(w.err, fmt.Errorf("finalize %s WAV header: %w", w.path, writeErr))
	}
	if syncErr := w.file.Sync(); syncErr != nil {
		w.err = errors.Join(w.err, fmt.Errorf("sync %s: %w", w.path, syncErr))
	}
	if closeErr := w.file.Close(); closeErr != nil {
		w.err = errors.Join(w.err, fmt.Errorf("close %s: %w", w.path, closeErr))
	}
	return w.err
}

func writeSelfPlayAll(writer io.Writer, data []byte) error {
	_, err := writeSelfPlayAllCount(writer, data)
	return err
}

func writeSelfPlayAllCount(writer io.Writer, data []byte) (int, error) {
	total := 0
	for len(data) > 0 {
		written, err := writer.Write(data)
		if written < 0 || written > len(data) {
			return total, fmt.Errorf("%w: writer returned invalid byte count %d", io.ErrShortWrite, written)
		}
		total += written
		if err != nil {
			return total, err
		}
		if written == 0 {
			return total, io.ErrShortWrite
		}
		data = data[written:]
	}
	return total, nil
}

type selfPlayManifest struct {
	SchemaVersion int                              `json:"schema_version"`
	Personas      selfPlayManifestPersonas         `json:"personas"`
	OpeningSeed   string                           `json:"opening_seed"`
	Provider      string                           `json:"provider"`
	Model         string                           `json:"model"`
	Timing        selfPlayManifestTiming           `json:"timing"`
	Bounds        selfPlayManifestBounds           `json:"bounds"`
	StopReason    SelfPlayStopReason               `json:"stop_reason"`
	Agents        map[string]selfPlayAgentManifest `json:"agents"`
	Artifacts     map[string]string                `json:"artifacts"`
	Error         string                           `json:"error,omitempty"`
}

type selfPlayManifestPersonas struct {
	Customer  string `json:"customer"`
	Assistant string `json:"assistant"`
}

type selfPlayManifestTiming struct {
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
	Elapsed   string `json:"elapsed"`
}

type selfPlayManifestBounds struct {
	MaxDuration string `json:"max_duration"`
	MaxTurns    int    `json:"max_turns"`
}

type selfPlayAgentManifest struct {
	Role           string                        `json:"role"`
	Persona        string                        `json:"persona"`
	CompletedTurns int                           `json:"completed_turns"`
	TerminalClean  bool                          `json:"terminal_clean"`
	TerminalError  string                        `json:"terminal_error,omitempty"`
	Artifacts      selfPlayAgentArtifactManifest `json:"artifacts"`
}

type selfPlayAgentArtifactManifest struct {
	WAV          string `json:"wav"`
	Diagnostics  string `json:"diagnostics"`
	StreamDeltas string `json:"stream_deltas"`
}

func (e *selfPlayEvidence) finalize(result SelfPlayResult, runErr error, endedAt time.Time) error {
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
				closeErr = errors.Join(closeErr, side.audio.close())
			}
			if side.diagnostics != nil {
				closeErr = errors.Join(closeErr, side.diagnostics.close())
			}
			if side.streamDeltas != nil {
				closeErr = errors.Join(closeErr, side.streamDeltas.close())
			}
		}

		effectiveErr := errors.Join(runErr, e.err(), closeErr)
		if manifestErr := e.writeManifest(result, effectiveErr, endedAt.UTC()); manifestErr != nil {
			e.finalizeErr = errors.Join(closeErr, manifestErr)
			return
		}
		e.finalizeErr = closeErr
	})
	return e.finalizeErr
}

func (e *selfPlayEvidence) writeManifest(result SelfPlayResult, runErr error, endedAt time.Time) error {
	if e == nil {
		return nil
	}
	if result.StopReason == "" {
		result.StopReason = SelfPlayStopFailure
	}
	manifest := selfPlayManifest{
		SchemaVersion: selfPlayManifestSchemaVersion,
		Personas: selfPlayManifestPersonas{
			Customer:  SelfPlayCustomerPersona,
			Assistant: SelfPlayAssistantPersona,
		},
		OpeningSeed: SelfPlayOpeningSeed,
		Provider:    e.provider,
		Model:       e.model,
		Timing: selfPlayManifestTiming{
			StartedAt: e.startedAt.UTC().Format(time.RFC3339Nano),
			EndedAt:   endedAt.UTC().Format(time.RFC3339Nano),
			Elapsed:   endedAt.Sub(e.startedAt).String(),
		},
		Bounds: selfPlayManifestBounds{
			MaxDuration: e.maxDuration.String(),
			MaxTurns:    e.maxTurns,
		},
		StopReason: result.StopReason,
		Agents:     make(map[string]selfPlayAgentManifest, len(e.sides)),
		Artifacts: map[string]string{
			"agent_a_wav":           SelfPlayAgentAWAVPath,
			"agent_b_wav":           SelfPlayAgentBWAVPath,
			"agent_a_diagnostics":   SelfPlayAgentADiagnosticsPath,
			"agent_b_diagnostics":   SelfPlayAgentBDiagnosticsPath,
			"agent_a_stream_deltas": SelfPlayAgentAStreamDeltasPath,
			"agent_b_stream_deltas": SelfPlayAgentBStreamDeltasPath,
		},
	}
	// The values are set from the normalized run options by the caller before
	// this method is used. Keeping assembly here makes the final write atomic.
	if runErr != nil {
		manifest.Error = redactSelfPlayError(runErr.Error(), e.apiKey)
	}
	for index, side := range e.sides {
		if side == nil {
			continue
		}
		_, terminalSeen, terminal := side.runtime.snapshot()
		turns := result.AssistantTurns
		if index == 0 {
			turns = result.CustomerTurns
		}
		// Completed-turn counts come only from the shared stop-owner snapshot.
		// The session runner may drain queued deltas after that boundary, but
		// those raw events must not let evidence mix a later runtime observation
		// into the terminal result.
		terminalClean := terminalSeen && terminal.Clean
		terminalError := ""
		if terminalSeen {
			terminalError = redactSelfPlayError(terminal.Error, e.apiKey)
		} else if runErr != nil {
			terminalError = redactSelfPlayError(runErr.Error(), e.apiKey)
		}
		manifest.Agents[side.id] = selfPlayAgentManifest{
			Role:           side.role,
			Persona:        side.persona,
			CompletedTurns: turns,
			TerminalClean:  terminalClean,
			TerminalError:  terminalError,
			Artifacts: selfPlayAgentArtifactManifest{
				WAV:          filepath.Base(side.audio.path),
				Diagnostics:  filepath.Base(side.diagnostics.path),
				StreamDeltas: filepath.Base(side.streamDeltas.path),
			},
		}
	}
	return writeSelfPlayManifestFile(filepath.Join(e.destination, SelfPlayManifestPath), manifest)
}

func writeSelfPlayManifestFile(path string, manifest selfPlayManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal self-play manifest: %w", err)
	}
	data = append(data, '\n')

	destination := filepath.Dir(path)
	temporary, err := os.CreateTemp(destination, ".run-manifest-*.tmp")
	if err != nil {
		return fmt.Errorf("create self-play manifest temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := writeSelfPlayAll(temporary, data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write self-play manifest temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync self-play manifest temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close self-play manifest temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace self-play manifest: %w", err)
	}
	removeTemporary = false
	return nil
}

func redactSelfPlayError(value, secret string) string {
	if value == "" {
		return ""
	}
	if secret != "" {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	for _, marker := range []string{
		"authorization: bearer ",
		"authorization=bearer ",
		"authorization: ",
		"authorization=",
		"x-api-key: ",
		"x-api-key=",
		"api-key: ",
		"api-key=",
		"api_key: ",
		"api_key=",
		"bearer ",
	} {
		for {
			lower := strings.ToLower(value)
			markerStart := strings.Index(lower, marker)
			if markerStart < 0 {
				break
			}
			markerEnd := markerStart + len(marker)
			if strings.HasPrefix(value[markerEnd:], "[REDACTED]") {
				break
			}
			end := markerEnd
			for end < len(value) {
				switch value[end] {
				case ' ', '\t', '\r', '\n', ',', ';', ')', ']', '}':
					goto tokenEnd
				default:
					end++
				}
			}
		tokenEnd:
			value = value[:markerEnd] + "[REDACTED]" + value[end:]
		}
	}
	return value
}
