package output

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

// LiveEventRenderer presents the typed session event stream for a terminal
// host. Session lifecycle and provider policy remain in the runtime; this
// renderer only owns the human readable projection.
type LiveEventRenderer struct {
	replay            bool
	visibleRole       messages.Role
	visibleStarted    bool
	transcriptRole    messages.Role
	transcriptStarted bool
	sessionClosed     bool
}

// NewLiveEventRenderer creates a renderer for a live or replay invocation.
func NewLiveEventRenderer(replay bool) *LiveEventRenderer {
	return &LiveEventRenderer{replay: replay}
}

// Render writes one typed runtime observation. It intentionally ignores
// provider-specific events that do not have a terminal or transcript
// presentation in the terminal host.
func (r *LiveEventRenderer) Render(_ context.Context, out io.Writer, event runtimeSession.LiveEvent) error {
	if out == nil {
		return errors.New("live output writer is nil")
	}
	switch event.Kind {
	case string(runtimeSession.LiveEventText):
		return r.renderVisibleText(out, event.Role, event.Text)
	case string(messages.StreamTypeTranscriptDelta):
		r.transcriptRole = event.Role
		if r.transcriptRole == "" {
			r.transcriptRole = messages.RoleAssistant
		}
		r.transcriptStarted = true
		return r.renderVisibleText(out, event.Role, event.Text)
	case string(messages.StreamTypeTranscriptEnd):
		return r.renderTranscriptEnd(out, event)
	case string(messages.StreamTypeTextEnd):
		return r.finishVisibleText(out)
	case string(messages.StreamTypeSessionClose):
		return r.renderSessionClose(out, event)
	case string(runtimeSession.LiveEventError):
		if event.Error == nil {
			return nil
		}
		_, err := fmt.Fprintf(out, "live session error: %v\n", event.Error)
		return err
	case string(runtimeSession.LiveEventTerminal):
		return r.renderTerminal(out, event)
	default:
		return nil
	}
}

func (r *LiveEventRenderer) renderTranscriptEnd(out io.Writer, event runtimeSession.LiveEvent) error {
	if !isRenderableTranscriptRole(event.Role) {
		return nil
	}
	endRole := event.Role
	if endRole == "" {
		endRole = messages.RoleAssistant
	}
	if r.transcriptStarted && r.transcriptRole == endRole {
		r.transcriptStarted = false
		r.transcriptRole = ""
		return r.finishVisibleText(out)
	}
	return r.renderVisibleText(out, event.Role, event.Text)
}

func (r *LiveEventRenderer) renderSessionClose(out io.Writer, event runtimeSession.LiveEvent) error {
	if err := r.finishVisibleText(out); err != nil {
		return err
	}
	if r.replay {
		// Replay completion belongs to the joined runtime result. Provider close
		// observations remain in the recording and may precede a replay error.
		if event.Reason != "" {
			_, err := fmt.Fprintf(out, "\n[session closed: %s]\n", event.Reason)
			return err
		}
		return nil
	}
	r.sessionClosed = true
	if event.Reason != "" {
		if _, err := fmt.Fprintf(out, "\n[session closed: %s]\n", event.Reason); err != nil {
			return err
		}
	}
	return writeLiveTerminal(out, event.Terminal, false)
}

func (r *LiveEventRenderer) renderTerminal(out io.Writer, event runtimeSession.LiveEvent) error {
	if r.sessionClosed {
		return nil
	}
	if err := r.finishVisibleText(out); err != nil {
		return err
	}
	if event.Error != nil {
		if err := writeLiveTerminal(out, event.Terminal, false); err != nil {
			return err
		}
		_, err := fmt.Fprintf(out, "live session terminated: %v\n", event.Error)
		return err
	}
	if r.replay && event.Terminal != nil && event.Terminal.TerminalReason == messages.TerminalReasonReplayComplete {
		if _, err := io.WriteString(out, "\n"); err != nil {
			return err
		}
	}
	return writeLiveTerminal(out, event.Terminal, r.replay)
}

func isRenderableTranscriptRole(role messages.Role) bool {
	return role == "" || role == messages.RoleAssistant || role == messages.RoleTool || role == messages.RoleUser
}

func (r *LiveEventRenderer) renderVisibleText(out io.Writer, role messages.Role, text string) error {
	if role == "" {
		role = messages.RoleAssistant
	}
	if r.visibleStarted && r.visibleRole != role {
		if _, err := io.WriteString(out, "\n"); err != nil {
			return err
		}
		r.visibleStarted = false
	}
	if !r.visibleStarted {
		label := liveRoleLabel(role)
		if _, err := io.WriteString(out, label+": "); err != nil {
			return err
		}
		r.visibleRole = role
		r.visibleStarted = true
	}
	_, err := io.WriteString(out, text)
	return err
}

func liveRoleLabel(role messages.Role) string {
	switch role {
	case messages.RoleUser:
		return "User"
	case messages.RoleTool:
		return "Tool result"
	case messages.RoleAssistant, messages.RoleSystem:
		return "Assistant"
	default:
		return "Assistant"
	}
}

func (r *LiveEventRenderer) finishVisibleText(out io.Writer) error {
	if !r.visibleStarted {
		return nil
	}
	if _, err := io.WriteString(out, "\n"); err != nil {
		return err
	}
	r.visibleStarted = false
	r.visibleRole = ""
	return nil
}

func writeLiveTerminal(out io.Writer, value *messages.SessionCloseValue, replayComplete bool) error {
	if value == nil {
		return nil
	}
	fields := liveTerminalFields(value)
	if fields != "" {
		if _, err := fmt.Fprintf(out, "[session terminal: %s]\n", fields); err != nil {
			return err
		}
	}
	if replayComplete && value.TerminalReason == messages.TerminalReasonReplayComplete {
		_, err := fmt.Fprintln(out, "[session replay complete]")
		return err
	}
	return nil
}

func liveTerminalFields(value *messages.SessionCloseValue) string {
	if value == nil {
		return ""
	}
	const terminalFieldCount = 4
	fields := make([]string, 0, terminalFieldCount)
	if value.Classification != "" {
		fields = append(fields, "classification="+value.Classification)
	}
	if value.TerminalReason != "" {
		fields = append(fields, "terminal_reason="+string(value.TerminalReason))
	}
	if value.TerminalProvenance != "" {
		fields = append(fields, "terminal_provenance="+string(value.TerminalProvenance))
	}
	if value.OutputState != "" {
		fields = append(fields, "output_state="+string(value.OutputState))
	}
	return strings.Join(fields, " ")
}
