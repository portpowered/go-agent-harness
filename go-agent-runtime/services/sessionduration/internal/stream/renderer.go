// Package stream owns operator-facing rendering of one session stream and the
// shared stop, drain, and signal fan-in decisions of a session loop. Stream
// ownership and terminal publication ordering remain with the service.
package stream

import (
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

// roleState tracks the latest utterance for one role. A role's visible line
// can be closed by an interleaved role before its provider completion arrives,
// so the renderer retains that state after the line is no longer active.
type roleState struct {
	deltaRendered bool
	completed     bool
}

// Renderer keeps streamed transcript chunks on one labeled line until the
// provider closes that transcript. A role change closes the current line
// before starting the next one, so interleaved customer and assistant
// transcripts can never be rendered as one utterance.
type Renderer struct {
	out      io.Writer
	observer sessionduration.TranscriptObserver

	role        messages.Role
	pendingRole messages.Role
	open        bool
	justClosed  bool
	states      map[messages.Role]roleState
}

var _ sessionduration.Transcript = (*Renderer)(nil)

// New returns a renderer for out. The observer, when present, receives the
// terminal evidence for every rendered message.
func New(out io.Writer, observer sessionduration.TranscriptObserver) *Renderer {
	return &Renderer{out: out, observer: observer, states: make(map[messages.Role]roleState)}
}

// Write closes any open transcript line before forwarding raw output.
func (r *Renderer) Write(data []byte) (int, error) {
	if err := r.Finish(); err != nil {
		return 0, err
	}
	n, err := r.out.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		r.justClosed = false
	}
	return n, err
}

// WriteMessage renders one stream message.
func (r *Renderer) WriteMessage(msg messages.StreamMessage) error {
	if r.observer != nil && msg.Type != messages.StreamTypeSessionClose && msg.Type != messages.StreamTypeError {
		r.observer.ObserveStreamMessage(msg, false)
	}
	switch value := msg.Value.(type) {
	case *messages.TranscriptStartValue:
		return r.transcriptStart(msg.Role)
	case *messages.TranscriptDeltaValue:
		return r.transcriptDelta(msg.Role, value)
	case *messages.TextDeltaValue:
		return r.textDelta(msg.Role, value)
	case *messages.ToolCallEndValue:
		return r.toolCall(value)
	case *messages.TranscriptEndValue:
		return r.transcriptEnd(msg.Role, value)
	case *messages.TextEndValue, *messages.MessageEndValue:
		// These are explicit visible-utterance boundaries. Audio and other
		// non-rendered events may be interleaved between text or transcript
		// deltas and must not fragment the terminal line.
		return r.Finish()
	case *messages.SessionCloseValue:
		return r.sessionClose(msg, value)
	case *messages.ErrorValue:
		return r.errorMessage(msg, value)
	default:
		// All remaining messages are invisible to this renderer. Their
		// arrival does not mean the visible speaker changed.
		return nil
	}
}

func (r *Renderer) transcriptStart(role messages.Role) error {
	if err := r.Finish(); err != nil {
		return err
	}
	role = transcriptRole(role, messages.RoleAssistant)
	r.pendingRole = role
	// TRANSCRIPT.START is an explicit new utterance boundary. Reset only this
	// role; another role may still have a completion in flight.
	r.states[role] = roleState{}
	return nil
}

func (r *Renderer) transcriptDelta(msgRole messages.Role, value *messages.TranscriptDeltaValue) error {
	if value == nil || value.Text == "" || (!r.open && strings.TrimSpace(value.Text) == "") {
		return nil
	}
	role := r.roleFor(msgRole)
	if err := r.openLine(role, true); err != nil {
		return err
	}
	if err := writeString(r.out, value.Text); err != nil {
		return err
	}
	r.states[role] = roleState{deltaRendered: true}
	return nil
}

func (r *Renderer) textDelta(msgRole messages.Role, value *messages.TextDeltaValue) error {
	if value == nil || value.Content == "" || (!r.open && strings.TrimSpace(value.Content) == "") {
		return nil
	}
	if err := r.openLine(transcriptRole(msgRole, messages.RoleAssistant), false); err != nil {
		return err
	}
	return writeString(r.out, value.Content)
}

// openLine ensures role owns the visible line. A transcript delta after a
// completed utterance starts the next one; when the prior line was only closed
// by an interleaved role, its pending completion state is retained.
func (r *Renderer) openLine(role messages.Role, transcript bool) error {
	if r.open && r.role != role {
		if err := r.Finish(); err != nil {
			return err
		}
	}
	if r.open {
		return nil
	}
	if transcript {
		if state := r.states[role]; state.completed || !state.deltaRendered {
			r.states[role] = roleState{}
		}
	}
	return r.startLine(role)
}

