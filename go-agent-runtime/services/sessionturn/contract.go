// Package sessionturn owns the persistent session turn boundary: turn state,
// initial text-seed and image input, instruction delivery, tool execution
// policy, and dynamic tool publication. Hosts supply already-resolved
// dependencies and presentation ports; the decisions stay behind this
// contract and its private implementation.
package sessionturn

// Error is the stable identity type for session-turn sentinel errors. Values
// are constants so their identity cannot be reassigned at runtime.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrTurnAlreadyActive rejects a turn start while another turn is open.
	ErrTurnAlreadyActive Error = "turn start while another turn is active"
	// ErrTurnEndWithoutStart rejects a turn end with no active turn.
	ErrTurnEndWithoutStart Error = "turn end without start: no active turn"
	// ErrEmptyTurn rejects blank turn input or an empty response.
	ErrEmptyTurn Error = "turn content must not be empty"
	// ErrInvalidTurnDirection rejects an unknown turn direction.
	ErrInvalidTurnDirection Error = "turn direction is invalid"
	// ErrInvalidTurnTick rejects a tick that does not strictly increase.
	ErrInvalidTurnTick Error = "turn tick must be strictly increasing"
	// ErrSessionEndedWithActiveTurn rejects closing a session mid-turn.
	ErrSessionEndedWithActiveTurn Error = "session ended with active turn"
	// ErrSessionClosed rejects work on a closed turn session.
	ErrSessionClosed Error = "session is closed"
	// ErrTurnMismatch rejects ending a turn other than the active one.
	ErrTurnMismatch Error = "turn does not match the active turn"
	// ErrMissingTurnInferencer reports a turn run without a provider.
	ErrMissingTurnInferencer Error = "session turn inferencer is not configured"
	// ErrSessionResponse reports a provider error value without detail.
	ErrSessionResponse Error = "session returned an error"

	// ErrToolTimeout is retained behind a correlated tool result so callers
	// can classify a local deadline without parsing the response content.
	ErrToolTimeout Error = "tool execution timed out"
	// ErrPublication identifies a failed dynamic tool refresh or delivery.
	ErrPublication Error = "session dynamic tool publication failed"
	// ErrMissingInferencer reports an input wrapper without a provider seam.
	ErrMissingInferencer Error = "session image runtime has no session inferencer"

	// ErrImageMissingFile identifies a missing session image path.
	ErrImageMissingFile Error = "session image file is missing"
	// ErrImageUnreadableFile identifies an unreadable session image path.
	ErrImageUnreadableFile Error = "session image file is unreadable"
	// ErrImageUnsupportedMIME identifies content outside the model's MIME set.
	ErrImageUnsupportedMIME Error = "session image MIME type is unsupported"
	// ErrImageInvalidContent identifies bytes that do not decode as an image.
	ErrImageInvalidContent Error = "session image content is invalid"
	// ErrImageEmptyFile identifies a zero-byte session image.
	ErrImageEmptyFile Error = "session image file is empty"
	// ErrImageCapability identifies a provider or model without image input.
	ErrImageCapability Error = "session image capability is unsupported"
	// ErrImageSend identifies a provider session that rejected an image turn.
	ErrImageSend Error = "session image turn could not be sent"
)

// Service is the complete session-turn capability. Its implementation is
// private and constructed through services/sessionturn/wire.
type Service interface {
	TurnService
	ToolService
	PublicationService
	InputService
}
