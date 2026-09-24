package stream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	gwproviders "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

// Write renders msg through out's stateful transcript when out is one, and
// otherwise through the unscoped single-message format.
func Write(out io.Writer, msg messages.StreamMessage) error {
	if transcript, ok := out.(sessionduration.Transcript); ok {
		return transcript.WriteMessage(msg)
	}
	return WriteUnscoped(out, msg)
}

// WriteUnscoped renders one message for callers that provide a plain writer.
// A full session uses Renderer so a stream of deltas receives one label rather
// than one label per chunk.
func WriteUnscoped(out io.Writer, msg messages.StreamMessage) error {
	switch value := msg.Value.(type) {
	case *messages.TextDeltaValue:
		_, err := fmt.Fprint(out, value.Content)
		return err
	case *messages.TranscriptDeltaValue:
		if value == nil {
			return nil
		}
		return writeLabeled(out, msg.Role, value.Text)
	case *messages.TranscriptEndValue:
		if value == nil {
			return nil
		}
		return writeLabeled(out, msg.Role, value.FullText)
	case *messages.SessionCloseValue:
		return writeClose(out, value, true)
	case *messages.ErrorValue:
		if value.IsNonTerminal() {
			return nil
		}
		return errorMessageError(value)
	default:
		return nil
	}
}

func writeLabeled(out io.Writer, role messages.Role, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	_, err := fmt.Fprintf(out, "%s: %s\n", transcriptLabel(role), text)
	return err
}

func errorMessageError(value *messages.ErrorValue) error {
	fields := ErrorFields(value)
	message := "session error"
	if value.Message != "" {
		message = "session error: " + value.Message
	}
	if fields != "" {
		message += " [" + fields + "]"
	}
	if value.Err == nil {
		return errors.New(message)
	}
	return fmt.Errorf("%s: %w", message, value.Err)
}

func writeClose(out io.Writer, value *messages.SessionCloseValue, leadingNewline bool) error {
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
	if fields := terminalFields(value.Classification, value.TerminalReason, value.TerminalProvenance, value.OutputState); fields != "" {
		_, err := fmt.Fprintf(out, "[session terminal: %s]\n", fields)
		return err
	}
	return nil
}

// ErrorFields formats the bounded classification and provider identity of a
// provider error for operator-facing text.
func ErrorFields(value *messages.ErrorValue) string {
	if value == nil {
		return ""
	}
	classification := value.Classification
	if classification == "" && (value.ErrorType != "" || value.Code != "" || value.Message != "") {
		classification = gwproviders.SessionErrorClassification(value.ErrorType, value.Code, value.Message)
	}
	fields := terminalFields(classification, value.TerminalReason, value.TerminalProvenance, value.OutputState)
	var providerFields []string
	if fields != "" {
		providerFields = append(providerFields, fields)
	}
	if value.ErrorType != "" {
		providerFields = append(providerFields, "error_type="+value.ErrorType)
	}
	if value.Code != "" {
		providerFields = append(providerFields, "code="+value.Code)
	}
	return strings.Join(providerFields, " ")
}

func terminalFields(classification string, reason messages.TerminalReason, provenance messages.TerminalProvenance, outputState messages.TerminalOutputState) string {
	var fields []string
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

func toolCallLine(value *messages.ToolCallEndValue) string {
	name := strings.TrimSpace(value.Name)
	if name == "" {
		name = "unknown"
	}
	arguments := compactToolText(value.Arguments)
	if arguments == "" {
		arguments = "{}"
	}
	return fmt.Sprintf("Tool call: %s %s\n", name, arguments)
}

func compactToolText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var compact bytes.Buffer
	if json.Compact(&compact, []byte(value)) == nil {
		return compact.String()
	}
	return strings.Join(strings.Fields(value), " ")
}

func writeString(out io.Writer, data string) error {
	n, err := out.Write([]byte(data))
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}
	return err
}

type streamTerminalError struct {
	cause error
	text  string
}

func (e *streamTerminalError) Error() string { return e.text }
func (e *streamTerminalError) Unwrap() error { return e.cause }

// TerminationError preserves a caller cancellation observed after the loop has
// already reported its terminal result, and exposes a structured provider
// classification carried by a stream delta error at the host boundary.
func TerminationError(ctx context.Context, err error) error {
	err = decorateStreamTerminalError(err)
	if ctx == nil {
		return err
	}
	return errors.Join(err, ctx.Err())
}

func decorateStreamTerminalError(err error) error {
	if err == nil {
		return nil
	}
	var deltaErr *engine.StreamDeltaError
	if !errors.As(err, &deltaErr) || deltaErr.Value == nil {
		return err
	}
	fields := ErrorFields(deltaErr.Value)
	if fields == "" || strings.Contains(err.Error(), "classification=") {
		return err
	}
	message := strings.TrimSpace(deltaErr.Value.Message)
	if message == "" {
		message = "session error"
	}
	return &streamTerminalError{cause: err, text: fmt.Sprintf("%s [%s]", message, fields)}
}
