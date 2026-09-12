// Package sessionupdate decorates a bidirectional session with the initial
// instruction and tool-definition update required by session providers.
//
// The contract is deliberately host-neutral: it contains no CLI, device, or
// provider transport types. Hosts that need a private capability such as RTC
// media can keep a narrow adapter around the original session.
package sessionupdate

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// Config is the immutable configuration sent once when a decorated session
// announces SESSION.OPEN or SESSION.CREATED.
type Config struct {
	Instructions    string
	ToolDefinitions []messages.ToolDefinition
}

// UpdateSendError preserves the typed send status and underlying cause when
// the initial SESSION.UPDATE cannot be admitted by the inner session.
type UpdateSendError struct {
	Status messages.SessionSendStatus
	Err    error
}

func (e *UpdateSendError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return "session update send failed: " + string(e.Status)
	}
	return "session update send failed: " + string(e.Status) + ": " + e.Err.Error()
}

func (e *UpdateSendError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// CompleteMessageSender is the optional complete-message capability used by
// multimodal and tool-result session callers.
type CompleteMessageSender interface {
	SendMessage(context.Context, messages.Message) bool
}

// CompleteMessageWithoutResponseSender is the optional deferred complete-
// message capability used when a caller batches a result before requesting a
// response.
type CompleteMessageWithoutResponseSender interface {
	SendMessageWithoutResponse(context.Context, messages.Message) bool
}

// CompleteMessageCapabilities reports which optional complete-message paths
// reach the underlying session.
type CompleteMessageCapabilities interface {
	SupportsCompleteMessages() bool
	SupportsCompleteMessagesWithoutResponse() bool
}

// TerminalErrorSource exposes a terminal transport error when the wrapped
// session provides one.
type TerminalErrorSource interface {
	TerminalError() error
}

// DecoratedSession is the public host-neutral session surface returned by the
// service. Optional capabilities are promoted deliberately; their Supports
// methods keep unsupported sessions distinguishable from capable ones.
type DecoratedSession interface {
	messages.Session
	messages.SessionSendOutcomeSender
	messages.SessionResponseRequester
	messages.SessionResponseCapability
	CompleteMessageSender
	CompleteMessageWithoutResponseSender
	CompleteMessageCapabilities
	TerminalErrorSource
}

// Service decorates inferencers and connected sessions with the one-shot
// session instruction update lifecycle.
type Service interface {
	Decorate(messages.SessionInferencer, Config) messages.SessionInferencer
	DecorateSession(context.Context, messages.Session, Config) DecoratedSession
}
