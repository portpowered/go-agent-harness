package wire

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeproviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	runtimeModels "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// provideLiveService is the host adapter for the reusable continuous session
// owner. It resolves the manifest's opaque credential reference at the CLI
// edge, then delegates protocol construction to the runtime provider service.
// No provider, websocket, or replay implementation is constructed here.
func provideLiveService(
	providerService runtimeproviders.SessionService,
	toolExecutor messages.ToolExecutor,
	toolDefs []messages.ToolDefinition,
	sessionInferencer messages.SessionInferencer,
	transportDialer transport.Dialer,
	clockSource Clock,
	credentialVault *liveCredentialVault,
) session.LiveService {
	return sessionwire.NewLiveService(sessionwire.LiveDependencies{
		InferencerFactory: newLiveInferencerFactory(providerService, toolDefs, sessionInferencer, credentialVault, transportDialer),
		ToolExecutor:      toolExecutor,
		ToolDefinitions:   append([]messages.ToolDefinition(nil), toolDefs...),
		Clock:             liveClock(clockSource),
		Scheduler:         liveScheduler(clockSource),
	})
}

func liveClock(source Clock) session.LiveClock {
	if source == nil {
		return nil
	}
	return source.Now
}

func liveScheduler(source Clock) clock.Scheduler {
	if source == nil {
		return nil
	}
	if scheduler, ok := source.(clock.Scheduler); ok {
		return scheduler
	}
	return nil
}

func newLiveInferencerFactory(
	providerService runtimeproviders.SessionService,
	toolDefs []messages.ToolDefinition,
	sessionInferencer messages.SessionInferencer,
	credentialVault *liveCredentialVault,
	transportDialer transport.Dialer,
) session.LiveInferencerFactory {
	return func(ctx context.Context, request session.LiveRequest) (messages.SessionInferencer, error) {
		if sessionInferencer != nil {
			return sessionInferencer, nil
		}
		if providerService == nil {
			return nil, fmt.Errorf("live provider service is unavailable")
		}
		apiKey, err := resolveLiveCredential(request.CredentialReference, credentialVault)
		if err != nil {
			return nil, err
		}
		config := liveSessionConfig(request, apiKey, toolDefs)
		// The legacy default port is an unconfigured placeholder. Leaving the
		// override nil selects the provider service's native transport; an
		// explicitly injected port must reach that same service.
		if _, placeholder := transportDialer.(inertTransportDialer); !placeholder {
			config.WebSocketDialer = transportDialer
		}
		return providerService.BuildSession(ctx, config)
	}
}

func liveSessionConfig(request session.LiveRequest, apiKey string, toolDefs []messages.ToolDefinition) runtimeproviders.SessionConfig {
	if apiKey == "" && strings.TrimSpace(request.Replay.InputCapturePath) != "" {
		// A replay owns its transport and never reaches the provider's live
		// dialer. Provider constructors still validate their credential option,
		// so use a non-secret sentinel only for this explicit offline path.
		apiKey = "replay"
	}
	tools := append([]messages.ToolDefinition(nil), toolDefs...)
	if request.Capabilities != nil {
		tools = append([]messages.ToolDefinition(nil), request.Capabilities.Definitions...)
	}
	config := runtimeproviders.SessionConfig{
		Provider: request.Provider, Model: request.Model, APIKey: apiKey,
		BaseURL: request.BaseURL, RealtimeURL: request.RealtimeURL,
		Instructions: request.Instructions, Voice: request.Voice,
		ReasoningEffort:               request.ReasoningEffort,
		InputAudioFormat:              runtimeModels.AudioFormat(request.InputAudioFormat),
		OutputAudioFormat:             runtimeModels.AudioFormat(request.OutputAudioFormat),
		InputAudioSampleRate:          runtimeModels.SampleRate(request.InputAudioSampleRate),
		OutputAudioSampleRate:         runtimeModels.SampleRate(request.OutputAudioSampleRate),
		TurnDetection:                 liveTurnDetection(request.TurnDetection),
		Tools:                         tools,
		ClientOwnsAudioTurnBoundaries: request.ClientOwnsAudioTurnBoundaries,
		ReplayPath:                    request.Replay.InputCapturePath,
		ReplayTiming:                  liveReplayTiming(request.Replay.Timing),
		RecordPath:                    request.Replay.OutputCapturePath,
	}
	if request.InputTranscription {
		config.InputTranscription = &runtimeModels.InputAudioTranscriptionConfig{Enabled: true, Model: request.InputTranscriptionModel}
	}
	return config
}

func resolveLiveCredential(reference string, vault *liveCredentialVault) (string, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return "", nil
	}
	if strings.HasPrefix(reference, "cli-credential:") {
		if vault != nil {
			if value, ok := vault.Take(reference); ok {
				return value, nil
			}
		}
		return "", fmt.Errorf("live credential reference %q is unavailable", reference)
	}
	name := strings.TrimPrefix(reference, "env:")
	if value, ok := os.LookupEnv(name); ok && strings.TrimSpace(value) != "" {
		return value, nil
	}
	return "", fmt.Errorf("live credential reference %q is unavailable", reference)
}

func liveTurnDetection(policy *session.LiveTurnDetection) *runtimeModels.TurnDetectionConfig {
	if policy == nil {
		return nil
	}
	return &runtimeModels.TurnDetectionConfig{
		Type: policy.Type, Threshold: policy.Threshold, PrefixPaddingMs: policy.PrefixPaddingMs,
		SilenceDurationMs: policy.SilenceDurationMs, CreateResponse: cloneBool(policy.CreateResponse),
		InterruptResponse: cloneBool(policy.InterruptResponse), Eagerness: policy.Eagerness,
	}
}

func liveReplayTiming(timing session.LiveReplayTiming) string {
	switch timing {
	case session.LiveReplayTimingRealtime:
		return "realtime"
	case session.LiveReplayTimingFast:
		return "fast"
	case session.LiveReplayTimingStep:
		return "step"
	default:
		return "fast"
	}
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
