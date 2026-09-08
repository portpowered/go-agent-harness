package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	session "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/persistence"
)

const savedSessionID = "saved"

func TestExecutorRequestInitialHistoryUsesExplicitStore(t *testing.T) {
	workspace := t.TempDir()
	storage := session.NewStorage(workspace)
	wantHistory := []messages.Message{messages.NewTextMessage(messages.RoleUser, savedSessionID)}
	if err := storage.Save(savedSessionID, wantHistory); err != nil {
		t.Fatalf("save session: %v", err)
	}
	exec := resolvedExecutorForTest(t, stubInferencer{}, storage, nil, nil)

	history, id, err := exec.getInitialHistory(&Config{SessionID: savedSessionID}, storage)
	if err != nil {
		t.Fatalf("session-id history error = %v", err)
	}
	if id != savedSessionID || len(history) != 1 || history[0].TextContent() != savedSessionID {
		t.Fatalf("session-id result = (%q, %#v), want saved user message", id, history)
	}

	latestHistory, latestID, err := exec.getInitialHistory(&Config{ContinueLastSession: true}, storage)
	if err != nil {
		t.Fatalf("continue-last history error = %v", err)
	}
	if latestID != savedSessionID || len(latestHistory) != 1 || latestHistory[0].TextContent() != savedSessionID {
		t.Fatalf("latest result = (%q, %#v), want saved user message", latestID, latestHistory)
	}

}

func TestExecutorRequestInitialHistoryCopiesInput(t *testing.T) {
	storage := session.NewStorage(t.TempDir())
	exec := resolvedExecutorForTest(t, stubInferencer{}, storage, nil, nil)
	initial := []messages.Message{messages.NewTextMessage(messages.RoleUser, iterativeInitialPrompt)}
	initialHistory, initialID, err := exec.getInitialHistory(&Config{InitialHistory: initial}, storage)
	if err != nil || initialID == "" || len(initialHistory) != 1 || initialHistory[0].TextContent() != iterativeInitialPrompt {
		t.Fatalf("provided history result = (%q, %#v, %v), want exact history and new ID", initialID, initialHistory, err)
	}
	initial[0] = messages.NewTextMessage(messages.RoleUser, "mutated")
	if initialHistory[0].TextContent() != iterativeInitialPrompt {
		t.Fatal("initial history was not copied")
	}

	emptyHistory, emptyID, err := exec.getInitialHistory(&Config{}, storage)
	if err != nil || emptyHistory != nil || emptyID == "" {
		t.Fatalf("empty history result = (%q, %#v, %v), want new ID and nil history", emptyID, emptyHistory, err)
	}

}

func TestExecutorRequestInitialHistoryReportsStorageErrors(t *testing.T) {
	exec := resolvedExecutorForTest(t, stubInferencer{}, nil, nil, nil)
	noSessions := session.NewStorage(t.TempDir())
	if _, _, err := exec.getInitialHistory(&Config{ContinueLastSession: true}, noSessions); err == nil || !strings.Contains(err.Error(), "no previous session to continue") {
		t.Fatalf("empty continue error = %v, want no-previous-session context", err)
	}

	badRoot := filepath.Join(t.TempDir(), "session-root-file")
	if err := os.MkdirAll(filepath.Join(badRoot, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badRoot, "sessions", "session-bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	badStorage := session.NewStorage(badRoot)
	if _, _, err := exec.getInitialHistory(&Config{SessionID: "bad"}, badStorage); err == nil || !strings.Contains(err.Error(), "load session bad") {
		t.Fatalf("bad session error = %v, want load context", err)
	}
}

func TestExecutorRequestRequiresExplicitStorage(t *testing.T) {
	exec := NewExecutor(nil, nil, stubInferencer{}, true).WithResolution(RuntimeResolution{Resolved: true})
	if _, err := exec.getSessionStorage(); err == nil || !strings.Contains(err.Error(), "session storage is required") {
		t.Fatalf("getSessionStorage() error = %v, want explicit store error", err)
	}
	if _, err := exec.NewChatSessionID(&Config{}); err == nil || !strings.Contains(err.Error(), "session storage is required") {
		t.Fatalf("NewChatSessionID() error = %v, want explicit store error", err)
	}
}

func TestExecutorRequestIDErrorsPreserveCause(t *testing.T) {
	sentinel := errors.New("ID source failed")
	storage := &requestIDFailureStorage{sessionErr: sentinel, traceErr: sentinel}
	if _, err := newSessionID(storage); !errors.Is(err, sentinel) {
		t.Fatalf("newSessionID() error = %v, want sentinel", err)
	}
	if _, err := newTraceID(storage); !errors.Is(err, sentinel) {
		t.Fatalf("newTraceID() error = %v, want sentinel", err)
	}
}

func TestExecutorRequestEmptyToolSurfaceStaysEmpty(t *testing.T) {
	exec := resolvedExecutorForTest(t, stubInferencer{}, nil, nil, nil)
	capability, err := exec.resolveToolCapability(context.Background(), t.TempDir(), nil, stubInferencer{})
	if err != nil {
		t.Fatalf("resolveToolCapability() error = %v", err)
	}
	if capability.Executor != nil || len(capability.Definitions) != 0 {
		t.Fatalf("empty capability = %+v, want no executor or definitions", capability)
	}
}

type requestIDFailureStorage struct {
	sessionErr error
	traceErr   error
}

func (s *requestIDFailureStorage) Load(string) ([]messages.Message, error)        { return nil, nil }
func (s *requestIDFailureStorage) Latest() (string, error)                        { return "", nil }
func (s *requestIDFailureStorage) NewSessionID() string                           { return "" }
func (s *requestIDFailureStorage) NewSessionIDWithError() (string, error)         { return "", s.sessionErr }
func (s *requestIDFailureStorage) Save(string, []messages.Message) error          { return nil }
func (s *requestIDFailureStorage) WorkspaceDir() string                           { return "" }
func (s *requestIDFailureStorage) LoadTrace(string) (*session.TraceRecord, error) { return nil, nil }
func (s *requestIDFailureStorage) SaveTrace(session.TraceRecord) error            { return nil }
func (s *requestIDFailureStorage) NewTraceID() string                             { return "" }
func (s *requestIDFailureStorage) NewTraceIDWithError() (string, error)           { return "", s.traceErr }
