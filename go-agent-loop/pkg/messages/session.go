package messages

import "context"

// SessionInferencer establishes persistent, bidirectional inference sessions.
// It is the sessional counterpart to the agent loop's Inferencer interface:
// where Inferencer handles stateless request/response inference, SessionInferencer
// handles long-running sessions (e.g. WebSocket-based realtime audio).
//
// Declared here so that the agent loop owns its dependency contracts.
// Implementations (e.g. go-llm-gateway) bake configuration into their constructor
// and expose a no-arg ConnectSession for the agent loop to call.
type SessionInferencer interface {
	// ConnectSession establishes a new session and returns a bidirectional
	// Session. Configuration (model, voice, instructions) is provided at
	// construction time, not per-call.
	ConnectSession(ctx context.Context) (Session, error)
}

// Session represents a persistent bidirectional inference connection.
// Implementations maintain the transport (e.g. WebSocket) and handle
// protocol translation internally. The agent loop communicates exclusively
// via typed StreamMessage buffers.
//
// Declared here so that go-llm-gateway (and other implementors) depend on
// go-agent-loop's contracts rather than the reverse.
type Session interface {
	// Send writes a StreamMessage to the session's outbound queue.
	// Returns false if the context is cancelled or the outbound buffer is full.
	Send(ctx context.Context, msg StreamMessage) bool
	// Receive returns the inbound typed buffer. Callers read from this buffer
	// to consume events from the session (e.g. AUDIO.DELTA, SESSION.OPEN).
	Receive() *TypedBuffer[StreamMessage]
	// Done returns a channel closed when the session terminates, either by
	// client close or server disconnection.
	Done() <-chan struct{}
	// Close terminates the session and releases resources. Safe to call multiple times.
	Close() error
}
