package messages

import "context"

// SessionSendStatus identifies the observable outcome of sending a message to
// a persistent session.
type SessionSendStatus string

const (
	SessionSendSucceeded       SessionSendStatus = "succeeded"
	SessionSendCancelled       SessionSendStatus = "cancelled"
	SessionSendTimedOut        SessionSendStatus = "timed_out"
	SessionSendBufferFull      SessionSendStatus = "buffer_full"
	SessionSendClosed          SessionSendStatus = "closed"
	SessionSendTerminalFailure SessionSendStatus = "terminal_failure"
)

// SessionSendOutcome is the typed result returned by session send helpers.
type SessionSendOutcome struct {
	Status SessionSendStatus
	Err    error
}

// OK reports whether the message was accepted for delivery.
func (o SessionSendOutcome) OK() bool {
	return o.Status == SessionSendSucceeded
}

// SessionSendOutcomeSender is implemented by sessions that can report why a
// send did not succeed. It is intentionally separate from Session so existing
// bool-only Session implementations continue to compile.
type SessionSendOutcomeSender interface {
	SendWithOutcome(ctx context.Context, msg StreamMessage) SessionSendOutcome
}

// SessionResponseRequester is an optional session capability for requesting a
// response without adding another user message or committing an input buffer.
// It is used for audio-only tool continuations; sessions that do not expose
// this capability, such as legacy replay fixtures, retain their existing wire
// traffic.
type SessionResponseRequester interface {
	RequestResponse(ctx context.Context) SessionSendOutcome
}

// SessionResponseCapability reports whether an optional response request can
// reach the underlying provider. Decorators implement this recursively so a
// legacy replay session is not mistaken for a live-capable session merely
// because an outer wrapper has the forwarding method.
type SessionResponseCapability interface {
	SupportsResponseRequests() bool
}

// SendSessionWithOutcome sends msg and returns a typed outcome. Sessions that
// implement SessionSendOutcomeSender provide authoritative closed, buffer-full,
// and terminal-failure states. Bool-only sessions are adapted for compatibility:
// context cancellation and timeout remain distinguishable, while other false
// results are reported as terminal failures because their precise cause is not
// observable through the legacy contract.
func SendSessionWithOutcome(ctx context.Context, session Session, msg StreamMessage) SessionSendOutcome {
	if sender, ok := session.(SessionSendOutcomeSender); ok {
		return sender.SendWithOutcome(ctx, msg)
	}
	if session.Send(ctx, msg) {
		return SessionSendOutcome{Status: SessionSendSucceeded}
	}
	return sessionSendContextOrFailure(ctx)
}

// RequestSessionResponse asks a session that supports the optional response
// request capability to start the next response. Unsupported sessions return
// a terminal failure without sending a new stream message.
func RequestSessionResponse(ctx context.Context, session Session) SessionSendOutcome {
	if !SupportsSessionResponseRequests(session) {
		return SessionSendOutcome{Status: SessionSendTerminalFailure}
	}
	requester, ok := session.(SessionResponseRequester)
	if !ok {
		return SessionSendOutcome{Status: SessionSendTerminalFailure}
	}
	return requester.RequestResponse(ctx)
}

// SupportsSessionResponseRequests reports whether session exposes a response
// request capability and, for decorated sessions, whether it reaches a live
// provider rather than a replay fixture.
func SupportsSessionResponseRequests(session Session) bool {
	if capability, ok := session.(SessionResponseCapability); ok {
		return capability.SupportsResponseRequests()
	}
	_, ok := session.(SessionResponseRequester)
	return ok
}

func sessionSendContextOrFailure(ctx context.Context) SessionSendOutcome {
	err := ctx.Err()
	if err == context.DeadlineExceeded {
		return SessionSendOutcome{Status: SessionSendTimedOut, Err: err}
	}
	if err != nil {
		return SessionSendOutcome{Status: SessionSendCancelled, Err: err}
	}
	return SessionSendOutcome{Status: SessionSendTerminalFailure}
}

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
	// Returns false if the context is cancelled, the outbound buffer is full, the
	// session is closed, or delivery fails. Call SendSessionWithOutcome when the
	// precise public lifecycle outcome is required.
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
