package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

func TestHistoryFingerprintAndResponseAreRequestDerived(t *testing.T) {
	items := []messageRecord{{Role: string(messages.RoleUser), Text: "one"}, {Role: string(messages.RoleAssistant), Text: "two"}}
	provider := newDeterministicProvider("test", providerOptions{})
	response := provider.observe(messages.InferenceRequest{Messages: []messages.Message{
		messages.NewTextMessage(messages.RoleUser, "one"),
		messages.NewTextMessage(messages.RoleAssistant, "two"),
	}})
	if !strings.Contains(response, historyFingerprint(items)) || !strings.Contains(response, `input="one"`) {
		t.Fatalf("response %q does not encode observed history", response)
	}
}

func TestConfigRejectsRelativePathsAndTrailingJSON(t *testing.T) {
	if err := validateConfig(config{Scenario: "boundary", StoreDirectory: "relative", WorkspaceDirectory: "/tmp/work", Input: "x"}); err == nil {
		t.Fatal("relative store path was accepted")
	}
	input := strings.NewReader(`{"scenario":"boundary","store_directory":"/tmp/store","workspace_directory":"/tmp/work","input":"x"} {"scenario":"boundary"}`)
	if _, err := readConfigFrom(input); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("trailing JSON was accepted: %v", err)
	}
}

type closeErrorStream struct {
	calls int
	err   error
}

func (s *closeErrorStream) HasNext() bool                    { return false }
func (s *closeErrorStream) Response() agentloop.Response     { return agentloop.Response{} }
func (s *closeErrorStream) Err() error                       { return nil }
func (s *closeErrorStream) Outcome() agentloop.StreamOutcome { return agentloop.StreamOutcome{} }
func (s *closeErrorStream) Close() error {
	s.calls++
	return s.err
}

type closeErrorHandle struct {
	calls  int
	err    error
	stream agentloop.Stream
}

func (h *closeErrorHandle) SessionID() string { return "test-session" }
func (h *closeErrorHandle) Stream(context.Context, agentloop.ExecuteInput) (agentloop.Stream, error) {
	if h.stream == nil {
		return nil, errors.New("not implemented")
	}
	return h.stream, nil
}
func (h *closeErrorHandle) Save() error        { return nil }
func (h *closeErrorHandle) Flush(string) error { return nil }
func (h *closeErrorHandle) Close() error {
	h.calls++
	return h.err
}

func TestCloseHelpersReportBothCloseErrors(t *testing.T) {
	streamErr := errors.New("stream close failed")
	stream := &closeErrorStream{err: streamErr}
	var streamCalls int
	if err := closeStreamTwice(stream, &streamCalls); !errors.Is(err, streamErr) {
		t.Fatalf("stream close error was discarded: %v", err)
	}
	if stream.calls != closeAttempts || streamCalls != closeAttempts {
		t.Fatalf("stream close calls=%d/%d, want %d", stream.calls, streamCalls, closeAttempts)
	}

	handleErr := errors.New("handle close failed")
	handle := &closeErrorHandle{err: handleErr}
	var handleCalls int
	if err := closeHandleTwice(handle, &handleCalls); !errors.Is(err, handleErr) {
		t.Fatalf("handle close error was discarded: %v", err)
	}
	if handle.calls != closeAttempts || handleCalls != closeAttempts {
		t.Fatalf("handle close calls=%d/%d, want %d", handle.calls, handleCalls, closeAttempts)
	}
}

func TestClosedControlRequiresExactDiagnosticMarker(t *testing.T) {
	if control := closedControl(errors.New("unrelated oracle failure"), historyOracleMarker); control.FailedClosed {
		t.Fatalf("unrelated diagnostic was accepted: %+v", control)
	}
	if control := closedControl(errors.New(historyOracleMarker+" observed mismatch"), historyOracleMarker); !control.FailedClosed {
		t.Fatalf("expected history diagnostic was rejected: %+v", control)
	}
}

type emptyManagedStore struct{}

