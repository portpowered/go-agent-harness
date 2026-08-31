// This file contains session output draining, close dispatch, stop decisions, terminal formatting, and shutdown error handling.
package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
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
		if err := r.finishTranscript(); err != nil {
			return err
		}
		role := sessionReplayTranscriptRole(msg.Role, messages.RoleAssistant)
		r.pendingTranscriptRole = role
		// TRANSCRIPT.START is an explicit new utterance boundary. Reset only
		// this role; another role may still have a completion in flight.
		r.transcriptStates[role] = sessionReplayTranscriptState{}
		return nil
	case *messages.TranscriptDeltaValue:
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
				// A delta after a completed transcript starts the next
				// utterance. If the prior line was only closed by an
				// interleaved role, retain its pending completion state.
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
		state.deltaRendered = true
		state.completed = false
		r.transcriptStates[role] = state
		return nil
	case *messages.TranscriptEndValue:
		role := r.transcriptRoleFor(msg.Role)
		state := r.transcriptStates[role]
		// A role can change before the provider delivers the previous role's
		// completion. The delta already rendered that previous line, so its
		// completion must not close or replace the currently active role's line.
		if r.transcriptOpen && r.transcriptRole != role {
			if state.deltaRendered || state.completed {
				if state.deltaRendered {
					state.completed = true
					r.transcriptStates[role] = state
				}
				return nil
			}
			// A provider may complete an inactive role without sending any
			// deltas. Preserve the active line, then render this completion as
			// its own line exactly once when it contains usable text.
			if value == nil || value.FullText == "" || strings.TrimSpace(value.FullText) == "" {
				return nil
			}
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
			state.completed = true
			r.transcriptStates[role] = state
			return nil
		}
		if !r.transcriptOpen && (state.deltaRendered || state.completed) {
			// The role's delta line was already rendered and may have been
			// closed by another role. Its completion is bookkeeping, not a
			// second visible transcript line.
			state.completed = true
			r.transcriptStates[role] = state
			return nil
		}
		// Some providers can send only the completed event. Render that final
		// value once; when deltas were already shown, the completed value is
		// deliberately not appended because it would duplicate the utterance.
		if !r.transcriptOpen && value != nil && value.FullText != "" && strings.TrimSpace(value.FullText) != "" {
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
	default:
		if err := r.finishTranscript(); err != nil {
			return err
		}
		if value, ok := msg.Value.(*messages.SessionCloseValue); ok {
			leadingNewline := !r.transcriptJustClosed
			r.transcriptJustClosed = false
			if r.terminalReporter != nil {
				r.terminalReporter.observeStreamMessage(msg, leadingNewline)
				return nil
			}
			return writeSessionReplayClose(r.out, value, leadingNewline)
		}
		if r.terminalReporter != nil {
			r.terminalReporter.observeStreamMessage(msg, !r.transcriptJustClosed)
		}
		err := writeSessionReplayMessageUnscoped(r.out, msg)
		if err == nil {
			if value, ok := msg.Value.(*messages.TextDeltaValue); ok && value != nil && value.Content != "" {
				r.transcriptJustClosed = false
			}
		}
		return err
	}
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

