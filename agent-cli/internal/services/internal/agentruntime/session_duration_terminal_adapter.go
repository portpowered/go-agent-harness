package agentruntime

import (
	"io"

	m "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	d "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	w "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/wire"
)

// Deprecated: use the runtime sessionduration service instead.
type sessionDurationTerminalState struct {
	state d.State
}

func newSessionDurationTerminalState(i *sessionDurationAdmissionInferencer) *sessionDurationTerminalState {
	return &sessionDurationTerminalState{state: w.NewService().NewState(d.TerminalSource{Message: i.providerTerminalMessage, Matches: i.isProviderTerminalMessage})}
}

func (i *sessionDurationAdmissionInferencer) providerTerminalMessage() (m.StreamMessage, bool) {
	if i == nil {
		return m.StreamMessage{}, false
	}
	i.mu.Lock()
	session := i.session
	i.mu.Unlock()
	if session == nil {
		return m.StreamMessage{}, false
	}
	return session.providerTerminalMessage()
}

func (i *sessionDurationAdmissionInferencer) isProviderTerminalMessage(msg m.StreamMessage) bool {
	if i == nil {
		return false
	}
	i.mu.Lock()
	session := i.session
	i.mu.Unlock()
	return session != nil && session.isProviderTerminalMessage(msg)
}

func (s *sessionDurationAdmissionSession) providerTerminalMessage() (m.StreamMessage, bool) {
	s.terminalMu.Lock()
	defer s.terminalMu.Unlock()
	if !s.providerTerminalSeen {
		return m.StreamMessage{}, false
	}
	msg := s.providerTerminal
	if value, ok := msg.Value.(*m.SessionCloseValue); ok {
		clone := *value
		msg.Value = &clone
	}
	return msg, true
}

func (s *sessionDurationAdmissionSession) isProviderTerminalMessage(msg m.StreamMessage) bool {
	value, ok := msg.Value.(*m.SessionCloseValue)
	if !ok {
		return false
	}
	s.terminalMu.Lock()
	defer s.terminalMu.Unlock()
	return s.providerTerminalSeen && value == s.providerTerminalValue
}

func (s *sessionDurationTerminalState) observe(msg m.StreamMessage) { s.state.Observe(msg) }
func (s *sessionDurationTerminalState) outputState() m.TerminalOutputState {
	return s.state.OutputState()
}
func (s *sessionDurationTerminalState) written() bool { return s.state.Written() }
func (s *sessionDurationTerminalState) admitTerminal(planned bool, msg m.StreamMessage) (m.StreamMessage, bool) {
	return s.state.Admit(planned, msg)
}
func (s *sessionDurationTerminalState) writeObservedProviderTerminal(out io.Writer, artifacts SessionDurationArtifactLifecycle) error {
	return s.state.PublishProviderTerminal(durationPublication(out, artifacts))
}
func (s *sessionDurationTerminalState) writeMaxDurationTerminal(out io.Writer, artifacts SessionDurationArtifactLifecycle, outputState m.TerminalOutputState) error {
	return s.state.PublishMaxDuration(durationPublication(out, artifacts), outputState)
}
func durationPublication(out io.Writer, artifacts SessionDurationArtifactLifecycle) d.Publication {
	return d.Publication{Artifacts: artifacts, Write: func(msg m.StreamMessage) error { return writeSessionReplayMessage(out, msg) }}
}
func sessionDurationLifecycleError(runtimeErr, closeErr, bindingErr error) error {
	return w.NewService().LifecycleError(d.LifecycleFailures{Runtime: runtimeErr, Close: closeErr, Binding: bindingErr})
}
func sessionTransportError(err error) error { return w.NewService().TransportError(err) }

type sessionDurationTerminalRecorder = d.TerminalRecorder

func recordingTerminalSummaryFromMessage(msg m.StreamMessage) (*transcript.RecordingTerminalSummary, bool, error) {
	return w.NewService().RecordingTerminalSummaryFromMessage(msg)
}
