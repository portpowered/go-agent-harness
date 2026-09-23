package agentruntime

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sessionterminal "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
	sessionterminalwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal/wire"
)

func TestSessionTerminalReporterReconcilesCompetingCandidatesOnce(t *testing.T) {
	var out bytes.Buffer
	reporter := sessionterminalwire.NewReporter()
	reporter.MarkRunStarted()
	reporter.ObserveStreamMessage(messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("accepted output"),
	}, false)
	reporter.ObserveStreamMessage(messages.StreamMessage{
		Type: messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValueWithTerminal(
			"",
			string(sessionterminal.MaxDurationReason),
			string(sessionterminal.MaxDurationReason),
			sessionterminal.MaxDurationReason,
			messages.TerminalProvenanceLoop,
			messages.TerminalOutputPartial,
		),
	}, true)
	reporter.MarkReplayComplete()

	if err := reporter.Publish(&out, nil); err != nil {
		t.Fatalf("publish reconciled terminal: %v", err)
	}
	got := out.String()
	if strings.Count(got, "[session terminal:") != 1 || !strings.Contains(got, "terminal_reason=replay_complete") {
		t.Fatalf("reconciled output = %q", got)
	}
	if strings.Contains(got, "terminal_reason=max_duration") || strings.Contains(got, "output_state=partial") {
		t.Fatalf("superseded duration candidate survived: %q", got)
	}
	if err := reporter.Publish(&out, nil); !errors.Is(err, sessionterminal.ErrAlreadyPublished) {
		t.Fatalf("second publish error = %v, want ErrAlreadyPublished", err)
	}
}

func TestSessionTerminalReporterAcceptsValidPartialArtifactAfterCancellation(t *testing.T) {
	var out bytes.Buffer
	reporter := sessionterminalwire.NewReporter()
	reporter.MarkRunStarted()
	reporter.MarkDurationExpiry(true, messages.TerminalOutputPartial)
	reporter.RecordArtifactFinalization(true, nil)

	if err := reporter.Publish(&out, context.Canceled); err != nil {
		t.Fatalf("publish valid partial artifact: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "terminal_reason=max_duration") || !strings.Contains(got, "output_state=partial") {
		t.Fatalf("valid partial artifact lost duration outcome: %q", got)
	}
	if strings.Contains(got, "terminal_reason=terminal_failure") {
		t.Fatalf("cancellation was promoted to a fatal terminal: %q", got)
	}
}

func TestSessionTerminalReporterPreservesIndependentArtifactFailure(t *testing.T) {
	var out bytes.Buffer
	reporter := sessionterminalwire.NewReporter()
	reporter.MarkRunStarted()
	reporter.MarkDurationExpiry(true, messages.TerminalOutputPartial)
	artifactErr := errors.New("artifact verification failed")
	reporter.RecordArtifactFinalization(true, artifactErr)

	if err := reporter.Publish(&out, nil); !errors.Is(err, artifactErr) {
		t.Fatalf("publish artifact failure = %v, want %v", err, artifactErr)
	}
	got := out.String()
	if !strings.Contains(got, "terminal_reason=terminal_failure") || strings.Contains(got, "terminal_reason=max_duration") {
		t.Fatalf("artifact failure was not retained in final terminal: %q", got)
	}
}
