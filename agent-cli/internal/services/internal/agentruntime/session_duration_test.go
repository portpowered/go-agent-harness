package agentruntime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestSessionDurationAdmission_PreservesCompleteMessageCapabilities(t *testing.T) {
	inner := &durationCompleteMessageSession{}
	durationService := testSessionRuntimeFactory().durationService
	wrapped := durationService.NewAdmissionSession(context.Background(), inner, durationService.NewEventAdmission(), nil)
	message := messages.NewTextMessage(messages.RoleUser, "image result")

	if !wrapped.SendMessage(context.Background(), message) {
		t.Fatal("duration admission rejected a complete message")
	}
	if !wrapped.SendMessageWithoutResponse(context.Background(), message) {
		t.Fatal("duration admission rejected a deferred complete message")
	}
	if !wrapped.SupportsCompleteMessages() {
		t.Fatal("duration admission hid complete-message capability")
	}
	if !wrapped.SupportsCompleteMessagesWithoutResponse() {
		t.Fatal("duration admission hid deferred complete-message capability")
	}
	if len(inner.messages) != 1 || len(inner.deferredMessages) != 1 {
		t.Fatalf("forwarded complete messages = %d/%d, want one of each", len(inner.messages), len(inner.deferredMessages))
	}
}

func TestSessionDurationAdmission_ForwardsNonTerminalDiagnosticWithoutShutdown(t *testing.T) {
	msg := messages.StreamMessage{
		Type:  messages.StreamTypeError,
		Value: messages.NewNonTerminalErrorValue("response is not active", "response_cancel_not_active"),
	}
	durationService := testSessionRuntimeFactory().durationService
	if durationService.IsDurationShutdownMessage(msg) {
		t.Fatal("nonterminal provider diagnostic is a shutdown message")
	}
	if !durationService.IsDurationForwardMessage(msg) {
		t.Fatal("nonterminal provider diagnostic was not retained for forwarding")
	}
}

func TestSessionCommandHelpAndOmittedDurationBehavior(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not return the test file")
	}
	moduleDir := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", "..", "..", ".."))
	// A bare session is a live-device admission path. Keep this help smoke test
	// on explicit non-session-mode invocations so it never needs a credential or
	// audio device.
	binaryPath := filepath.Join(t.TempDir(), "agent")
	build := exec.Command("go", "build", "-o", binaryPath, "./cmd/agent")
	build.Dir = moduleDir
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build agent: %v\n%s", err, output)
	}
	for _, args := range [][]string{{"session", "--help"}, {"session", "--browser-headless"}} {
		cmd := exec.Command(binaryPath, args...)
		cmd.Dir = moduleDir
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("agent %v: %v\n%s", args, err, output)
		}
		if !strings.Contains(string(output), "--max-duration") {
			t.Fatalf("agent %v help omitted --max-duration:\n%s", args, output)
		}
	}
}

type durationCompleteMessageSession struct {
	messages         []messages.Message
	deferredMessages []messages.Message
}

func (s *durationCompleteMessageSession) Send(context.Context, messages.StreamMessage) bool {
	return true
}
func (s *durationCompleteMessageSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return nil
}
func (s *durationCompleteMessageSession) Done() <-chan struct{} { return nil }
func (s *durationCompleteMessageSession) Close() error          { return nil }
func (s *durationCompleteMessageSession) SendMessage(_ context.Context, message messages.Message) bool {
	s.messages = append(s.messages, message)
	return true
}
func (s *durationCompleteMessageSession) SendMessageWithoutResponse(_ context.Context, message messages.Message) bool {
	s.deferredMessages = append(s.deferredMessages, message)
	return true
}
func (s *durationCompleteMessageSession) SupportsCompleteMessages() bool { return true }
func (s *durationCompleteMessageSession) SupportsCompleteMessagesWithoutResponse() bool {
	return true
}
