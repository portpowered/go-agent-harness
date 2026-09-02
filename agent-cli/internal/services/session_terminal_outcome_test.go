package services

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestSessionTerminalReporterReconcilesCompetingCandidatesOnce(t *testing.T) {
	var out bytes.Buffer
	reporter := newSessionTerminalReporter()
	reporter.markRunStarted()
	renderer := newSessionReplayRenderer(&out, reporter)

	if err := writeSessionReplayMessage(renderer, messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("accepted output"),
	}); err != nil {
		t.Fatalf("write accepted output: %v", err)
	}
	if err := writeSessionReplayMessage(renderer, messages.StreamMessage{
		Type: messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValueWithTerminal(
			"",
			string(SessionMaxDurationReason),
			string(SessionMaxDurationReason),
			SessionMaxDurationReason,
			messages.TerminalProvenanceLoop,
			messages.TerminalOutputPartial,
		),
	}); err != nil {
		t.Fatalf("observe pre-drain duration candidate: %v", err)
	}
	if got := out.String(); got != "Assistant: accepted output\n" {
		t.Fatalf("terminal candidate was emitted before reconciliation: %q", got)
	}

	// Replay exhaustion is discovered only after the stream and finalization
	// evidence arrive. It must replace the earlier planned candidate at the
	// single reporting boundary.
	reporter.markReplayComplete()
	if err := reporter.publish(&out, nil); err != nil {
		t.Fatalf("publish reconciled terminal: %v", err)
	}
	got := out.String()
	if strings.Count(got, "[session terminal:") != 1 || strings.Count(got, "[session replay complete]") != 1 {
		t.Fatalf("reconciled output has more than one terminal announcement: %q", got)
	}
	if !strings.Contains(got, "classification=replay_complete terminal_reason=replay_complete terminal_provenance=replay output_state=complete") {
		t.Fatalf("reconciled output lost replay completion fields: %q", got)
	}
	if strings.Contains(got, "terminal_reason=max_duration") || strings.Contains(got, "output_state=partial") {
		t.Fatalf("reconciled output retained the superseded duration candidate: %q", got)
	}
	if err := reporter.publish(&out, nil); !errors.Is(err, ErrSessionTerminalAlreadyPublished) {
		t.Fatalf("second publish error = %v, want ErrSessionTerminalAlreadyPublished", err)
	}
	if strings.Count(out.String(), "[session terminal:") != 1 {
		t.Fatalf("second publish emitted another terminal block: %q", out.String())
	}
}

func TestSessionTerminalReporterAcceptsValidPartialArtifactAfterCancellation(t *testing.T) {
	var out bytes.Buffer
	reporter := newSessionTerminalReporter()
	reporter.markRunStarted()
	reporter.markDurationExpiryWithOutput(messages.TerminalOutputPartial)
	reporter.recordArtifactFinalization(true, nil)

	if err := reporter.publish(&out, context.Canceled); err != nil {
		t.Fatalf("publish valid partial artifact: %v", err)
	}
	if reporter.outcome.artifactState != sessionTerminalArtifactValid {
		t.Fatalf("artifact state = %d, want valid", reporter.outcome.artifactState)
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
	reporter := newSessionTerminalReporter()
	reporter.markRunStarted()
	reporter.markDurationExpiryWithOutput(messages.TerminalOutputPartial)
	artifactErr := errors.New("artifact verification failed")
	reporter.recordArtifactFinalization(true, artifactErr)

	if err := reporter.publish(&out, nil); err != nil {
		t.Fatalf("publish artifact failure: %v", err)
	}
	if !errors.Is(reporter.outcome.fatalError, artifactErr) {
		t.Fatalf("fatal error = %v, want artifact failure", reporter.outcome.fatalError)
	}
	got := out.String()
	if !strings.Contains(got, "terminal_reason=terminal_failure") || strings.Contains(got, "terminal_reason=max_duration") {
		t.Fatalf("artifact failure was not retained in final terminal: %q", got)
	}
}
