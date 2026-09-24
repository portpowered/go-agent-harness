package internal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestSelfPlayEvidenceWriteFailureIsReturnedAndRecorded(t *testing.T) {
	files := &injectedFileSystem{writeFailure: agentADiagnosticsPath}
	service := newInjectedService(files)
	outputDir := filepath.Join(t.TempDir(), "run")
	result, err := service.Run(context.Background(), selfplay.Request{APIKey: "test-key", OutputDir: outputDir, MaxDuration: time.Second, MaxTurns: 1})
	if err == nil || !strings.Contains(err.Error(), "diagnostic evidence") {
		t.Fatalf("Run error = %v, want diagnostic write failure", err)
	}
	if result.StopReason != selfplay.StopFailure || !result.Manifest.Complete || result.Customer.Diagnostics.Complete {
		t.Fatalf("write-failure result = %#v", result)
	}
	manifest, readErr := os.ReadFile(filepath.Join(outputDir, manifestPath))
	if readErr != nil {
		t.Fatalf("read manifest: %v", readErr)
	}
	if !strings.Contains(string(manifest), "diagnostic evidence") || !strings.Contains(string(manifest), `"stop_reason": "failure"`) {
		t.Fatalf("manifest omitted failed evidence state: %s", manifest)
	}
}

func TestSelfPlayEvidenceSetupFailureCleansCreatedArtifacts(t *testing.T) {
	files := &injectedFileSystem{openFailure: agentBStreamDeltasPath}
	service := newInjectedService(files)
	outputDir := filepath.Join(t.TempDir(), "run")
	_, err := service.Run(context.Background(), selfplay.Request{APIKey: "test-key", OutputDir: outputDir, MaxDuration: time.Second, MaxTurns: 1})
	if !errors.Is(err, errInjectedEvidence) {
		t.Fatalf("Run error = %v, want injected setup failure", err)
	}
	if _, statErr := os.Stat(outputDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("setup failure left output directory behind: %v", statErr)
	}
}

func TestSelfPlayEvidenceCloseFailureReturnsCauseAndMarksArtifactIncomplete(t *testing.T) {
	files := &injectedFileSystem{closeFailure: agentADiagnosticsPath}
	service := newInjectedService(files)
	outputDir := filepath.Join(t.TempDir(), "run")
	result, err := service.Run(context.Background(), selfplay.Request{APIKey: "test-key", OutputDir: outputDir, MaxDuration: 30 * time.Millisecond, MaxTurns: 1})
	if !errors.Is(err, errInjectedEvidence) {
		t.Fatalf("Run error = %v, want injected close failure", err)
	}
	if result.StopReason != selfplay.StopMaxDuration || result.Customer.Diagnostics.Complete || !result.Manifest.Complete {
		t.Fatalf("close-failure result = %#v", result)
	}
	manifest, readErr := os.ReadFile(filepath.Join(outputDir, manifestPath))
	if readErr != nil {
		t.Fatalf("read manifest: %v", readErr)
	}
	if !strings.Contains(string(manifest), errInjectedEvidence.Error()) {
		t.Fatalf("manifest omitted close failure: %s", manifest)
	}
}

func TestSelfPlayManifestFinalizationFailureReturnsCauseWithoutClaimingComplete(t *testing.T) {
	files := &injectedFileSystem{manifestFailure: true}
	service := newInjectedService(files)
	outputDir := filepath.Join(t.TempDir(), "run")
	result, err := service.Run(context.Background(), selfplay.Request{APIKey: "test-key", OutputDir: outputDir, MaxDuration: 30 * time.Millisecond, MaxTurns: 1})
	if !errors.Is(err, errInjectedEvidence) {
		t.Fatalf("Run error = %v, want injected manifest failure", err)
	}
	if result.StopReason != selfplay.StopMaxDuration || result.Manifest.Complete || result.Manifest.Path != "" {
		t.Fatalf("manifest-failure result = %#v", result)
	}
	if _, statErr := os.Stat(filepath.Join(outputDir, manifestPath)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("manifest exists after failed finalization: %v", statErr)
	}
}

