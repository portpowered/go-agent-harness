// Package providers defines the provider construction contract shared by
// embeddable runtime services. Implementations live behind this package's
// internal composition boundary and consume only resolved values.
package providers

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	llmproviders "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// Config is the provider-owned value contract. It contains no config-file
// paths, CLI flags, or environment lookup rules.
type Config struct {
	Provider   string
	Model      string
	APIKey     string
	BaseURL    string
	Fal        *FalConfig
	RecordPath string
	ReplayPath string
}

// FalConfig contains provider-specific values used by fal.ai.
type FalConfig struct {
	Model   string
	APIKey  string
	BaseURL string
}

// Service builds providers from explicit request values. Build may allocate a
// network client, so callers should invoke it at admission time rather than
// while composing an otherwise inert host.
type Service interface {
	Build(context.Context, Config) (llmproviders.Provider, error)
}

// FullService is the built-in provider graph's complete role. Keeping this
// combined type separate preserves the small stateless Service seam for
// embedders that only need ordinary inference while allowing the application
// graph to inject one owner for both turn based and continuous providers.
type FullService interface {
	Service
	SessionService
	ModelAdmission
	ModelCatalog
}

// SessionService builds provider-backed continuous sessions from resolved
// values. Credential resolution belongs to the host edge; this contract only
// receives the credential value needed to construct the provider. A session
// builder is separate from Service so stateless provider fakes do not need to
// implement realtime behavior.
type SessionService interface {
	BuildSession(context.Context, SessionConfig) (messages.SessionInferencer, error)
}

// SessionConfig is the provider-neutral input to a continuous session
// builder. It contains no CLI flags, config-file paths, environment names, or
// device selectors. Replay and recording paths are explicit artifact values
// owned by the caller and are never discovered by the provider service.
type SessionConfig struct {
	Provider    string
	Model       string
	APIKey      string
	BaseURL     string
	RealtimeURL string

	Instructions                  string
	Voice                         string
	ReasoningEffort               string
	InputAudioFormat              models.AudioFormat
	OutputAudioFormat             models.AudioFormat
	InputAudioSampleRate          models.SampleRate
	OutputAudioSampleRate         models.SampleRate
	TurnDetection                 *models.TurnDetectionConfig
	InputTranscription            *models.InputAudioTranscriptionConfig
	Tools                         []messages.ToolDefinition
	ClientOwnsAudioTurnBoundaries bool

	// WebSocketDialer is optional. A nil value selects the provider's default
	// transport, while a host can inject replay or hermetic transports.
	WebSocketDialer transport.Dialer
	// ReplayPath selects an explicit raw session capture. The provider service
	// creates a replay transport and never connects to a live provider when it
	// is set.
	ReplayPath   string
	ReplayTiming string
	// RecordPath selects an explicit raw capture artifact. The returned
	// inferencer flushes the capture when its session terminates.
	RecordPath string
}

// CaptureWriter is an optional request-scoped role returned by Build when
// recording is selected. Flush runs at the invocation boundary, outside ticks.
type CaptureWriter interface {
	FlushToFile(string) error
}
