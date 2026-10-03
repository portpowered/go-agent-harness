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
	// LiveEventDelegationToolCall and LiveEventDelegationToolResult record a
	// session tool call made by a client delegation's backend loop (not the
	// voice loop). ItemID is the delegation id, ToolCallID and Text the call
	// id and tool name; a result carries Error when the call failed.
	LiveEventDelegationToolCall   LiveEventKind = "delegation_tool_call"
	LiveEventDelegationToolResult LiveEventKind = "delegation_tool_result"
)