func (r *Renderer) toolCall(value *messages.ToolCallEndValue) error {
	if value == nil {
		return nil
	}
	if err := r.Finish(); err != nil {
		return err
	}
	if err := writeString(r.out, toolCallLine(value)); err != nil {
		return err
	}
	r.justClosed = false
	return nil
}

func (r *Renderer) transcriptEnd(msgRole messages.Role, value *messages.TranscriptEndValue) error {
	role := r.roleFor(msgRole)
	state := r.states[role]
	// A role can change before the provider delivers the previous role's
	// completion. The delta already rendered that line, so its completion must
	// not close or replace the currently active role's line.
	if r.open && r.role != role {
		return r.inactiveTranscriptEnd(role, state, value)
	}
	if !r.open && (state.deltaRendered || state.completed) {
		// The role's delta line was already rendered and may have been closed
		// by another role. Its completion is bookkeeping, not a second line.
		state.completed = true
		r.states[role] = state
		return nil
	}
	// Some providers send only the completed event. Render that final value
	// once; rendered deltas are deliberately not duplicated.
	if !r.open && hasText(value) {
		if err := r.startLine(role); err != nil {
			return err
		}
		if err := writeString(r.out, value.FullText); err != nil {
			return err
		}
	}
	if err := r.Finish(); err != nil {
		return err
	}
	state.completed = true
	r.states[role] = state
	return nil
}

func (r *Renderer) inactiveTranscriptEnd(role messages.Role, state roleState, value *messages.TranscriptEndValue) error {
	if state.deltaRendered || state.completed {
		if state.deltaRendered {
			state.completed = true
			r.states[role] = state
		}
		return nil
	}
	// A provider may complete an inactive role without deltas. Preserve the
	// active line, then render this completion as its own line exactly once.
	if !hasText(value) {
		return nil
	}
	if err := r.Finish(); err != nil {
		return err
	}
	if err := r.startLine(role); err != nil {
		return err
	}
	if err := writeString(r.out, value.FullText); err != nil {
		return err
	}
	if err := r.Finish(); err != nil {
		return err
	}
	state.completed = true
	r.states[role] = state
	return nil
}

func (r *Renderer) sessionClose(msg messages.StreamMessage, value *messages.SessionCloseValue) error {
	if err := r.Finish(); err != nil {
		return err
	}
	leadingNewline := !r.justClosed
	r.justClosed = false
	if r.observer != nil {
		r.observer.ObserveStreamMessage(msg, leadingNewline)
		return nil
	}
	return writeClose(r.out, value, leadingNewline)
}

func (r *Renderer) errorMessage(msg messages.StreamMessage, value *messages.ErrorValue) error {
	// A non-terminal provider notice is invisible and cannot create a visible
	// actor boundary. A terminal error ends the current line before it is
	// returned to the session loop.
	if !value.IsNonTerminal() {
		if err := r.Finish(); err != nil {
			return err
		}
	}
	if r.observer != nil {
		r.observer.ObserveStreamMessage(msg, !r.justClosed)
	}
	return WriteUnscoped(r.out, msg)
}

func (r *Renderer) roleFor(role messages.Role) messages.Role {
	if role != "" {
		return transcriptRole(role, messages.RoleAssistant)
	}
	if r.open && r.role != "" {
		return r.role
	}
	if r.pendingRole != "" {
		return r.pendingRole
	}
	return messages.RoleAssistant
}

func (r *Renderer) startLine(role messages.Role) error {
	if err := writeString(r.out, transcriptLabel(role)+": "); err != nil {
		return err
	}
	r.role = role
	r.pendingRole = role
	r.open = true
	r.justClosed = false
	return nil
}

// Finish closes the currently open transcript line, if any.
func (r *Renderer) Finish() error {
	if !r.open {
		return nil
	}
	err := writeString(r.out, "\n")
	if err == nil {
		r.role = ""
		r.pendingRole = ""
		r.open = false
		r.justClosed = true
	}
	return err
}

func hasText(value *messages.TranscriptEndValue) bool {
	return value != nil && strings.TrimSpace(value.FullText) != ""
}

func transcriptRole(role, fallback messages.Role) messages.Role {
	if role != "" {
		return role
	}
	return fallback
}

func transcriptLabel(role messages.Role) string {
	if role == messages.RoleUser {
		return "User"
	}
	if role == messages.RoleTool {
		return "Tool result"
	}
	return "Assistant"
}