func sessionReplayTranscriptRole(role, fallback messages.Role) messages.Role {
	if role == messages.RoleUser {
		return messages.RoleUser
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
	return "Assistant"
}

func writeSessionReplayBytes(out io.Writer, data []byte) error {
	n, err := out.Write(data)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
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

// writeSessionReplayMessageUnscoped retains the direct helper's behavior for
// callers that provide a plain writer. A full session uses the renderer above
// so a stream of deltas receives one label rather than one label per chunk.
func writeSessionReplayMessageUnscoped(out io.Writer, msg messages.StreamMessage) error {
	switch v := msg.Value.(type) {
	case *messages.TextDeltaValue:
		_, err := fmt.Fprint(out, v.Content)
		return err
	case *messages.TranscriptDeltaValue:
		if v == nil || v.Text == "" || strings.TrimSpace(v.Text) == "" {
			return nil
		}
		_, err := fmt.Fprintf(out, "%s: %s\n", sessionReplayTranscriptLabel(msg.Role), v.Text)
		return err
	case *messages.TranscriptEndValue:
		if v == nil || v.FullText == "" || strings.TrimSpace(v.FullText) == "" {
			return nil
		}
		_, err := fmt.Fprintf(out, "%s: %s\n", sessionReplayTranscriptLabel(msg.Role), v.FullText)
		return err
	case *messages.SessionCloseValue:
		return writeSessionReplayClose(out, v, true)
	case *messages.ErrorValue:
		if v.IsNonTerminal() {
			return nil
		}
		fields := sessionTerminalFields(v.Classification, v.TerminalReason, v.TerminalProvenance, v.OutputState)
		wrapCause := func(message string) error {
			if v.Err == nil {
				return errors.New(message)
			}
			return fmt.Errorf("%s: %w", message, v.Err)
		}
		if v.Message != "" {
			if fields != "" {
				return wrapCause(fmt.Sprintf("session error: %s [%s]", v.Message, fields))
			}
			return wrapCause(fmt.Sprintf("session error: %s", v.Message))
		}
		if fields != "" {
			return wrapCause(fmt.Sprintf("session error [%s]", fields))
		}
		return wrapCause("session error")
	}
	return nil
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

func isTerminalErrorMessage(msg messages.StreamMessage) bool {
	if msg.Type != messages.StreamTypeError {
		return false
	}
	value, ok := msg.Value.(*messages.ErrorValue)
	return !ok || value.IsTerminal()
}

func sessionTerminalFields(classification string, reason messages.TerminalReason, provenance messages.TerminalProvenance, outputState messages.TerminalOutputState) string {
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

// waitForSessionLoopStragglers waits for provider deltas until the supplied
// quiet period elapses. The terminal boundary is the only caller; terminal
// code must not silently replace this bounded wait with a buffered-only flush.
func waitForSessionLoopStragglers(out io.Writer, loop *agentloop.AgentLoop, quiet time.Duration, obs *sessionProgressObserver) error {
	if quiet <= 0 {
		return flushBufferedSessionLoopMessages(out, loop, obs)
	}

	idle := time.NewTimer(quiet)
	defer idle.Stop()
	for {
		select {
		case msg := <-loop.Deltas().Chan():
			if obs != nil {
				obs.observe(msg)
			}
			if err := writeSessionReplayMessage(out, msg); err != nil {
				return err
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(quiet)
		case <-idle.C:
			return nil
		}
	}
}

func shouldStopSessionLoop(msg messages.StreamMessage, opts sessionLoopOptions, closeSent bool) bool {
	if msg.Type == messages.StreamTypeMessageEnd && opts.observer != nil {
		if opts.observer.hasTerminalToolContinuationFailure() || opts.observer.hasTerminalScheduledResponseFailure() {
			return true
		}
	}
	if opts.CloseAfterOpen {
		return closeSent && msg.Type == messages.StreamTypeSessionClose
	}
	if opts.WaitForClose {
		return msg.Type == messages.StreamTypeSessionClose
	}
	switch msg.Type {
	case messages.StreamTypeMessageEnd:
		if opts.observer != nil && !opts.observer.lastMessageEndAdmitted() {
			return false
		}
		if opts.observer != nil && opts.observer.hasToolLifecycleObligation() {
			return false
		}
		if opts.CloseAfterScheduledAudio && opts.observer != nil && !opts.observer.scheduledAudioComplete() {
			return false
		}
		return true
	case messages.StreamTypeTextEnd:
		if opts.observer != nil && opts.observer.hasToolLifecycleObligation() {
			return false
		}
		return true
	case messages.StreamTypeSessionClose:
		return true
	default:
		return false
	}
}

// flushBufferedSessionLoopMessages renders only messages already buffered.
// It never waits for a future provider message; the terminal boundary invokes
// it only after owned resources have been stopped.
func flushBufferedSessionLoopMessages(out io.Writer, loop *agentloop.AgentLoop, obs *sessionProgressObserver) error {
	for {
		msg, ok := loop.Deltas().Read()
		if !ok {
			return nil
		}
		if obs != nil {
			obs.observe(msg)
		}
		if err := writeSessionReplayMessage(out, msg); err != nil {
			return err
		}
	}
}

func sendSessionClose(ctx context.Context, loop *agentloop.AgentLoop) error {
	msg := messages.Message{
		Role: messages.RoleUser,
		ContentParts: []messages.ContentPart{
			messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypeSessionClose},
		},
	}
	if err := loop.Send(ctx, []messages.Message{msg}); err != nil {
		return fmt.Errorf("close session loop: %w", err)
	}
	return nil
}

func wrapSessionPhaseError(phase string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", phase, err)
}
