package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	serviceSelfPlay "github.com/portpowered/go-agent-harness/agent-cli/internal/services/selfplay"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeproviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	runtimeSelfPlay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

const (
	SelfPlayDefaultProvider    = runtimeSelfPlay.SelfPlayDefaultProvider
	SelfPlayDefaultModel       = runtimeSelfPlay.SelfPlayDefaultModel
	SelfPlayDefaultMaxDuration = runtimeSelfPlay.SelfPlayDefaultMaxDuration
	SelfPlayDefaultTurnTarget  = runtimeSelfPlay.SelfPlayDefaultTurnTarget
	SelfPlayCustomerPersona    = runtimeSelfPlay.SelfPlayCustomerPersona
	SelfPlayAssistantPersona   = runtimeSelfPlay.SelfPlayAssistantPersona
	SelfPlayOpeningSeed        = runtimeSelfPlay.SelfPlayOpeningSeed
)

// This minimal shape remains only because the general CLI model-admission
// helper still accepts the former package-local aggregate.
type SelfPlayRunOptions struct {
	Model        string
	modelCatalog runtimeproviders.ModelCatalog
}

func selfPlaySessionRunOptions(SelfPlayRunOptions) SessionRunOptions { return SessionRunOptions{} }

// NewSelfPlayService adapts the runtime service to the unchanged CLI contract.
func NewSelfPlayService(service runtimeSelfPlay.Service) serviceSelfPlay.Service {
	return cliSelfPlayService{service: service}
}

type cliSelfPlayService struct{ service runtimeSelfPlay.Service }

func (s cliSelfPlayService) Run(ctx context.Context, out io.Writer, options serviceSelfPlay.RunOptions) error {
	if s.service == nil {
		return errors.New("self-play runtime service is required")
	}
	return s.service.Run(ctx, out, runtimeSelfPlay.RunOptions{
		APIKey: options.APIKey, OutputDir: options.OutputDir, Provider: options.Provider,
		Model: options.Model, BaseURL: options.BaseURL, ConfigDir: options.ConfigDir,
		MaxDuration: options.MaxDuration, MaxTurns: options.MaxTurns,
	})
}

// NewSelfPlaySessionFactory binds the process-scoped CLI provider factory to
// the runtime's value-only construction port.
func NewSelfPlaySessionFactory(factory SessionRuntimeFactory, catalog runtimeproviders.ModelCatalog) runtimeSelfPlay.SessionFactory {
	return selfPlaySessionFactory{factory: factory, catalog: catalog}
}

type selfPlaySessionFactory struct {
	factory sessionRuntimeFactory
	catalog runtimeproviders.ModelCatalog
}

func (f selfPlaySessionFactory) NewSession(_ context.Context, request runtimeSelfPlay.SessionRequest) (messages.SessionInferencer, error) {
	return defaultSelfPlaySessionFactory(SessionRunOptions{
		Provider: request.Provider, Model: request.Model, ModelProvided: true,
		APIKey: request.APIKey, BaseURL: request.BaseURL, ConfigDir: request.ConfigDir,
		Prompt: request.Prompt, PromptProvided: request.Prompt != "", Clock: request.Clock,
		ModelCatalog: f.catalog, runtimeFactory: f.factory,
	}, request.Persona)
}

func defaultSelfPlaySessionFactory(options SessionRunOptions, instructions string) (messages.SessionInferencer, error) {
	if options.runtimeFactory.configured() {
		factory := sessionRuntimeFactoryWithInstructions(options.runtimeFactory, instructions)
		if factory.newBareLiveSessionInferencer == nil {
			return nil, errors.New("self-play session runtime factory cannot construct live sessions")
		}
		inferencer, _, err := factory.newBareLiveSessionInferencer(options)
		return inferencer, err
	}
	inferencer, _, err := NewLiveSessionInferencer(options, instructions)
	return inferencer, err
}

// NewSelfPlaySessionRunner binds the existing CLI loop to the runtime's small
// runner port. The agent loop itself is not part of the runtime contract.
func NewSelfPlaySessionRunner() runtimeSelfPlay.SessionRunner { return cliSelfPlaySessionRunner{} }

type cliSelfPlaySessionRunner struct{}
type selfPlayLoopAudioInput struct{ loop *agentloop.AgentLoop }

func (i selfPlayLoopAudioInput) SendAudioInput(ctx context.Context, pcm []byte) error {
	if i.loop == nil {
		return errors.New("self-play session loop is nil")
	}
	return i.loop.SendAudioInput(ctx, pcm)
}

type selfPlayDiagnosticSink struct {
	observe func(runtimeSelfPlay.Diagnostic)
}

func (s selfPlayDiagnosticSink) RecordSessionDiagnostic(record SessionDiagnosticRecord) {
	if s.observe != nil {
		s.observe(runtimeSelfPlay.Diagnostic{Event: record.Event, Fields: runtimeSelfPlay.CloneStringMap(record.Fields)})
	}
}