func (emptyManagedStore) Load(context.Context, string) ([]session.Message, error) { return nil, nil }
func (emptyManagedStore) Latest(context.Context) (string, error)                  { return "", nil }
func (emptyManagedStore) NewSessionID(context.Context) (string, error)            { return "test-session", nil }
func (emptyManagedStore) Save(context.Context, string, []session.Message) error   { return nil }
func (emptyManagedStore) LoadTrace(context.Context, string) (*session.TraceRecord, error) {
	return nil, nil
}
func (emptyManagedStore) SaveTrace(context.Context, session.TraceRecord) error { return nil }
func (emptyManagedStore) NewTraceID(context.Context) (string, error)           { return "test-trace", nil }
func (emptyManagedStore) List(context.Context, session.SessionListOptions) ([]session.SessionInfo, error) {
	return nil, nil
}
func (emptyManagedStore) Delete(context.Context, string) error { return nil }
func (emptyManagedStore) ListTraces(context.Context) ([]session.TraceInfo, error) {
	return nil, nil
}

type closeErrorService struct{ handle session.SessionHandle }

func (closeErrorService) Run(context.Context, session.Request) (session.Result, error) {
	return session.Result{}, errors.New("not implemented")
}
func (s closeErrorService) Open(context.Context, session.Request) (session.SessionHandle, error) {
	return s.handle, nil
}
func (closeErrorService) RunIterative(context.Context, session.Request, session.IterativeRequest) (session.IterativeResult, error) {
	return session.IterativeResult{}, errors.New("not implemented")
}
func (closeErrorService) NewSessionID(context.Context, session.Request) (string, error) {
	return "test-session", nil
}

func TestRunTurnPropagatesStreamAndHandleCloseErrors(t *testing.T) {
	streamErr := errors.New("stream close failed")
	handleErr := errors.New("handle close failed")
	stream := &closeErrorStream{err: streamErr}
	handle := &closeErrorHandle{err: handleErr, stream: stream}
	provider := newDeterministicProvider("close-errors", providerOptions{})

	result, runErr := runTurn(
		context.Background(),
		config{Scenario: "boundary", Input: "close failure"},
		runtimeInstance{service: closeErrorService{handle: handle}, store: emptyManagedStore{}},
		provider, nil, false, false, "close-errors",
	)
	if runErr == nil || !strings.Contains(runErr.Error(), streamErr.Error()) || !strings.Contains(runErr.Error(), handleErr.Error()) {
		t.Fatalf("close errors were not propagated: %v", runErr)
	}
	if result.StreamCloseCalls != closeAttempts || result.HandleCloseCalls != closeAttempts {
		t.Fatalf("close counts=%d/%d, want %d/%d", result.StreamCloseCalls, result.HandleCloseCalls, closeAttempts, closeAttempts)
	}
}

func TestIsolationCancellationWaitsForObservedPartialDelta(t *testing.T) {
	for attempt := 0; attempt < 20; attempt++ {
		root := t.TempDir()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		result, runErr := runIsolation(ctx, config{
			Scenario:            "isolation",
			StoreDirectoryA:     root + "/store-a",
			WorkspaceDirectoryA: root + "/workspace-a",
			StoreDirectoryB:     root + "/store-b",
			WorkspaceDirectoryB: root + "/workspace-b",
		})
		cancel()
		if runErr != nil {
			t.Fatalf("attempt %d isolation failed: %v", attempt, runErr)
		}
		if result.Isolation == nil || !result.Isolation.PartialAObserved {
			t.Fatalf("attempt %d canceled before A partial delta was observed: %+v", attempt, result.Isolation)
		}
		if result.Isolation.CanceledA == nil || !result.Isolation.CanceledA.Stream.Partial {
			t.Fatalf("attempt %d did not retain partial A cancellation: %+v", attempt, result.Isolation)
		}
	}
}

func TestCancellationWaitsForObservedPartialDelta(t *testing.T) {
	for attempt := 0; attempt < 20; attempt++ {
		root := t.TempDir()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		result, runErr := runCancellation(ctx, config{
			Scenario:           "cancellation",
			StoreDirectory:     root + "/store",
			WorkspaceDirectory: root + "/workspace",
			Input:              "cancellation probe",
		})
		cancel()
		if runErr != nil {
			t.Fatalf("attempt %d cancellation failed: %v", attempt, runErr)
		}
		if result.DuringCancel == nil || !result.DuringCancel.Stream.Partial {
			t.Fatalf("attempt %d did not retain a partial cancellation: %+v", attempt, result.DuringCancel)
		}
	}
}

var _ session.SessionHandle = (*closeErrorHandle)(nil)
