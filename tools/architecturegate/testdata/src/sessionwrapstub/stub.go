// Package sessionwrapstub declares a session interface of its own, like
// sessionwrap.OrderedSession.
package sessionwrapstub

import m "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"

type OrderedSession interface {
	m.Session
	Ordered() bool
}
