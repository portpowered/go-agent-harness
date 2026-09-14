package service

import (
	"fmt"
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
)

func NewReporter() sessionterminal.Reporter {
	return newTerminalReporter()
}

func (r *terminalReporter) MarkRunStarted() {
	r.markRunStarted()
}

func (r *terminalReporter) ObserveStreamMessage(msg messages.StreamMessage, leadingNewline bool) {
	r.observeStreamMessage(msg, leadingNewline)
}

func (r *terminalReporter) MarkDurationExpiry(planned bool, outputState messages.TerminalOutputState) {
	markSessionDurationExpiry(r, planned, outputState)
}

func (r *terminalReporter) MarkReplayComplete() {
	r.markReplayComplete()
}

func (r *terminalReporter) RecordArtifactFinalization(requested bool, err error) {
	r.recordArtifactFinalization(requested, err)
}

func (r *terminalReporter) Publish(out io.Writer, runErr error) error {
	return r.publish(out, runErr)
}

func writeSessionReplayClose(out io.Writer, value *messages.SessionCloseValue, leadingNewline bool) error {
	if value == nil {
		return nil
	}
	if value.Reason != "" {
		prefix := ""
		if leadingNewline {
			prefix = "\n"
		}
		if _, err := fmt.Fprintf(out, "%s[session closed: %s]\n", prefix, value.Reason); err != nil {
			return err
		}
	}
	if fields := sessionTerminalFields(value.Classification, value.TerminalReason, value.TerminalProvenance, value.OutputState); fields != "" {
		_, err := fmt.Fprintf(out, "[session terminal: %s]\n", fields)
		return err
	}
	return nil
}

func sessionTerminalFields(classification string, reason messages.TerminalReason, provenance messages.TerminalProvenance, outputState messages.TerminalOutputState) string {
	fields := make([]string, 0, 4)
	if classification != "" {
		fields = append(fields, "classification="+classification)
	}
	if reason != "" {
		fields = append(fields, "terminal_reason="+string(reason))
	}
	if provenance != "" {
		fields = append(fields, "terminal_provenance="+string(provenance))
	}
	if outputState != "" {
		fields = append(fields, "output_state="+string(outputState))
	}
	return strings.Join(fields, " ")
}
