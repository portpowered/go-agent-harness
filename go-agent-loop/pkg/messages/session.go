package messages

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

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

// SessionOutboundFlusher is an optional session capability for callers that
// must wait until every already-admitted outbound event has completed its
// transport write. Session.Send admission normally means queue admission; a
// realtime capture boundary may need the stronger wire-settled guarantee.
type SessionOutboundFlusher interface {
	FlushOutbound(ctx context.Context) error
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
type SessionInferencer interface { //nolint:iface // Exported contract implemented and consumed outside this package; nothing here asserts against it.
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

// SessionCapabilities forwards every optional session capability to the
// session it wraps. Every type that wraps a Session embeds it and overrides
// only the capabilities it changes, so a new capability is added here once
// and every wrapper relays it.
//
// Go cannot add or remove methods at run time, so a wrapper always has every
// forwarding method. A capability whose absence differs from its zero answer
// is therefore discovered through a query (SupportsResponseRequests,
// SupportsCompleteMessages, SupportsCompleteMessagesWithoutResponse,
// SupportsRTCMedia) that each forwarder answers from the wrapped session, and
// callers discover it through the matching helper (SupportsSessionResponseRequests,
// SupportsSessionMessages, SupportsSessionMessagesWithoutResponse,
// SessionMedia). Every other forwarded capability answers its zero value when
// the wrapped session lacks it, which is the answer a session without it gives:
// no provider turn detection, no local playback, an unknown input rate, no
// receive relay, no outbound queue, no terminal error and no drops.
//
// A wrapper that answers a queried capability itself, instead of relaying it,
// overrides the query as well (a wrapper rendering its own media overrides
// both RTCMedia and SupportsRTCMedia).
type SessionCapabilities struct {
	Wrapped Session
}

// SessionMessageSender is implemented by sessions that accept a complete
// message (for example a tool result carrying an image) and then request the
// next response.
type SessionMessageSender interface {
	SendMessage(context.Context, Message) bool
}

// SessionMessageWithoutResponseSender is implemented by sessions that accept a
// complete message without requesting a response.
type SessionMessageWithoutResponseSender interface {
	SendMessageWithoutResponse(context.Context, Message) bool
}

// SessionMessageCapability reports whether the complete-message methods reach
// a session that supports them.
type SessionMessageCapability interface {
	SupportsCompleteMessages() bool
	SupportsCompleteMessagesWithoutResponse() bool
}

// SessionTerminalError is implemented by sessions that know why they ended.
type SessionTerminalError interface {
	TerminalError() error
}

// SessionInitialConfigMarker is implemented by sessions whose connection
// already sent the initial session configuration.
type SessionInitialConfigMarker interface {
	InitialSessionConfigSent() bool
}

// SessionMediaCapability reports whether a session's RTCMedia method reaches
// media a caller may consume.
type SessionMediaCapability interface {
	SupportsRTCMedia() bool
}

var (
	_ BargeInCapableSession = struct {
		Session
		SessionCapabilities
	}{}
	_ SessionResponseRequester            = SessionCapabilities{}
	_ SessionResponseCapability           = SessionCapabilities{}
	_ SessionMessageSender                = SessionCapabilities{}
	_ SessionMessageWithoutResponseSender = SessionCapabilities{}
	_ SessionMessageCapability            = SessionCapabilities{}
	_ SessionOutboundFlusher              = SessionCapabilities{}
	_ SessionTerminalError                = SessionCapabilities{}
	_ SessionDropCounters                 = SessionCapabilities{}
	_ SessionInitialConfigMarker          = SessionCapabilities{}
	_ audio.ConfigurableMediaSession      = SessionCapabilities{}
	_ SessionMediaCapability              = SessionCapabilities{}
)

func (c SessionCapabilities) ProviderTurnDetection() bool {
	detector, ok := c.Wrapped.(SessionTurnDetection)
	return ok && detector.ProviderTurnDetection()
}

func (c SessionCapabilities) LocalPlayback() LocalPlaybackState {
	if playback, ok := c.Wrapped.(SessionLocalPlayback); ok {
		return playback.LocalPlayback()
	}
	return LocalPlaybackState{}
}

func (c SessionCapabilities) InterruptLocalPlayback(ctx context.Context) bool {
	playback, ok := c.Wrapped.(SessionLocalPlayback)
	return ok && playback.InterruptLocalPlayback(ctx)
}

func (c SessionCapabilities) InputAudioSampleRate() int {
	if format, ok := c.Wrapped.(SessionInputFormat); ok {
		return format.InputAudioSampleRate()
	}
	return 0
}

// SyncReceive waits for the relays below this wrapper. A wrapper with its own
// relay calls it before waiting for that relay.
func (c SessionCapabilities) SyncReceive(ctx context.Context) {
	if syncer, ok := c.Wrapped.(SessionReceiveSyncer); ok {
		syncer.SyncReceive(ctx)
	}
}

func (c SessionCapabilities) RequestResponse(ctx context.Context) SessionSendOutcome {
	return RequestSessionResponse(ctx, c.Wrapped)
}

func (c SessionCapabilities) SupportsResponseRequests() bool {
	return SupportsSessionResponseRequests(c.Wrapped)
}

func (c SessionCapabilities) SendMessage(ctx context.Context, msg Message) bool {
	return SendSessionMessage(ctx, c.Wrapped, msg)
}

func (c SessionCapabilities) SendMessageWithoutResponse(ctx context.Context, msg Message) bool {
	return SendSessionMessageWithoutResponse(ctx, c.Wrapped, msg)
}

func (c SessionCapabilities) SupportsCompleteMessages() bool {
	return SupportsSessionMessages(c.Wrapped)
}

func (c SessionCapabilities) SupportsCompleteMessagesWithoutResponse() bool {
	return SupportsSessionMessagesWithoutResponse(c.Wrapped)
}

func (c SessionCapabilities) FlushOutbound(ctx context.Context) error {
	if flusher, ok := c.Wrapped.(SessionOutboundFlusher); ok {
		return flusher.FlushOutbound(ctx)
	}
	return nil
}

func (c SessionCapabilities) TerminalError() error {
	if terminal, ok := c.Wrapped.(SessionTerminalError); ok {
		return terminal.TerminalError()
	}
	return nil
}

func (c SessionCapabilities) InputDrops() int64 {
	if counters, ok := c.Wrapped.(SessionDropCounters); ok {
		return counters.InputDrops()
	}
	return 0
}

func (c SessionCapabilities) OutputDrops() int64 {
	if counters, ok := c.Wrapped.(SessionDropCounters); ok {
		return counters.OutputDrops()
	}
	return 0
}

func (c SessionCapabilities) InitialSessionConfigSent() bool {
	marker, ok := c.Wrapped.(SessionInitialConfigMarker)
	return ok && marker.InitialSessionConfigSent()
}

func (c SessionCapabilities) RTCMedia() audio.MediaEndpoints {
	endpoints, _ := SessionMedia(c.Wrapped)
	return endpoints
}

func (c SessionCapabilities) RTCMediaWithOptions(options audio.MediaSessionOptions) audio.MediaEndpoints {
	endpoints, _ := SessionMediaWithOptions(c.Wrapped, options)
	return endpoints
}

func (c SessionCapabilities) SupportsRTCMedia() bool {
	return SupportsSessionMedia(c.Wrapped)
}

// SupportsSessionMessages reports whether session delivers complete messages
// that request a response.
func SupportsSessionMessages(session Session) bool {
	if capability, ok := session.(SessionMessageCapability); ok {
		return capability.SupportsCompleteMessages()
	}
	_, ok := session.(SessionMessageSender)
	return ok
}

// SupportsSessionMessagesWithoutResponse reports whether session delivers
// complete messages without requesting a response.
func SupportsSessionMessagesWithoutResponse(session Session) bool {
	if capability, ok := session.(SessionMessageCapability); ok {
		return capability.SupportsCompleteMessagesWithoutResponse()
	}
	_, ok := session.(SessionMessageWithoutResponseSender)
	return ok
}

// SendSessionMessage sends a complete message and requests a response. It
// reports false when session does not support complete messages.
func SendSessionMessage(ctx context.Context, session Session, msg Message) bool {
	sender, ok := session.(SessionMessageSender)
	return ok && SupportsSessionMessages(session) && sender.SendMessage(ctx, msg)
}

// SendSessionMessageWithoutResponse sends a complete message without
// requesting a response. It reports false when session does not support it.
func SendSessionMessageWithoutResponse(ctx context.Context, session Session, msg Message) bool {
	sender, ok := session.(SessionMessageWithoutResponseSender)
	return ok && SupportsSessionMessagesWithoutResponse(session) && sender.SendMessageWithoutResponse(ctx, msg)
}

// SupportsSessionMedia reports whether session exposes media a caller may
// consume, without opening it.
func SupportsSessionMedia(session Session) bool {
	if capability, ok := session.(SessionMediaCapability); ok {
		return capability.SupportsRTCMedia()
	}
	_, ok := session.(audio.MediaSession)
	return ok
}

// SessionMedia returns the media session exposes and whether it exposes any.
func SessionMedia(session Session) (audio.MediaEndpoints, bool) {
	owner, ok := session.(audio.MediaSession)
	if !ok || !SupportsSessionMedia(session) {
		return audio.MediaEndpoints{}, false
	}
	return owner.RTCMedia(), true
}

// SessionMediaWithOptions returns the media session exposes, configured with
// options when session can configure it.
func SessionMediaWithOptions(session Session, options audio.MediaSessionOptions) (audio.MediaEndpoints, bool) {
	configurable, ok := session.(audio.ConfigurableMediaSession)
	if !ok || !SupportsSessionMedia(session) {
		return SessionMedia(session)
	}
	return configurable.RTCMediaWithOptions(options), true
}
