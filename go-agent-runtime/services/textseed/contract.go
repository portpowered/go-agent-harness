// Package textseed owns the explicit text-seed boundary for persistent
// sessions. Hosts provide the seed and consume the session/output adapters;
// sentinel allocation and lifecycle policy stay in the runtime service.
package textseed

import (
	"context"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const (
	// WirePromptPrefix identifies prompts that are internal to the runtime
	// boundary. The complete allocated value is never intended for provider
	// payloads.
	WirePromptPrefix = "\x00agent-cli-session-text-seed:"
	// ReceiveCapacity bounds the forwarding buffer owned by one wrapped
	// session.
	ReceiveCapacity = 256
)

// Seed preserves the difference between an omitted prompt and an explicitly
// supplied empty prompt.
type Seed struct {
	Value   string
	Present bool
}

// Allocator supplies one non-empty, unique wire value for each allocation.
// Implementations must make Allocate safe for concurrent callers.
type Allocator interface {
	Allocate() string
}

// AllocatorFunc adapts a function to Allocator, which is useful for
// deterministic composition tests and small hosts.
type AllocatorFunc func() string

func (f AllocatorFunc) Allocate() string { return f() }

// Output is the serialized text sink used by a session run. Err retains the
// first write failure, including a nil-error short write as io.ErrShortWrite.
type Output interface {
	io.Writer
	Err() error
}

// CompleteMessageSender is the optional multimodal send capability forwarded
// by a wrapped session.
type CompleteMessageSender interface {
	SendMessage(context.Context, messages.Message) bool
}

// CompleteMessageWithoutResponseSender is the optional multimodal send
// capability that queues a message without starting a response.
type CompleteMessageWithoutResponseSender interface {
	SendMessageWithoutResponse(context.Context, messages.Message) bool
}

// CompleteMessageCapabilities reports which optional multimodal send paths
// reach the underlying session.
type CompleteMessageCapabilities interface {
	SupportsCompleteMessages() bool
	SupportsCompleteMessagesWithoutResponse() bool
}

// TerminalErrorSource exposes a provider terminal error without changing its
// identity.
type TerminalErrorSource interface {
	TerminalError() error
}

// Session is the public shape returned by Service.WrapSession. Optional
// capabilities remain visible through the wrapper and report false when the
// underlying session does not implement them.
type Session interface {
	messages.Session
	messages.SessionSendOutcomeSender
	messages.SessionResponseRequester
	messages.SessionResponseCapability
	CompleteMessageSender
	CompleteMessageWithoutResponseSender
	CompleteMessageCapabilities
	TerminalErrorSource
}

// Service owns allocation and the private session/output implementations.
// The host supplies prompt shaping and receives only these narrow adapters.
type Service interface {
	Allocate() string
	WrapInferencer(messages.SessionInferencer, string, Seed) messages.SessionInferencer
	WrapSession(context.Context, messages.Session, string, Seed) Session
	NewOutput(io.Writer) Output
}
