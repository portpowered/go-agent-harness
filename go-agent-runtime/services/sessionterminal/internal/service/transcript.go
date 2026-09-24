// Package service owns buffered session transcript formatting.
package service

import (
	"fmt"
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
)

// transcriptRenderer keeps streamed transcript chunks on one labeled line
// until the provider closes that transcript. A role change closes the current
// line before starting the next one, so interleaved customer and assistant
// transcripts can never be rendered as one utterance.
type transcriptRenderer struct {
	out      io.Writer
	observer sessionterminal.TranscriptObserver

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

func (*Service) NewTranscriptRenderer(out io.Writer, observer sessionterminal.TranscriptObserver) sessionterminal.TranscriptRenderer {
	if out == nil {
		out = io.Discard
	}
	return &transcriptRenderer{
		out:              out,
		observer:         observer,
		transcriptStates: make(map[messages.Role]sessionReplayTranscriptState),
	}
}

func (r *transcriptRenderer) Write(data []byte) (int, error) {
	if err := r.Finish(); err != nil {
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

func (r *transcriptRenderer) WriteMessage(msg messages.StreamMessage) error {
	if r.observer != nil && msg.Type != messages.StreamTypeSessionClose && msg.Type != messages.StreamTypeError {
		r.observer(msg, false)
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
		return r.Finish()
	default:
		return r.writeOtherMessage(msg)
	}
}

func (r *transcriptRenderer) writeTranscriptStart(msg messages.StreamMessage) error {
	if err := r.Finish(); err != nil {
		return err
	}
	role := sessionReplayTranscriptRole(msg.Role, messages.RoleAssistant)
	r.pendingTranscriptRole = role
	r.transcriptStates[role] = sessionReplayTranscriptState{}
	return nil
}

func (r *transcriptRenderer) writeTranscriptDelta(msg messages.StreamMessage, value *messages.TranscriptDeltaValue) error {
	if value == nil || value.Text == "" || (!r.transcriptOpen && strings.TrimSpace(value.Text) == "") {
		return nil
	}
	role := r.transcriptRoleFor(msg.Role)
	if r.transcriptOpen && r.transcriptRole != role {
		if err := r.Finish(); err != nil {
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

func (r *transcriptRenderer) writeTextDelta(msg messages.StreamMessage, value *messages.TextDeltaValue) error {
	if value == nil || value.Content == "" || (!r.transcriptOpen && strings.TrimSpace(value.Content) == "") {
		return nil
	}
	role := sessionReplayTranscriptRole(msg.Role, messages.RoleAssistant)
	if r.transcriptOpen && r.transcriptRole != role {
		if err := r.Finish(); err != nil {
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

func (r *transcriptRenderer) writeToolCall(value *messages.ToolCallEndValue) error {
	if value == nil {
		return nil
	}
	if err := r.Finish(); err != nil {
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

func (r *transcriptRenderer) writeTranscriptEnd(msg messages.StreamMessage, value *messages.TranscriptEndValue) error {
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

func (r *transcriptRenderer) writeInterleavedTranscriptEnd(role messages.Role, state sessionReplayTranscriptState, value *messages.TranscriptEndValue) error {
	if state.deltaRendered || state.completed {
		return r.markTranscriptComplete(role, state)
	}
	if !usableTranscript(value) {
		return nil
	}
	return r.writeStandaloneTranscript(role, state, value)
}

func (r *transcriptRenderer) writeActiveTranscriptEnd(role messages.Role, state sessionReplayTranscriptState, value *messages.TranscriptEndValue) error {
	if !r.transcriptOpen && usableTranscript(value) {
		if err := r.startTranscript(role); err != nil {
			return err
		}
		if err := writeSessionReplayString(r.out, value.FullText); err != nil {
			return err
		}
	}
	if err := r.Finish(); err != nil {
		return err
	}
	state.completed = true
	r.transcriptStates[role] = state
	return nil
}

func (r *transcriptRenderer) writeStandaloneTranscript(role messages.Role, state sessionReplayTranscriptState, value *messages.TranscriptEndValue) error {
	if err := r.Finish(); err != nil {
		return err
	}
	if err := r.startTranscript(role); err != nil {
		return err
	}
	if err := writeSessionReplayString(r.out, value.FullText); err != nil {
		return err
	}
	if err := r.Finish(); err != nil {
		return err
	}
	return r.markTranscriptComplete(role, state)
}

func (r *transcriptRenderer) markTranscriptComplete(role messages.Role, state sessionReplayTranscriptState) error {
	state.completed = true
	r.transcriptStates[role] = state
	return nil
}

func usableTranscript(value *messages.TranscriptEndValue) bool {
	return value != nil && value.FullText != "" && strings.TrimSpace(value.FullText) != ""
}

func (r *transcriptRenderer) writeOtherMessage(msg messages.StreamMessage) error {
	if value, ok := msg.Value.(*messages.SessionCloseValue); ok {
		if err := r.Finish(); err != nil {
			return err
		}
		leadingNewline := !r.transcriptJustClosed
		r.transcriptJustClosed = false
		if r.observer != nil {
			r.observer(msg, leadingNewline)
			return nil
		}
		return writeTranscriptClose(r.out, value, leadingNewline)
	}
	if value, ok := msg.Value.(*messages.ErrorValue); ok {
		if !value.IsNonTerminal() {
			if err := r.Finish(); err != nil {
				return err
			}
		}
		if r.observer != nil {
			r.observer(msg, !r.transcriptJustClosed)
		}
		return writeTranscriptMessageUnscoped(r.out, msg)
	}
	return nil
}

func (r *transcriptRenderer) transcriptRoleFor(role messages.Role) messages.Role {
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

func writeTranscriptMessageUnscoped(out io.Writer, msg messages.StreamMessage) error {
	switch value := msg.Value.(type) {
	case *messages.TextDeltaValue:
		if value == nil {
			return nil
		}
		_, err := fmt.Fprint(out, value.Content)
		return err
	case *messages.TranscriptDeltaValue:
		if value == nil || value.Text == "" || strings.TrimSpace(value.Text) == "" {
			return nil
		}
		_, err := fmt.Fprintf(out, "%s: %s\n", sessionReplayTranscriptLabel(msg.Role), value.Text)
		return err
	case *messages.TranscriptEndValue:
		if !usableTranscript(value) {
			return nil
		}
		_, err := fmt.Fprintf(out, "%s: %s\n", sessionReplayTranscriptLabel(msg.Role), value.FullText)
		return err
	case *messages.SessionCloseValue:
		return writeTranscriptClose(out, value, true)
	case *messages.ErrorValue:
		return writeTranscriptError(out, value)
	}
	return nil
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

func (s *Service) WriteTranscriptMessage(out io.Writer, msg messages.StreamMessage) error {
	if writer, ok := out.(sessionterminal.TranscriptRenderer); ok {
		return writer.WriteMessage(msg)
	}
	if out == nil {
		out = io.Discard
	}
	return writeTranscriptMessageUnscoped(out, msg)
}

func (*Service) WriteSessionClose(out io.Writer, value *messages.SessionCloseValue, leadingNewline bool) error {
	if out == nil {
		out = io.Discard
	}
	return writeTranscriptClose(out, value, leadingNewline)
}

func (*Service) ErrorFields(value *messages.ErrorValue) string {
	return transcriptErrorFields(value)
}

func (r *transcriptRenderer) startTranscript(role messages.Role) error {
	if err := writeSessionReplayString(r.out, sessionReplayTranscriptLabel(role)+": "); err != nil {
		return err
	}
	r.transcriptRole = role
	r.pendingTranscriptRole = role
	r.transcriptOpen = true
	r.transcriptJustClosed = false
	return nil
}

func (r *transcriptRenderer) Finish() error {
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
