// Package livedelegation runs the backend work a live voice provider hands
// off to the client (GPT-Live client delegation, docs/architecture/
// gpt-live-provider.md 2.5). The provider reports each delegation as a
// DELEGATION.CREATED stream message; the live session hands it to an
// Executor, which runs a nested turn-based agent loop on a configured backend
// provider with the session's own tools and answers with CONTEXT.APPEND
// messages that quote the delegation id.
//
// The executor sits outside the voice loop's ToolRunner: delegations never
// become loop tool calls, never request a response, and are not cancelled by
// a user interrupt. They end only by Executor.Cancel (a task revision) or by
// Executor.Close at the end of the session. The implementation is private and
// constructed through services/livedelegation/wire.
package livedelegation

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// Error is the stable identity type for delegation sentinel errors.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrClosed rejects work submitted after Executor.Close.
	ErrClosed Error = "live delegation executor is closed"
	// ErrAppendUnavailable reports a binding with no way to answer a
	// delegation.
	ErrAppendUnavailable Error = "live delegation append port is required"
	// ErrBackendUnavailable reports a policy that names no backend provider.
	ErrBackendUnavailable Error = "live delegation backend provider is not configured"
	// ErrTurnBudget, ErrTokenBudget and ErrTimeBudget end a nested loop that
	// exceeded one of its Limits.
	ErrTurnBudget  Error = "live delegation exceeded its turn budget"
	ErrTokenBudget Error = "live delegation exceeded its token budget"
	ErrTimeBudget  Error = "live delegation exceeded its time budget"
)

// Defaults applied to a zero Limits field (design open question Q11 and the
// PR 4 budget).
const (
	DefaultConcurrency = 2
	DefaultMaxTurns    = 8
	DefaultMaxDuration = 2 * time.Minute
	DefaultMaxTokens   = 200_000
)

// Limits bound the executor and each nested loop. A zero field selects its
// default. Concurrency is the worker-pool limit: work beyond it waits in an
// unbounded FIFO queue and is never dropped. MaxTurns counts backend
// inferences, MaxDuration is measured on the injected clock from when the
// delegation starts running, and MaxTokens is the backend's reported total
// usage across the delegation.
type Limits struct {
	Concurrency int
	MaxTurns    int
	MaxDuration time.Duration
	MaxTokens   int
}

// Backend names the stateless text provider that reasons for a delegation.
// It never carries a raw secret: CredentialReference is an opaque host
// reference resolved by the service's CredentialResolver when the backend is
// first built, and ChatGPTAuthPath is the `yui auth chatgpt` store the
// openai-chatgpt provider signs requests with. An empty Model selects the
// provider's default (for openai-chatgpt, the account's default model).
type Backend struct {
	Provider            string
	Model               string
	BaseURL             string
	ChatGPTAuthPath     string
	CredentialReference string
	// Unconfigured is a host-detected reason the backend cannot run, such as
	// a missing text model. Every delegation is then answered with a
	// configuration failure, and the reason is logged, not spoken.
	Unconfigured string
}

// Policy is the host-resolved delegation configuration of one live session.
type Policy struct {
	Backend Backend
	Limits  Limits
	// Instructions are the task instructions placed in the backend prompt
	// (the "Task instructions" section of the suggested GPT-Live prefix).
	Instructions string
}

// ToolEvent reports one session tool call a delegation made: once when it
// starts (Done false) and once when it ends (Done true, with Response or
// Err). Hosts record it as session evidence tagged with the delegation id.
type ToolEvent struct {
	DelegationID string
	Call         messages.ToolCall
	Done         bool
	Response     messages.ToolCallResponse
	Err          error
}

// ToolLock admits one tool call into the resource group of toolName and
// returns its release. It waits until the group is free, or returns ctx's
// cause when ctx ends first. An ungrouped tool is admitted at once.
type ToolLock func(ctx context.Context, toolName string) (release func(), err error)

// Binding connects one executor to its live session. Tools is the session's
// own tool executor, already restricted to the session's tool surface and
// policy, and Definitions samples that surface at each delegation. Append
// delivers one CONTEXT.APPEND to the provider and reports whether it was
// admitted. History samples the session's conversation so short replies
// stay resolvable. ToolLock, when set, is the session's resource-group lock
// for tools that share host state; the session's own voice loop takes the
// same lock, so a delegation's browser call never overlaps another
// delegation's or the voice loop's. OnTool, when set, observes every tool
// call a delegation makes. Scheduler measures time budgets; nil selects the
// host clock.
type Binding struct {
	SessionID   string
	Policy      Policy
	Tools       messages.ToolExecutor
	Definitions func() []messages.ToolDefinition
	ToolLock    ToolLock
	History     func() []messages.Message
	Append      func(context.Context, *messages.ContextAppendValue) error
	OnTool      func(ToolEvent)
	Scheduler   platformclock.Scheduler
}

// Executor runs the delegations of one session.
type Executor interface {
	// Submit queues a delegation. It never drops work: it returns an error
	// only after Close. Results may complete out of order; each answer
	// carries its own delegation id.
	Submit(messages.DelegationCreatedValue) error
	// Cancel stops one queued or running delegation without answering it
	// (a task revision supersedes it). It reports whether the id was known.
	Cancel(delegationID string) bool
	// Close cancels every queued and running delegation and waits for the
	// workers to exit. It is idempotent.
	Close() error
}

// CredentialResolver resolves a Backend.CredentialReference.
type CredentialResolver func(context.Context, string) (string, error)

// Service opens one Executor per live session. ctx is the session lifetime:
// cancelling it cancels every delegation, as Close does.
type Service interface {
	Open(ctx context.Context, binding Binding) (Executor, error)
}
