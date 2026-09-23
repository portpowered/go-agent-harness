// Package selfplay defines the host-neutral contract for a bounded two-sided
// realtime conversation. Implementations and mutable run state stay private to
// this service.
package selfplay

import (
	"context"
	"time"
)

const (
	// DefaultProvider is the only provider enabled by the initial self-play service.
	DefaultProvider = "openai"
	// DefaultModel is the realtime model used when a request omits one.
	DefaultModel = "gpt-realtime"
	// DefaultMaxDuration bounds a request that omits its duration.
	DefaultMaxDuration = 2 * time.Minute
	// DefaultTurnTarget bounds a request that omits its completed-turn target.
	DefaultTurnTarget = 3
)

// StopReason is the single terminal reason committed for one run.
type StopReason string

const (
	StopMaxDuration StopReason = "max_duration"
	StopTurnTarget  StopReason = "turn_target"
	StopFailure     StopReason = "failure"
)

// SideRole identifies the fixed participant role in a self-play run.
type SideRole string

const (
	RoleCustomer  SideRole = "customer"
	RoleAssistant SideRole = "assistant"
)

// SideTerminal describes how the service ended one provider session.
type SideTerminal string

const (
	SideNotStarted SideTerminal = "not_started"
	SideStopped    SideTerminal = "stopped_by_run_bound"
	SideFailed     SideTerminal = "failed"
)

// SentinelError is an immutable error category for stable request and runtime
// failures. Constants preserve errors.Is behavior without mutable package state.
type SentinelError string

func (e SentinelError) Error() string { return string(e) }

const (
	ErrInvalidRequest         SentinelError = "invalid self-play request"
	ErrUnsupportedProvider    SentinelError = "unsupported self-play provider"
	ErrUnsupportedModel       SentinelError = "unsupported self-play model"
	ErrCredentialRequired     SentinelError = "self-play API credential is required"
	ErrModelCatalogRequired   SentinelError = "self-play model catalog is required"
	ErrSessionServiceRequired SentinelError = "self-play session service is required"
	ErrOutputTargetUnsafe     SentinelError = "self-play output target is unsafe"
	ErrArtifactLimit          SentinelError = "self-play evidence limit exceeded"
	ErrShutdownTimeout        SentinelError = "self-play shutdown deadline exceeded"
)

// Request contains only values resolved by the host. Config-file paths,
// provider objects, writers, and mutable runtime state are intentionally absent.
type Request struct {
	APIKey      string
	OutputDir   string
	Provider    string
	Model       string
	BaseURL     string
	MaxDuration time.Duration
	MaxTurns    int
}

// ArtifactOutcome reports one durable file using a run-relative path.
type ArtifactOutcome struct {
	Path     string
	Bytes    int64
	Complete bool
}

// SideResult is the final value for one conversation side.
type SideResult struct {
	Role           SideRole
	CompletedTurns int
	Terminal       SideTerminal
	TerminalError  string
	WAV            ArtifactOutcome
	Diagnostics    ArtifactOutcome
	StreamDeltas   ArtifactOutcome
}

// Result is a value-only snapshot of the completed run and its durable output.
type Result struct {
	StopReason StopReason
	Customer   SideResult
	Assistant  SideResult
	StartedAt  time.Time
	EndedAt    time.Time
	Elapsed    time.Duration
	Manifest   ArtifactOutcome
}

// Service runs one bounded self-play conversation.
type Service interface {
	Run(context.Context, Request) (Result, error)
}
