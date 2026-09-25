package wire

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeproviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	runtimeRecording "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// provideLiveService is the host adapter for the reusable continuous session
// owner. It supplies the manifest credential resolver and transport ports;
// the session service translates requests into provider sessions. No
// provider, websocket, or replay implementation is constructed here.
func provideLiveService(
	providerService runtimeproviders.SessionService,
	recordingService runtimeRecording.Service,
	toolExecutor messages.ToolExecutor,
	toolDefs []messages.ToolDefinition,
	sessionInferencer messages.SessionInferencer,
	transportDialer transport.Dialer,
	clockSource Clock,
	runtimeObserver SessionRuntimeObserver,
	credentialVault *liveCredentialVault,
) session.LiveService {
	return sessionwire.NewLiveService(sessionwire.LiveDependencies{
		InferencerFactory: newLiveInferencerFactory(providerService, recordingService, toolDefs, sessionInferencer, credentialVault, transportDialer),
		ToolExecutor:      toolExecutor,
		ToolDefinitions:   append([]messages.ToolDefinition(nil), toolDefs...),
		Clock:             liveClock(clockSource),
		Scheduler:         liveScheduler(clockSource),
		RuntimeObserver:   runtimeObserver,
		Tick:              liveTick(clockSource),
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

func liveTick(source Clock) func() uint64 {
	if tickSource, ok := source.(interface{ Tick() uint64 }); ok {
		return tickSource.Tick
	}
	return nil
}

func newLiveInferencerFactory(
	providerService runtimeproviders.SessionService,
	recordingService runtimeRecording.Service,
	toolDefs []messages.ToolDefinition,
	sessionInferencer messages.SessionInferencer,
	credentialVault *liveCredentialVault,
	transportDialer transport.Dialer,
) session.LiveInferencerFactory {
	if sessionInferencer != nil {
		return func(_ context.Context, request session.LiveRequest) (messages.SessionInferencer, error) {
			return configureInjectedLiveInferencer(sessionInferencer, request.Replay.OutputCapturePath, request.Replay.InjectedCaptureAllowed, recordingService)
		}
	}
	// The legacy default port is an unconfigured placeholder. Leaving the
	// override nil selects the provider service's native transport; an
	// explicitly injected port must reach that same service.
	var dialer transport.Dialer
	if _, placeholder := transportDialer.(inertTransportDialer); !placeholder {
		dialer = transportDialer
	}
	return sessionwire.NewProviderInferencerFactory(sessionwire.ProviderInferenceDependencies{
		Providers: providerService, Dialer: dialer, ToolDefinitions: toolDefs,
		Credentials: func(_ context.Context, reference string) (string, error) {
			return resolveLiveCredential(reference, credentialVault)
		},
	})
}

func configureInjectedLiveInferencer(inferencer messages.SessionInferencer, capturePath string, allowSessionCapture bool, recordingService runtimeRecording.Service) (messages.SessionInferencer, error) {
	if configurator, ok := inferencer.(interface{ ConfigureProviderCapture(string) error }); ok {
		if strings.TrimSpace(capturePath) == "" {
			return inferencer, nil
		}
		if err := configurator.ConfigureProviderCapture(capturePath); err != nil {
			return nil, fmt.Errorf("configure injected provider capture: %w", err)
		}
		return inferencer, nil
	}
	if path := strings.TrimSpace(capturePath); path != "" && allowSessionCapture {
		if recordingService == nil {
			return nil, fmt.Errorf("recording service is required for injected session capture")
		}
		return recordingService.TrackInjectedSession(inferencer, path)
	}
	return inferencer, nil
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