func (cliSelfPlaySessionRunner) Run(ctx context.Context, inferencer messages.SessionInferencer, options runtimeSelfPlay.SessionRunOptions) error {
	ready := make(chan *agentloop.AgentLoop, 1)
	readyCtx, cancelReady := context.WithCancel(ctx)
	defer cancelReady()
	go func() {
		select {
		case loop := <-ready:
			if loop == nil || options.Ready == nil {
				return
			}
			select {
			case options.Ready <- selfPlayLoopAudioInput{loop: loop}:
			case <-readyCtx.Done():
			}
		case <-readyCtx.Done():
		}
	}()
	observer := newSessionProgressObserver(selfPlayDiagnosticSink{observe: options.ObserveDiagnostic}, nil, options.Provider, options.Model)
	observer.turnAdmission = options.AdmitTurn
	observer.streamObserver = options.ObserveStream
	return runAgentLoopSession(ctx, io.Discard, inferencer, sessionLoopOptions{
		Prompt: options.Prompt, WaitForClose: true, Done: options.Done, DoneErr: options.DoneErr,
		observer: observer, loopReady: ready, clockSource: options.Clock,
		livenessClock: sessionLivenessClockFromSource(options.Clock),
	})
}

const (
	SelfPlayAgentAWAVPath          = runtimeSelfPlay.AgentAWAVPath
	SelfPlayAgentBWAVPath          = runtimeSelfPlay.AgentBWAVPath
	SelfPlayAgentADiagnosticsPath  = runtimeSelfPlay.AgentADiagnosticsPath
	SelfPlayAgentBDiagnosticsPath  = runtimeSelfPlay.AgentBDiagnosticsPath
	SelfPlayAgentAStreamDeltasPath = runtimeSelfPlay.AgentAStreamDeltasPath
	SelfPlayAgentBStreamDeltasPath = runtimeSelfPlay.AgentBStreamDeltasPath
	SelfPlayManifestPath           = runtimeSelfPlay.ManifestPath
)

// Room evidence retains these narrow names while encoding policy lives in the
// public runtime evidence package.
type selfPlayDiagnosticLine struct {
	Event  string            `json:"event"`
	Fields map[string]string `json:"fields,omitempty"`
}

func cloneSelfPlayStringMap(fields map[string]string) map[string]string {
	return runtimeSelfPlay.CloneStringMap(fields)
}

func redactSelfPlayError(value, secret string) string {
	return runtimeSelfPlay.RedactError(value, secret)
}

type selfPlayJSONLWriter struct {
	path   string
	file   *os.File
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
	if err := runtimeSelfPlay.WriteJSONLine(w.file, data); err != nil {
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
	if w.file != nil {
		if err := w.file.Sync(); err != nil {
			w.err = errors.Join(w.err, fmt.Errorf("sync %s: %w", w.path, err))
		}
		if err := w.file.Close(); err != nil {
			w.err = errors.Join(w.err, fmt.Errorf("close %s: %w", w.path, err))
		}
	}
	return w.err
}

type selfPlayWAVRecorder struct {
	path       string
	sampleRate int
	file       *os.File
	mu         sync.Mutex
	dataBytes  uint64
	closed     bool
	err        error
}

func newSelfPlayWAVRecorder(path string, sampleRate int) (*selfPlayWAVRecorder, error) {
	if sampleRate <= 0 {
		return nil, fmt.Errorf("WAV sample rate must be positive, got %d", sampleRate)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	header, err := wavio.PCM16Header(sampleRate, 0)
	if err == nil {
		_, err = runtimeSelfPlay.WriteAll(file, header[:])
	}
	if err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("write WAV header: %w", err)
	}
	return &selfPlayWAVRecorder{path: path, sampleRate: sampleRate, file: file}, nil
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
	written, err := runtimeSelfPlay.WriteAll(w.file, pcm)
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
	if header, err := wavio.PCM16Header(w.sampleRate, w.dataBytes); err != nil {
		w.err = errors.Join(w.err, err)
	} else if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		w.err = errors.Join(w.err, fmt.Errorf("seek %s for WAV header: %w", w.path, err))
	} else if _, err := runtimeSelfPlay.WriteAll(w.file, header[:]); err != nil {
		w.err = errors.Join(w.err, fmt.Errorf("finalize %s WAV header: %w", w.path, err))
	}
	if err := w.file.Sync(); err != nil {
		w.err = errors.Join(w.err, fmt.Errorf("sync %s: %w", w.path, err))
	}
	if err := w.file.Close(); err != nil {
		w.err = errors.Join(w.err, fmt.Errorf("close %s: %w", w.path, err))
	}
	return w.err
}

func writeSelfPlayAll(writer io.Writer, data []byte) error {
	_, err := runtimeSelfPlay.WriteAll(writer, data)
	return err
}

func writeSelfPlayAllCount(writer io.Writer, data []byte) (int, error) {
	return runtimeSelfPlay.WriteAll(writer, data)
}
