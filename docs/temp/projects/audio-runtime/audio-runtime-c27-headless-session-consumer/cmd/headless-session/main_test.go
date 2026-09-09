package main

import (
	"context"
	"errors"
	"strings"
	"testing"

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
	calls int
	err   error
}

func (h *closeErrorHandle) SessionID() string { return "test-session" }
func (h *closeErrorHandle) Stream(context.Context, agentloop.ExecuteInput) (agentloop.Stream, error) {
	return nil, errors.New("not implemented")
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

var _ session.SessionHandle = (*closeErrorHandle)(nil)
