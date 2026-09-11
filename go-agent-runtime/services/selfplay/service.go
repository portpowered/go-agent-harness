// Package selfplay owns the provider-neutral bounded two-session self-play
// service. Hosts supply session construction and session execution ports;
// provider and terminal policy stays behind the package's private runtime.
package selfplay

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

const (
	SelfPlayDefaultProvider    = "openai"
	SelfPlayDefaultModel       = "gpt-realtime"
	SelfPlayDefaultMaxDuration = 2 * time.Minute
	SelfPlayDefaultTurnTarget  = 3
	SelfPlayCustomerPersona    = "You are the customer. Speak naturally, briefly, and only as part of a spoken conversation. Ask one practical follow-up at a time. Do not call tools."
	SelfPlayAssistantPersona   = "You are the helpful assistant. Speak naturally, briefly, and only as part of a spoken conversation. Answer the customer's latest request and ask one concise follow-up when useful. Do not call tools."
	SelfPlayOpeningSeed        = "Hi, I need help planning a simple weekend trip."
)

// StopReason is the immutable reason a bounded invocation stopped.
type StopReason string

const (
	StopMaxDuration StopReason = "max_duration"
	StopTurnTarget  StopReason = "turn_target"
	StopFailure     StopReason = "failure"
)

// RunOptions contains only host-resolved values. It intentionally has no
// provider, loop, transport, or filesystem implementation fields.
type RunOptions struct {
	APIKey         string
	OutputDir      string
	Provider       string
	Model          string
	BaseURL        string
	ConfigDir      string
	MaxDuration    time.Duration
	MaxTurns       int
	EvidenceLimits EvidenceLimits
}

type SelfPlayRunOptions = RunOptions
type Options = RunOptions

// Result is the bounded result snapshot. Counts belong to the same terminal
// transition as StopReason, so a later drain cannot change them.
type Result struct {
	StopReason     StopReason
	CustomerTurns  int
	AssistantTurns int
}

// SessionRequest is the complete value request for one side's session. The
// factory owns all concrete provider and websocket construction.
type SessionRequest struct {
	APIKey    string
	Provider  string
	Model     string
	BaseURL   string
	ConfigDir string
	Prompt    string
	Persona   string
	Clock     clock.Source
}

// SessionFactory is the explicit construction port for the two sessions.
type SessionFactory interface {
	NewSession(context.Context, SessionRequest) (messages.SessionInferencer, error)
}

type SessionFactoryFunc func(context.Context, SessionRequest) (messages.SessionInferencer, error)

func (f SessionFactoryFunc) NewSession(ctx context.Context, request SessionRequest) (messages.SessionInferencer, error) {
	if f == nil {
		return nil, errors.New("self-play session factory is required")
	}
	return f(ctx, request)
}

// AudioInput is the only session-loop operation the runtime needs for a PCM
// bridge. It keeps the concrete agent loop out of the public contract.
type AudioInput interface {
	SendAudioInput(context.Context, []byte) error
}

// SessionRunOptions configures one host session runner. Callbacks are
// observations/admission ports; the runtime remains the owner of the bound.
type SessionRunOptions struct {
	Prompt   string
	Provider string
	Model    string
	Clock    clock.Source
	Done     <-chan struct{}
	DoneErr  func() error
	Ready    chan<- AudioInput

	ObserveStream     func(messages.StreamMessage)
	ObserveDiagnostic func(Diagnostic)
	AdmitTurn         func(messages.StreamMessage) bool
}

type SessionRunner interface {
	Run(context.Context, messages.SessionInferencer, SessionRunOptions) error
}

type SessionRunnerFunc func(context.Context, messages.SessionInferencer, SessionRunOptions) error

func (f SessionRunnerFunc) Run(ctx context.Context, session messages.SessionInferencer, options SessionRunOptions) error {
	if f == nil {
		return errors.New("self-play session runner is required")
	}
	return f(ctx, session, options)
}

// Dependencies are the complete process-scoped runtime ports. No default
// provider or loop implementation is discovered through optional interfaces.
type Dependencies struct {
	Clock          clock.Source
	ModelAdmission providers.ModelAdmission
	Sessions       SessionFactory
	Runner         SessionRunner
}

// Service is the runtime service contract exposed to hosts and Wire.
type Service interface {
	Run(context.Context, io.Writer, RunOptions) error
	RunWithResult(context.Context, io.Writer, RunOptions) (Result, error)
}

type RunFunc func(context.Context, io.Writer, RunOptions) error

func (f RunFunc) Run(ctx context.Context, out io.Writer, options RunOptions) error {
	if f == nil {
		return errors.New("self-play service is required")
	}
	return f(ctx, out, options)
}
