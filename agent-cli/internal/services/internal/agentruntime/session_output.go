// This file contains session output draining, close dispatch, stop decisions, terminal formatting, and shutdown error handling.
package agentruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimereplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
)

// sessionReplayMessageWriter is implemented by the stateful terminal renderer
// used by a complete session run. Keeping the interface private preserves the
// small writeSessionReplayMessage seam used by cancellation and unit tests.
type sessionReplayMessageWriter interface {
	writeSessionReplayMessage(messages.StreamMessage) error
}

// sessionReplayRenderer keeps streamed transcript chunks on one labeled line
// until the provider closes that transcript. A role change closes the current
// line before starting the next one, so interleaved customer and assistant
// transcripts can never be rendered as one utterance.
type sessionReplayRenderer struct {
	out              io.Writer
	terminalReporter *sessionTerminalReporter

	transcriptRole        messages.Role
	pendingTranscriptRole messages.Role
	transcriptOpen        bool
	transcriptJustClosed  bool
	transcriptStates      map[messages.Role]sessionReplayTranscriptState
}

// sessionReplayTranscriptState tracks the lifecycle of the latest transcript
// utterance for a role. A role's visible line can be closed by an interleaved
// role before its provider completion arrives, so the renderer must retain
// that state after the line is no longer active.
type sessionReplayTranscriptState struct {
	deltaRendered bool
	completed     bool
}

const unknownSessionToolName = "unknown"

func newSessionReplayRenderer(out io.Writer, reporter ...*sessionTerminalReporter) *sessionReplayRenderer {
	var terminalReporter *sessionTerminalReporter
	if len(reporter) > 0 {
		terminalReporter = reporter[0]
	}
	return &sessionReplayRenderer{
		out:              out,
		terminalReporter: terminalReporter,
		transcriptStates: make(map[messages.Role]sessionReplayTranscriptState),
	}
}

func runReplayCapture(ctx context.Context, out io.Writer, service runtimereplay.Service, path string) error {
	replay, err := service.Replay(ctx, path)
	if err != nil {
		return err
	}
	defer func() { _ = replay.Close() }() //nolint:errcheck // bounded best-effort close
	renderer := newSessionReplayRenderer(out, sessionTerminalReporterFromContext(ctx))
	return replay.Drain(ctx, func(msg messages.StreamMessage) error {
		return writeSessionReplayMessage(renderer, msg)
	})
}

func (r *sessionReplayRenderer) Write(data []byte) (int, error) {
	if err := r.finishTranscript(); err != nil {
		return 0, err
	}
	n, err := r.out.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		r.transcriptJustClosed = false
	}
	return n, err
}

func (r *sessionReplayRenderer) writeSessionReplayMessage(msg messages.StreamMessage) error {
	if r.terminalReporter != nil && msg.Type != messages.StreamTypeSessionClose && msg.Type != messages.StreamTypeError {
		r.terminalReporter.observeStreamMessage(msg, false)
	}
	switch value := msg.Value.(type) {
	case *messages.TranscriptStartValue:
		return r.writeTranscriptStart(msg)
	case *messages.TranscriptDeltaValue:
		return r.writeTranscriptDelta(msg, value)
	case *messages.TextDeltaValue:
		return r.writeTextDelta(msg, value)
	case *messages.ToolCallEndValue:
		return r.writeToolCall(value)
	case *messages.TranscriptEndValue:
		return r.writeTranscriptEnd(msg, value)
	case *messages.TextEndValue, *messages.MessageEndValue:
		return r.finishTranscript()
	default:
		return r.writeOtherMessage(msg)
	}
}

func (r *sessionReplayRenderer) writeTranscriptStart(msg messages.StreamMessage) error {
	if err := r.finishTranscript(); err != nil {
		return err
	}
	role := sessionReplayTranscriptRole(msg.Role, messages.RoleAssistant)
	r.pendingTranscriptRole = role
	r.transcriptStates[role] = sessionReplayTranscriptState{}
	return nil
}

