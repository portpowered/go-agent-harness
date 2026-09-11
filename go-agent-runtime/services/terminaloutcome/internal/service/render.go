package service

import (
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const terminalFieldCapacity = 4

func writePublishedTerminal(out io.Writer, selected *candidate, replayDone bool) error {
	if selected == nil || selected.value == nil {
		return nil
	}
	value := selected.value
	if value.Reason == "" && selected.leadingNewline {
		if err := writeTerminalString(out, "\n"); err != nil {
			return err
		}
		if err := writeSessionClose(out, value, false); err != nil {
			return err
		}
	} else if err := writeSessionClose(out, value, selected.leadingNewline); err != nil {
		return err
	}
	if replayDone {
		return writeTerminalString(out, "[session replay complete]\n")
	}
	return nil
}

func writeSessionClose(out io.Writer, value *messages.SessionCloseValue, leadingNewline bool) error {
	if value == nil {
		return nil
	}
	if value.Reason != "" {
		prefix := ""
		if leadingNewline {
			prefix = "\n"
		}
		if err := writeTerminalString(out, prefix+"[session closed: "+value.Reason+"]\n"); err != nil {
			return err
		}
	}
	if fields := terminalFields(value.Classification, value.TerminalReason, value.TerminalProvenance, value.OutputState); fields != "" {
		return writeTerminalString(out, "[session terminal: "+fields+"]\n")
	}
	return nil
}

func terminalFields(classification string, reason messages.TerminalReason, provenance messages.TerminalProvenance, outputState messages.TerminalOutputState) string {
	fields := make([]string, 0, terminalFieldCapacity)
	if classification != "" {
		fields = append(fields, "classification="+boundTerminalText(classification))
	}
	if reason != "" {
		fields = append(fields, "terminal_reason="+boundTerminalText(string(reason)))
	}
	if provenance != "" {
		fields = append(fields, "terminal_provenance="+boundTerminalText(string(provenance)))
	}
	if outputState != "" {
		fields = append(fields, "output_state="+boundTerminalText(string(outputState)))
	}
	return strings.Join(fields, " ")
}

func writeTerminalString(out io.Writer, value string) error {
	if out == nil {
		return nil
	}
	n, err := io.WriteString(out, value)
	if err == nil && n != len(value) {
		return io.ErrShortWrite
	}
	return err
}
