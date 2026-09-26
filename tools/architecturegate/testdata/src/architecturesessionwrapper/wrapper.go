package architecturesessionwrapper

import m "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"

type PS = m.Session

// forwarding wraps a session and forwards the barge-in capabilities.
type forwarding struct {
	m.Session
	m.SessionCapabilities
}

// hiding wraps a session through an alias and drops them.
type hiding struct { // want "session wrapper hiding does not implement messages.BargeInCapableSession"
	PS
}

// named holds the wrapped session in a named field and drops them.
type named struct { // want "session wrapper named does not implement messages.BargeInCapableSession"
	inner m.Session
}

func (s *named) Send(msg string) bool { return s.inner.Send(msg) }
func (s *named) Close() error         { return s.inner.Close() }

// leaf is a provider session: it wraps nothing.
type leaf struct{}

func (leaf) Send(string) bool { return true }
func (leaf) Close() error     { return nil }

var (
	_ = forwarding{}
	_ = hiding{}
	_ = named{}
	_ = leaf{}
)