func (r *sessionReplayRenderer) writeTranscriptDelta(msg messages.StreamMessage, value *messages.TranscriptDeltaValue) error {
	if value == nil || value.Text == "" || (!r.transcriptOpen && strings.TrimSpace(value.Text) == "") {
		return nil
	}
	role := r.transcriptRoleFor(msg.Role)
	if r.transcriptOpen && r.transcriptRole != role {
		if err := r.finishTranscript(); err != nil {
			return err
		}
	}
	if !r.transcriptOpen {
		state := r.transcriptStates[role]
		if state.completed || !state.deltaRendered {
			r.transcriptStates[role] = sessionReplayTranscriptState{}
		}
		if err := r.startTranscript(role); err != nil {
			return err
		}
	}
	if err := writeSessionReplayString(r.out, value.Text); err != nil {
		return err
	}
	state := r.transcriptStates[role]
	state.deltaRendered, state.completed = true, false
	r.transcriptStates[role] = state
	return nil
}

func (r *sessionReplayRenderer) writeTextDelta(msg messages.StreamMessage, value *messages.TextDeltaValue) error {
	if value == nil || value.Content == "" || (!r.transcriptOpen && strings.TrimSpace(value.Content) == "") {
		return nil
	}
	role := sessionReplayTranscriptRole(msg.Role, messages.RoleAssistant)
	if r.transcriptOpen && r.transcriptRole != role {
		if err := r.finishTranscript(); err != nil {
			return err
		}
	}
	if !r.transcriptOpen {
		if err := r.startTranscript(role); err != nil {
			return err
		}
	}
	return writeSessionReplayString(r.out, value.Content)
}

func (r *sessionReplayRenderer) writeToolCall(value *messages.ToolCallEndValue) error {
	if value == nil {
		return nil
	}
	if err := r.finishTranscript(); err != nil {
		return err
	}
	name := strings.TrimSpace(value.Name)
	if name == "" {
		name = unknownSessionToolName
	}
	arguments := compactSessionToolText(value.Arguments)
	if arguments == "" {
		arguments = "{}"
	}
	if err := writeSessionReplayString(r.out, fmt.Sprintf("Tool call: %s %s\n", name, arguments)); err != nil {
		return err
	}
	r.transcriptJustClosed = false
	return nil
}

func (r *sessionReplayRenderer) writeTranscriptEnd(msg messages.StreamMessage, value *messages.TranscriptEndValue) error {
	role := r.transcriptRoleFor(msg.Role)
	state := r.transcriptStates[role]
	if r.transcriptOpen && r.transcriptRole != role {
		return r.writeInterleavedTranscriptEnd(role, state, value)
	}
	if !r.transcriptOpen && (state.deltaRendered || state.completed) {
		return r.markTranscriptComplete(role, state)
	}
	return r.writeActiveTranscriptEnd(role, state, value)
}

func (r *sessionReplayRenderer) writeInterleavedTranscriptEnd(role messages.Role, state sessionReplayTranscriptState, value *messages.TranscriptEndValue) error {
	if state.deltaRendered || state.completed {
		return r.markTranscriptComplete(role, state)
	}
	if !usableTranscript(value) {
		return nil
	}
	return r.writeStandaloneTranscript(role, state, value)
}

func (r *sessionReplayRenderer) writeActiveTranscriptEnd(role messages.Role, state sessionReplayTranscriptState, value *messages.TranscriptEndValue) error {
	if !r.transcriptOpen && usableTranscript(value) {
		if err := r.startTranscript(role); err != nil {
			return err
		}
		if err := writeSessionReplayString(r.out, value.FullText); err != nil {
			return err
		}
	}
	if err := r.finishTranscript(); err != nil {
		return err
	}
	state.completed = true
	r.transcriptStates[role] = state
	return nil
}

