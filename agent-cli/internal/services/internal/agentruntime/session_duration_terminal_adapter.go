package agentruntime

import "io"

import (
	m "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
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
