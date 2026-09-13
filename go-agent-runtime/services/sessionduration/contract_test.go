package sessionduration

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestContractTypesRemainHostNeutral(t *testing.T) {
	var _ MessageWriter = func(messages.StreamMessage) error { return nil }
	var _ ArtifactWriter = artifactProbe{}
	var _ State = stateProbe{}
	var _ Service = serviceProbe{}
}

type artifactProbe struct{}

func (artifactProbe) Accept(messages.StreamMessage) error { return nil }

type stateProbe struct{}

func (stateProbe) Observe(messages.StreamMessage)            {}
func (stateProbe) OutputState() messages.TerminalOutputState { return messages.TerminalOutputNone }
func (stateProbe) Admit(bool, messages.StreamMessage) (messages.StreamMessage, bool) {
	return messages.StreamMessage{}, false
}
func (stateProbe) PublishProviderTerminal(Publication) error { return nil }
func (stateProbe) PublishMaxDuration(Publication, messages.TerminalOutputState) error {
	return nil
}
func (stateProbe) Written() bool { return false }

type serviceProbe struct{}

func (serviceProbe) NewState(TerminalSource) State { return stateProbe{} }
func (serviceProbe) PublishMaxDuration(Publication, messages.TerminalOutputState) error {
	return nil
}
func (serviceProbe) LifecycleError(LifecycleFailures) error { return nil }
func (serviceProbe) TransportError(error) error             { return nil }
