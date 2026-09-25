// Package providerbuild translates admitted live requests into provider
// session configuration. Provider protocol construction stays behind the
// providers service; this package owns only the live-request projection and
// the credential, tool, and transport choices made at provider admission.
package providerbuild

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// replayCredential is a non-secret sentinel for explicit offline replay. A
// replay owns its transport and never reaches the provider's live dialer, but
// provider constructors still validate their credential option.
const replayCredential = "replay"

// Dependencies are the provider, credential, tool, and transport edges used
// to build one participant's provider session.
type Dependencies struct {
	Providers       providers.SessionService
	Credentials     session.LiveCredentialResolver
	Dialer          transport.Dialer
	ToolDefinitions []messages.ToolDefinition
}

// NewFactory returns the live inferencer factory backed by the provider
// service. Credentials are resolved only when the session is built.
func NewFactory(deps Dependencies) session.LiveInferencerFactory {
	tools := append([]messages.ToolDefinition(nil), deps.ToolDefinitions...)
	return func(ctx context.Context, request session.LiveRequest) (messages.SessionInferencer, error) {
		if deps.Providers == nil {
			return nil, errors.New("live provider service is unavailable")
		}
		apiKey, err := resolveCredential(ctx, deps.Credentials, request.CredentialReference)
		if err != nil {
			return nil, err
		}
		config := SessionConfig(request, apiKey, tools)
		config.WebSocketDialer = deps.Dialer
		return deps.Providers.BuildSession(ctx, config)
	}
}

func resolveCredential(ctx context.Context, resolve session.LiveCredentialResolver, reference string) (string, error) {
	if strings.TrimSpace(reference) == "" {
		return "", nil
	}
	if resolve == nil {
		return "", fmt.Errorf("live credential reference %q is unavailable", reference)
	}
	return resolve(ctx, reference)
}

// SessionConfig projects one live request onto the provider session
// contract. Participant capabilities replace the default tool surface.
func SessionConfig(request session.LiveRequest, apiKey string, defaultTools []messages.ToolDefinition) providers.SessionConfig {
	if apiKey == "" && strings.TrimSpace(request.Replay.InputCapturePath) != "" {
		apiKey = replayCredential
	}
	tools := append([]messages.ToolDefinition(nil), defaultTools...)
	if request.Capabilities != nil {
		tools = append([]messages.ToolDefinition(nil), request.Capabilities.Definitions...)
	}
	config := providers.SessionConfig{
		Provider: request.Provider, Model: request.Model, APIKey: apiKey,
		BaseURL: request.BaseURL, RealtimeURL: request.RealtimeURL,
		Instructions: request.Instructions, Voice: request.Voice,
		ReasoningEffort:               request.ReasoningEffort,
		InputAudioFormat:              models.AudioFormat(request.InputAudioFormat),
		OutputAudioFormat:             models.AudioFormat(request.OutputAudioFormat),
		InputAudioSampleRate:          models.SampleRate(request.InputAudioSampleRate),
		OutputAudioSampleRate:         models.SampleRate(request.OutputAudioSampleRate),
		TurnDetection:                 turnDetection(request.TurnDetection),
		Tools:                         tools,
		ClientOwnsAudioTurnBoundaries: request.ClientOwnsAudioTurnBoundaries,
		ReplayPath:                    request.Replay.InputCapturePath,
		ReplayTiming:                  replayTiming(request.Replay.Timing),
		RecordPath:                    request.Replay.OutputCapturePath,
		SessionMessageReplay:          request.Replay.Kind == session.LiveReplayKindTurn,
	}
	if request.InputTranscription {
		config.InputTranscription = &models.InputAudioTranscriptionConfig{Enabled: true, Model: request.InputTranscriptionModel}
	}
	return config
}

func turnDetection(policy *session.LiveTurnDetection) *models.TurnDetectionConfig {
	if policy == nil {
		return nil
	}
	return &models.TurnDetectionConfig{
		Type: policy.Type, Threshold: policy.Threshold, PrefixPaddingMs: policy.PrefixPaddingMs,
		SilenceDurationMs: policy.SilenceDurationMs, CreateResponse: cloneBool(policy.CreateResponse),
		InterruptResponse: cloneBool(policy.InterruptResponse), Eagerness: policy.Eagerness,
	}
}

func replayTiming(timing session.LiveReplayTiming) string {
	switch timing {
	case session.LiveReplayTimingRealtime:
		return string(session.LiveReplayTimingRealtime)
	case session.LiveReplayTimingStep:
		return string(session.LiveReplayTimingStep)
	default:
		return string(session.LiveReplayTimingFast)
	}
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
