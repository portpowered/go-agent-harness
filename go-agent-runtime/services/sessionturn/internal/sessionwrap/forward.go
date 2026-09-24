// Package sessionwrap implements the provider-session decorators that carry
// initial turn input: instruction delivery, text-seed substitution, and the
// first image turn. Each decorator forwards the optional provider
// capabilities it historically exposed and no others.
package sessionwrap

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// TerminalErrorSource exposes a provider session's terminal failure.
type TerminalErrorSource interface{ TerminalError() error }

// CompleteMessageSupport distinguishes an optional capability from a
// wrapper method that merely returns false when unsupported.
func CompleteMessageSupport(session messages.Session) (complete, withoutResponse bool) {
	if capabilities, ok := session.(sessionturn.CompleteMessageCapabilities); ok {
		return capabilities.SupportsCompleteMessages(), capabilities.SupportsCompleteMessagesWithoutResponse()
	}
	_, complete = session.(sessionturn.CompleteMessageSender)
	_, withoutResponse = session.(sessionturn.CompleteMessageWithoutResponseSender)
	return complete, withoutResponse
}

// forwarder implements the optional capabilities shared by every decorator
// over the wrapped provider session.
type forwarder struct {
	inner messages.Session
}

// RequestResponse forwards the explicit response capability.
func (f forwarder) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	return messages.RequestSessionResponse(ctx, f.inner)
}

func (f forwarder) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(f.inner)
}

// SendMessage forwards the complete-message path used to deliver rich tool
// results on the same provider connection.
func (f forwarder) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := f.inner.(sessionturn.CompleteMessageSender)
	return ok && sender.SendMessage(ctx, msg)
}

// SendMessageWithoutResponse forwards deferred complete messages.
func (f forwarder) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := f.inner.(sessionturn.CompleteMessageWithoutResponseSender)
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

func (f forwarder) SupportsCompleteMessages() bool {
	complete, _ := CompleteMessageSupport(f.inner)
	return complete
}

func (f forwarder) SupportsCompleteMessagesWithoutResponse() bool {
	_, withoutResponse := CompleteMessageSupport(f.inner)
	return withoutResponse
}

// RTCMedia exposes the wrapped session's media endpoints, if it owns any.
func (f forwarder) RTCMedia() audio.MediaEndpoints {
	if owner, ok := f.inner.(audio.MediaSession); ok {
		return owner.RTCMedia()
	}
	return audio.MediaEndpoints{}
}

// TerminalError exposes the wrapped session's terminal failure.
func (f forwarder) TerminalError() error {
	if source, ok := f.inner.(TerminalErrorSource); ok {
		return source.TerminalError()
	}
	return nil
}
