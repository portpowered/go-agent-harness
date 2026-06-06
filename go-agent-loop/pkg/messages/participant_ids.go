package messages

// ParticipantID identifies a participant in the agentic loop.
type ParticipantID string

const (
	System ParticipantID = "system"
	User   ParticipantID = "user"
	Tool   ParticipantID = "tool"
	Model  ParticipantID = "model"
	Kernel ParticipantID = "kernel"
)
