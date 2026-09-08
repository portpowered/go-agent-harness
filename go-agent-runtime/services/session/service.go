// Package session defines the public session execution contract.
package session

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// Request is the normalized request for a session invocation. Hosts resolve
// files, environment, terminal state, and presentation before constructing
// this value. The service owns provider selection, loop construction, and
// invocation cleanup.
type Request struct {
	Input               agentloop.ExecuteInput
	SessionID           string
	ContinueLastSession bool
	InitialHistory      []messages.Message
	SystemPrompt        string
	Model               string
	Provider            string
	APIKey              string
	BaseURL             string
	OutputModality      string
	ModelConfig         string
	// OutputReasoningTokens asks the execution service to retain reasoning
	// deltas in its stream. Presentation options such as JSON formatting remain
	// host-owned; this value only controls whether the provider's reasoning
	// channel is admitted to the stream.
	OutputReasoningTokens bool
	RecordCapturePath     string
	ReplayCapturePath     string
	SystemPromptSuffix    string
	MaxContinuationDepth  int
}

// Result is the structured terminal value for a session invocation.
type Result struct {
	Text string
	// Messages contains messages dispatched during this invocation, including
	// refusal and binary content. Hosts choose how to present these values.
	Messages []messages.Message
}

// IterativeRequest controls the optional fresh-context loop around a session
// request. It is a transport-neutral form of the CLI loop flags.
type IterativeRequest struct {
	MaxIterations            int
	StopWord                 string
	ContextPressureThreshold float64
	ContextPressureMessage   string
	TraceID                  string
	// TraceStore supplies the durable trace port for this loop when the host
	// chooses per-invocation persistence. The service otherwise uses its
	// composed default store.
	TraceStore TraceStore
	// Interaction is an optional host-owned steering port. The runtime keeps
	// trace state, iteration status, fresh request construction, and stop-word
	// handling; a host may use this port for prompts, presentation, and
	// cancellation mapping without making the runtime depend on a terminal.
	Interaction *IterativeInteraction
}

// IterativeAction is the decision returned by a host after one iteration.
type IterativeAction string

const (
	IterativeContinue IterativeAction = "continue"
	IterativeStop     IterativeAction = "stop"
)

// IterativeDecision carries the host's steering decision and, when present,
// the prompt for the next fresh iteration.
type IterativeDecision struct {
	Action IterativeAction
	Prompt string
}

// IterativeTrace describes the runtime-owned trace after it has been loaded
// or created and before the first iteration starts.
type IterativeTrace struct {
	TraceID        string
	StartIteration int
	MaxIterations  int
	Resumed        bool
}

// IterationResult records one completed or failed iteration.
type IterationResult struct {
	Iteration       int
	SessionID       string
	Text            string
	Err             error
	Interrupted     bool
	StopWordMatched bool
}

// IterativeInteraction is the optional host boundary for interactive loop
// concerns. InitialPrompt is called only when a new trace has no request
// prompt; done lets a host treat input EOF as a clean command exit. The
// runtime calls IterationContext for each fresh turn so a host can map a
// signal or UI action to that turn without installing process handlers in the
// reusable runtime. Each callback is optional for hosts that use only part of
// the interaction boundary.
type IterativeInteraction struct {
	InitialPrompt    func(context.Context) (prompt string, done bool, err error)
	TraceReady       func(context.Context, IterativeTrace) error
	IterationContext func(context.Context, int) (context.Context, func())
	OnIteration      func(context.Context, IterationResult) (IterativeDecision, error)
}

// IterativeResult records all iterations and whether the stop condition was
// reached.
type IterativeResult struct {
	TraceID    string
	Iterations []IterationResult
	Completed  bool
}

// SessionHandle owns one invocation-scoped loop. Close releases logger and
// recorder resources even when a streaming turn fails partway through.
type SessionHandle interface {
	SessionID() string
	Stream(context.Context, agentloop.ExecuteInput) (agentloop.Stream, error)
	Save() error
	Flush(recordPath string) error
	Close() error
}

// Service executes admitted session requests. Implementations are inert at
// construction time and create invocation resources only inside Run/Open.
type Service interface {
	Run(context.Context, Request) (Result, error)
	Open(context.Context, Request) (SessionHandle, error)
	RunIterative(context.Context, Request, IterativeRequest) (IterativeResult, error)
	NewSessionID(context.Context, Request) (string, error)
}