func (r *sessionReplayRenderer) writeStandaloneTranscript(role messages.Role, state sessionReplayTranscriptState, value *messages.TranscriptEndValue) error {
	if err := r.finishTranscript(); err != nil {
		return err
	}
	if err := r.startTranscript(role); err != nil {
		return err
	}
	if err := writeSessionReplayString(r.out, value.FullText); err != nil {
		return err
	}
	if err := r.finishTranscript(); err != nil {
		return err
	}
	return r.markTranscriptComplete(role, state)
}

func (r *sessionReplayRenderer) markTranscriptComplete(role messages.Role, state sessionReplayTranscriptState) error {
	state.completed = true
	r.transcriptStates[role] = state
	return nil
}

func usableTranscript(value *messages.TranscriptEndValue) bool {
	return value != nil && value.FullText != "" && strings.TrimSpace(value.FullText) != ""
}

func (r *sessionReplayRenderer) writeOtherMessage(msg messages.StreamMessage) error {
	if value, ok := msg.Value.(*messages.SessionCloseValue); ok {
		if err := r.finishTranscript(); err != nil {
			return err
		}
		leadingNewline := !r.transcriptJustClosed
		r.transcriptJustClosed = false
		if r.terminalReporter != nil {
			r.terminalReporter.observeStreamMessage(msg, leadingNewline)
			return nil
		}
		return writeSessionReplayClose(r.out, value, leadingNewline)
	}
	if value, ok := msg.Value.(*messages.ErrorValue); ok {
		if !value.IsNonTerminal() {
			if err := r.finishTranscript(); err != nil {
				return err
			}
		}
		if r.terminalReporter != nil {
			r.terminalReporter.observeStreamMessage(msg, !r.transcriptJustClosed)
		}
		return writeSessionReplayMessageUnscoped(r.out, msg)
	}
	return nil
}

func (r *sessionReplayRenderer) transcriptRoleFor(role messages.Role) messages.Role {
	if role != "" {
		return sessionReplayTranscriptRole(role, messages.RoleAssistant)
	}
	if r.transcriptOpen && r.transcriptRole != "" {
		return r.transcriptRole
	}
	if r.pendingTranscriptRole != "" {
		return r.pendingTranscriptRole
	}
	return messages.RoleAssistant
}

func sessionReplayTranscriptRole(role, fallback messages.Role) messages.Role {
	if role == messages.RoleUser || role == messages.RoleTool {
		return role
	}
	if role != "" {
		return role
	}
	return fallback
}

func sessionReplayTranscriptLabel(role messages.Role) string {
	if role == messages.RoleUser {
		return "User"
	}
	if role == messages.RoleTool {
		return "Tool result"
	}
	return "Assistant"
}

func compactSessionToolText(value string) string {
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

func writeSessionReplayBytes(out io.Writer, data []byte) error {
	n, err := out.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return err
}

func writeSessionReplayString(out io.Writer, data string) error {
	return writeSessionReplayBytes(out, []byte(data))
}

func writeSessionReplayMessage(out io.Writer, msg messages.StreamMessage) error {
	if writer, ok := out.(sessionReplayMessageWriter); ok {
		return writer.writeSessionReplayMessage(msg)
	}
	return writeSessionReplayMessageUnscoped(out, msg)
}

func (r *sessionReplayRenderer) startTranscript(role messages.Role) error {
	if err := writeSessionReplayString(r.out, sessionReplayTranscriptLabel(role)+": "); err != nil {
		return err
	}
	r.transcriptRole = role
	r.pendingTranscriptRole = role
	r.transcriptOpen = true
	r.transcriptJustClosed = false
	return nil
}

func (r *sessionReplayRenderer) finishTranscript() error {
	if !r.transcriptOpen {
		return nil
	}
	err := writeSessionReplayString(r.out, "\n")
	if err == nil {
		r.transcriptRole = ""
		r.pendingTranscriptRole = ""
		r.transcriptOpen = false
		r.transcriptJustClosed = true
	}
	return err
}

// waitForLoopStragglers waits for provider deltas until the required
// positive policy quiet period elapses. The terminal boundary is the only
