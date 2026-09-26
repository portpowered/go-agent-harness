// Package architecturesessionwrapperindirect wraps a session through an
// interface declared in another package and never imports messages itself.
package architecturesessionwrapperindirect

import "sessionwrapstub"

type indirect struct { // want "session wrapper indirect does not implement messages.BargeInCapableSession"
	sessionwrapstub.OrderedSession
}

var _ = indirect{}
