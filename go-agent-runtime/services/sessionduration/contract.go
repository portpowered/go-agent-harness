// Package sessionduration defines the host-neutral terminal boundary for a
// bounded session. Provider observations, output projection, publication
// ports, and lifecycle error facts are explicit; host resources stay outside
// the contract.
package sessionduration

import "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"

// TerminalSource supplies the provider observation facts needed to distinguish
// a provider-authored close from a loop shutdown request.
type TerminalSource struct {
	Message func() (messages.StreamMessage, bool)
	Matches func(messages.StreamMessage) bool
}

// MessageWriter is an injected publication port. The service never opens or
// owns the underlying stream or artifact resource.
type MessageWriter func(messages.StreamMessage) error

// ArtifactWriter is the host-neutral artifact admission port.
type ArtifactWriter interface {
	Accept(messages.StreamMessage) error
}

// Publication describes the ordered artifact and output effects for one
// terminal message.
type Publication struct {
	Artifacts ArtifactWriter
	Write     MessageWriter
}

// State owns one bounded-session observation and terminal-admission lifecycle.
type State interface {
	Observe(messages.StreamMessage)
	OutputState() messages.TerminalOutputState
	Admit(bool, messages.StreamMessage) (messages.StreamMessage, bool)
	PublishProviderTerminal(Publication) error
	Written() bool
}

// LifecycleFailures contains independent shutdown causes. The service joins
// each non-nil cause while retaining errors.Is/As identity.
type LifecycleFailures struct {
	Runtime error
	Close   error
	Binding error
}

// Service owns terminal precedence, output projection, publication ordering,
// terminal synthesis, and normalized error composition.
type Service interface {
	NewState(TerminalSource) State
	PublishMaxDuration(Publication, messages.TerminalOutputState) error
	LifecycleError(LifecycleFailures) error
	TransportError(error) error
}
