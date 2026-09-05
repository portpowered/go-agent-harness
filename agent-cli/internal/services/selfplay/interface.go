// Package selfplay defines the application-facing self-play service boundary.
//
// The command configuration is intentionally value-only. Provider session
// factories, inferencers, and other runtime seams remain private to the
// services implementation and its test-support package.
package selfplay

import (
	"context"
	"errors"
	"io"
	"time"
)

const (
	// SelfPlayDefaultProvider is the provider enabled by the Phase 1 command.
	SelfPlayDefaultProvider = "openai"
	// SelfPlayDefaultModel is the default OpenAI Realtime model for self-play.
	SelfPlayDefaultModel = "gpt-realtime"
	// SelfPlayDefaultMaxDuration bounds an invocation that omits the flag.
	SelfPlayDefaultMaxDuration = 2 * time.Minute
	// SelfPlayDefaultTurnTarget is the default completed-turn target per side.
	SelfPlayDefaultTurnTarget = 3

	// SelfPlayCustomerPersona and SelfPlayAssistantPersona are the fixed Phase
	// 1 prompts shown by the CLI and supplied to the private runtime.
	SelfPlayCustomerPersona  = "You are the customer. Speak naturally, briefly, and only as part of a spoken conversation. Ask one practical follow-up at a time. Do not call tools."
	SelfPlayAssistantPersona = "You are the helpful assistant. Speak naturally, briefly, and only as part of a spoken conversation. Answer the customer's latest request and ask one concise follow-up when useful. Do not call tools."
	// SelfPlayOpeningSeed is sent once as the customer-side text seed.
	SelfPlayOpeningSeed = "Hi, I need help planning a simple weekend trip."
)

// RunOptions contains the admitted self-play command values. It deliberately
// excludes concrete transports and inferencers so callers cannot bypass the
// service composition boundary.
type RunOptions struct {
	APIKey      string
	OutputDir   string
	Provider    string
	Model       string
	BaseURL     string
	ConfigDir   string
	MaxDuration time.Duration
	MaxTurns    int
}

// SelfPlayRunOptions preserves the descriptive spelling used by the former
// runtime-owned command options for callers of the public contract.
type SelfPlayRunOptions = RunOptions

// Options is the concise spelling used by service-oriented callers.
type Options = RunOptions

// Service runs one bounded self-play conversation.
type Service interface {
	Run(context.Context, io.Writer, RunOptions) error
}

// RunFunc adapts a function to Service for command tests and composition
// seams that do not need a concrete service value.
type RunFunc func(context.Context, io.Writer, RunOptions) error

func (f RunFunc) Run(ctx context.Context, out io.Writer, options RunOptions) error {
	if f == nil {
		return errors.New("self-play runner is required")
	}
	return f(ctx, out, options)
}
