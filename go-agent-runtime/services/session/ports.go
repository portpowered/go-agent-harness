package session

import (
	"context"

	looplogging "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

// ProviderConfig contains the resolved provider values needed to create a
// session. It is a value contract; callers do not need the runtime's config
// tree or provider implementation packages.
// ProviderConfig is retained as a readable compatibility name for the
// provider-owned contract. New callers may import services/providers directly.
type ProviderConfig = providers.Config

// FalProviderConfig is the provider-owned fal.ai value contract.
type FalProviderConfig = providers.FalConfig

// ModelInfo describes the subset of model capabilities that admission and
// input validation need. Hosts may provide an empty catalog when validation is
// owned by another service.
type ModelInfo struct {
	Name                    string
	Aliases                 []string
	Providers               []string
	InputModalities         []string
	OutputModalities        []string
	SupportedInputMimeTypes []string
}

// SessionStore owns conversation history persistence for a runtime host. The
// store is injected explicitly so an embedded runtime never discovers a home
// directory or CLI storage layout on its own.
type SessionStore interface {
	Load(context.Context, string) ([]Message, error)
	Latest(context.Context) (string, error)
	NewSessionID(context.Context) (string, error)
	Save(context.Context, string, []Message) error
}

// TraceStore is the optional persistence port used by iterative execution.
// Keeping it separate lets text-only hosts omit trace storage while still
// supporting ordinary session history.
type TraceStore interface {
	LoadTrace(context.Context, string) (*TraceRecord, error)
	SaveTrace(context.Context, TraceRecord) error
	NewTraceID(context.Context) (string, error)
}

// Message is an alias to the loop-owned message contract. It keeps the storage
// port readable without adding a second message model.
type Message = messages.Message

// TraceStatus represents the lifecycle state of an iterative trace.
type TraceStatus string

const (
	TraceStatusRunning     TraceStatus = "running"
	TraceStatusInterrupted TraceStatus = "interrupted"
	TraceStatusCompleted   TraceStatus = "completed"
)

// IterationStatus represents the lifecycle state of one iteration.
type IterationStatus string

const (
	IterationStatusRunning     IterationStatus = "running"
	IterationStatusCompleted   IterationStatus = "completed"
	IterationStatusInterrupted IterationStatus = "interrupted"
	IterationStatusFailed      IterationStatus = "failed"
)

// TraceConfig holds the durable configuration needed to resume an iterative
// run.
type TraceConfig struct {
	MaxIterations int
	StopWord      string
	Prompt        string
}

// IterationTrace holds lineage for one iterative session.
type IterationTrace struct {
	Iteration          int
	SessionID          string
	SubAgentSessionIDs []string
	Status             IterationStatus
}

// TraceRecord is the transport-neutral durable representation of an
// iterative run. JSON tags are intentionally omitted because stores own their
// serialization format.
type TraceRecord struct {
	TraceID          string
	Status           TraceStatus
	Config           TraceConfig
	CurrentIteration int
	Iterations       []IterationTrace
}

// Resolution is the host-resolved portion of a request. A resolver may load
// configuration and prompt files before handing this value to the runtime;
// the runtime itself only consumes values and explicit ports.
type Resolution struct {
	Provider ProviderConfig
	// ProviderService is the explicit provider construction owner. Hosts may
	// leave it nil when they inject an Inferencer directly; resolved requests
	// should otherwise use this service rather than the legacy executor factory.
	ProviderService providers.Service
	// Logger is the optional loop logger selected by the host. The runtime
	// defaults to a no-op logger when this port is nil; it never discovers a
	// process logger or config directory.
	Logger       looplogging.Logger
	SystemPrompt string
	// SystemPromptResolved makes an empty prompt authoritative after host resolution.
	SystemPromptResolved bool
	WorkspaceDir         string
	AllowPaths           []string
	// SkillRoots are host-resolved directories that directly contain skills.
	// The session service passes them to the tools owner without inferring
	// config or workspace layouts.
	SkillRoots               []tools.SkillRoot
	Models                   []ModelInfo
	Store                    SessionStore
	TraceStore               TraceStore
	ContinuationNudgeEnabled bool
	ContinuationNudgeMessage string
	RepetitionPenalty        float64
}

// Resolver translates a normalized request into explicit runtime values. It
// is called per invocation so two hosts or sessions can use independent
// configuration without mutable process globals.
type Resolver interface {
	Resolve(context.Context, Request) (Resolution, error)
}

// ResolverFunc adapts a function to Resolver.
type ResolverFunc func(context.Context, Request) (Resolution, error)

func (f ResolverFunc) Resolve(ctx context.Context, request Request) (Resolution, error) {
	return f(ctx, request)
}
