package messages

// Session lifecycle, configuration, and session-control stream values and their constructors live in this file.

// PongValue is the value for PONG, emitted in response to a ping control plane message.
type PongValue struct {
	Type      string `json:"type"`      // "pong"
	Timestamp int64  `json:"timestamp"` // Unix milliseconds at response time
}

func (*PongValue) streamMessageValue() {}

// NewPongValue returns a value for PONG with the current timestamp.
func NewPongValue(timestamp int64) *PongValue {
	return &PongValue{Type: "pong", Timestamp: timestamp}
}

// SessionOpenValue is the value for SESSION.OPEN, emitted as the first event in a session.
type SessionOpenValue struct {
	Type      string `json:"type"`       // "session_open"
	SessionID string `json:"session_id"` // unique session identifier
	Mode      string `json:"mode"`       // e.g. "audio_inference"
}

func (*SessionOpenValue) streamMessageValue() {}

// NewSessionOpenValue returns a value for SESSION.OPEN.
func NewSessionOpenValue(sessionID, mode string) *SessionOpenValue {
	return &SessionOpenValue{Type: "session_open", SessionID: sessionID, Mode: mode}
}

// SessionCloseValue is the value for SESSION.CLOSE, emitted before LOOP.END.
type SessionCloseValue struct {
	Type               string              `json:"type"`       // "session_close"
	SessionID          string              `json:"session_id"` // unique session identifier
	Reason             string              `json:"reason"`     // e.g. "client_close", "error", "timeout"
	Classification     string              `json:"classification,omitempty"`
	TerminalReason     TerminalReason      `json:"terminal_reason,omitempty"`
	TerminalProvenance TerminalProvenance  `json:"terminal_provenance,omitempty"`
	OutputState        TerminalOutputState `json:"output_state,omitempty"`
}

func (*SessionCloseValue) streamMessageValue() {}

// NewSessionCloseValue returns a value for SESSION.CLOSE.
func NewSessionCloseValue(sessionID, reason string) *SessionCloseValue {
	return &SessionCloseValue{Type: "session_close", SessionID: sessionID, Reason: reason}
}

// NewSessionCloseValueWithTerminal returns a SESSION.CLOSE value with additive
// terminal metadata while preserving the legacy Reason field.
func NewSessionCloseValueWithTerminal(sessionID, reason, classification string, terminalReason TerminalReason, provenance TerminalProvenance, outputState TerminalOutputState) *SessionCloseValue {
	return &SessionCloseValue{
		Type:               "session_close",
		SessionID:          sessionID,
		Reason:             reason,
		Classification:     classification,
		TerminalReason:     terminalReason,
		TerminalProvenance: provenance,
		OutputState:        outputState,
	}
}

// SessionCreatedValue is the value for SESSION.CREATED, emitted when the server
// confirms a session has been established. It carries the session configuration
// returned by the inference provider.
type SessionCreatedValue struct {
	Type      string `json:"type"`       // "session_created"
	SessionID string `json:"session_id"` // unique session identifier from the server
	Model     string `json:"model"`      // model in use for the session
}

func (*SessionCreatedValue) streamMessageValue() {}

// NewSessionCreatedValue returns a value for SESSION.CREATED.
func NewSessionCreatedValue(sessionID, model string) *SessionCreatedValue {
	return &SessionCreatedValue{Type: "session_created", SessionID: sessionID, Model: model}
}

// SessionUpdatedValue is the value for SESSION.UPDATED, emitted when the server
// confirms a session configuration update (in response to SESSION.UPDATE).
type SessionUpdatedValue struct {
	Type      string `json:"type"`       // "session_updated"
	SessionID string `json:"session_id"` // unique session identifier
}

func (*SessionUpdatedValue) streamMessageValue() {}

// NewSessionUpdatedValue returns a value for SESSION.UPDATED.
func NewSessionUpdatedValue(sessionID string) *SessionUpdatedValue {
	return &SessionUpdatedValue{Type: "session_updated", SessionID: sessionID}
}

// SessionUpdateConfig holds the parameters sent in a SESSION.UPDATE message.
// Set in AgentLoopConfig via WithSessionConfig; the model runner sends this
// automatically after SESSION.CREATED.
type SessionUpdateConfig struct {
	Instructions string           // system prompt / instructions for the session
	Model        string           // model name (e.g. "grok-3")
	Modalities   []string         // input/output modalities (e.g. ["audio", "text"])
	Tools        []ToolDefinition // tool definitions advertised by the session
}

// SessionUpdateValue is the value for SESSION.UPDATE (outbound), used to send
// a session configuration update to the inference provider.
type SessionUpdateValue struct {
	Type         string           `json:"type"`                   // "session_update"
	Instructions string           `json:"instructions,omitempty"` // system prompt
	Model        string           `json:"model,omitempty"`        // model name
	Modalities   []string         `json:"modalities,omitempty"`   // input/output modalities
	Tools        []ToolDefinition `json:"tools,omitempty"`        // advertised tool definitions
}

func (*SessionUpdateValue) streamMessageValue() {}

// NewSessionUpdateValue returns a SESSION.UPDATE value from a SessionUpdateConfig.
func NewSessionUpdateValue(cfg *SessionUpdateConfig) *SessionUpdateValue {
	tools := append([]ToolDefinition(nil), cfg.Tools...)
	for i := range tools {
		tools[i].Parameters = append([]ToolParameter(nil), cfg.Tools[i].Parameters...)
	}
	return &SessionUpdateValue{
		Type:         "session_update",
		Instructions: cfg.Instructions,
		Model:        cfg.Model,
		Modalities:   cfg.Modalities,
		Tools:        tools,
	}
}

// ResponseCancelValue is the value for RESPONSE.CANCEL (outbound), sent to the
// inference provider via session.Send to cancel an in-progress response (barge-in).
type ResponseCancelValue struct {
	Type string `json:"type"` // "response_cancel"
}

func (*ResponseCancelValue) streamMessageValue() {}

// NewResponseCancelValue returns a value for RESPONSE.CANCEL.
func NewResponseCancelValue() *ResponseCancelValue {
	return &ResponseCancelValue{Type: "response_cancel"}
}
