package session

// LiveEventKind identifies the small set of lifecycle observations that a
// live owner may publish. Providers may add namespaced values for provider
// details; these values are stable across the room and CLI adapters.
type LiveEventKind string

const (
	LiveEventStarted  LiveEventKind = "started"
	LiveEventText     LiveEventKind = "text"
	LiveEventError    LiveEventKind = "error"
	LiveEventOverflow LiveEventKind = "overflow"
	LiveEventLiveness LiveEventKind = "liveness_fault"
	LiveEventTerminal LiveEventKind = "terminal"
)
