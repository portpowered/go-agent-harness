package architecturesessionwrapper

import m "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"

type PS = m.Session

// forwarding wraps a session and embeds the shared forwarder.
type forwarding struct {
	m.Session
	m.SessionCapabilities
}

// nested embeds a struct that embeds the shared forwarder.
type nested struct {
	forwarding
	inner m.Session
}

// hiding wraps a session through an alias and drops the capabilities.
type hiding struct { // want "session wrapper hiding does not embed messages.SessionCapabilities"
	PS
}

// copying forwards a capability by hand instead of embedding the forwarder.
type copying struct { // want "session wrapper copying does not embed messages.SessionCapabilities"
	m.Session
}

func (s *copying) ProviderTurnDetection() bool { return false }

// named holds the forwarder in a named field, so nothing is promoted.
type named struct { // want "session wrapper named does not embed messages.SessionCapabilities"
	inner        m.Session
	capabilities m.SessionCapabilities
}

func (s *named) Send(msg string) bool { return s.inner.Send(msg) }
func (s *named) Close() error         { return s.inner.Close() }

// leaf is a provider session: it wraps nothing.
type leaf struct{}

func (leaf) Send(string) bool { return true }
func (leaf) Close() error     { return nil }

// pointerHolder holds a concrete wrapper by pointer.
type pointerHolder struct { // want "session wrapper pointerHolder does not embed messages.SessionCapabilities"
	inner *forwarding
}

func (s *pointerHolder) Send(msg string) bool { return s.inner.Send(msg) }
func (s *pointerHolder) Close() error         { return s.inner.Close() }

// resolving reaches the wrapped session through a function.
type resolving struct { // want "session wrapper resolving does not embed messages.SessionCapabilities"
	session func() m.Session
}

func (s *resolving) Send(msg string) bool { return s.session().Send(msg) }
func (s *resolving) Close() error         { return s.session().Close() }

// fanOut relays to several sessions.
type fanOut struct { // want "session wrapper fanOut does not embed messages.SessionCapabilities"
	sessions []m.Session
}

func (s *fanOut) Send(msg string) bool { return s.sessions[0].Send(msg) }
func (s *fanOut) Close() error         { return nil }

// fanOutByID relays to sessions keyed by participant.
type fanOutByID struct { // want "session wrapper fanOutByID does not embed messages.SessionCapabilities"
	sessions map[string]*forwarding
}

func (s *fanOutByID) Send(string) bool { return true }
func (s *fanOutByID) Close() error     { return nil }

// fanOutForwarding relays to several sessions and embeds the forwarder for
// its primary session.
type fanOutForwarding struct {
	m.SessionCapabilities
	sessions []m.Session
}

func (s *fanOutForwarding) Send(string) bool { return true }
func (s *fanOutForwarding) Close() error     { return nil }

var (
	_ = pointerHolder{}
	_ = resolving{}
	_ = forwarding{}
	_ = nested{}
	_ = hiding{}
	_ = copying{}
	_ = named{}
	_ = leaf{}
	_ = fanOut{}
	_ = fanOutByID{}
	_ = fanOutForwarding{}
)