func TestSelfPlayMissingCredentialFailsBeforeSessionOrFilesystemEffects(t *testing.T) {
	sessions := &countingSessionService{}
	service := NewService(sessions, injectedCatalog{}, clock.Real{})
	outputDir := filepath.Join(t.TempDir(), "run")
	_, err := service.Run(context.Background(), selfplay.Request{OutputDir: outputDir, MaxDuration: time.Second, MaxTurns: 1})
	if !errors.Is(err, selfplay.ErrCredentialRequired) {
		t.Fatalf("Run error = %v, want missing credential", err)
	}
	if sessions.buildCalls != 0 {
		t.Fatalf("built %d sessions before rejecting the request", sessions.buildCalls)
	}
	if _, statErr := os.Stat(outputDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("missing credential created output directory: %v", statErr)
	}
}

func newInjectedService(files fileSystem) selfplay.Service {
	service := NewService(injectedSessionService{}, injectedCatalog{}, clock.Real{})
	service.files = files
	return service
}

type injectedCatalog struct{}

func (injectedCatalog) RealtimeModels(string) []providers.RealtimeModel { return nil }
func (injectedCatalog) SupportedRealtimeModelIDs(string) []string       { return nil }
func (injectedCatalog) LookupRealtimeModel(_, model string) (providers.RealtimeModel, bool) {
	return providers.RealtimeModel{ID: model, SupportsAudio: true}, true
}

type injectedSessionService struct{}

func (injectedSessionService) BuildSession(context.Context, providers.SessionConfig) (messages.SessionInferencer, error) {
	return injectedInferencer{}, nil
}

type countingSessionService struct{ buildCalls int }

func (s *countingSessionService) BuildSession(context.Context, providers.SessionConfig) (messages.SessionInferencer, error) {
	s.buildCalls++
	return injectedInferencer{}, nil
}

type injectedInferencer struct{}

func (injectedInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session := &injectedSession{receive: messages.NewTypedBuffer[messages.StreamMessage](32), done: make(chan struct{})}
	if !session.receive.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("injected", "audio")}) {
		return nil, ctx.Err()
	}
	return session, nil
}

type injectedSession struct {
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	once    sync.Once
}

func (s *injectedSession) Send(context.Context, messages.StreamMessage) bool      { return true }
func (s *injectedSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.receive }
func (s *injectedSession) Done() <-chan struct{}                                  { return s.done }
func (s *injectedSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

type injectedEvidenceError string

func (err injectedEvidenceError) Error() string { return string(err) }

const errInjectedEvidence injectedEvidenceError = "injected evidence fault"

type injectedFileSystem struct {
	osFileSystem
	openFailure     string
	writeFailure    string
	closeFailure    string
	manifestFailure bool
}

func (f *injectedFileSystem) OpenFile(path string, flag int, mode os.FileMode) (evidenceFile, error) {
	if filepath.Base(path) == f.openFailure {
		return nil, errInjectedEvidence
	}
	file, err := f.osFileSystem.OpenFile(path, flag, mode)
	if err != nil {
		return nil, err
	}
	base := filepath.Base(path)
	return &injectedFile{evidenceFile: file, writeFailure: base == f.writeFailure, closeFailure: base == f.closeFailure}, nil
}

func (f *injectedFileSystem) CreateTemp(dir, pattern string) (evidenceFile, error) {
	if f.manifestFailure {
		return nil, errInjectedEvidence
	}
	return f.osFileSystem.CreateTemp(dir, pattern)
}

type injectedFile struct {
	evidenceFile
	writeFailure bool
	closeFailure bool
}

func (f *injectedFile) Write(data []byte) (int, error) {
	if f.writeFailure {
		return 0, errInjectedEvidence
	}
	return f.evidenceFile.Write(data)
}

func (f *injectedFile) Close() error {
	closeErr := f.evidenceFile.Close()
	if f.closeFailure {
		return errors.Join(closeErr, errInjectedEvidence)
	}
	return closeErr
}

var _ evidenceFile = (*injectedFile)(nil)
