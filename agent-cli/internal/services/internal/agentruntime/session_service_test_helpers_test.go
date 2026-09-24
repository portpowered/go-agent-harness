package agentruntime_test

import (
	"context"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	agentruntime "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime"
	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	servicewire "github.com/portpowered/go-agent-harness/agent-cli/internal/services/wire"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audioiowire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
	runtimedevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	runtimedeviceswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices/wire"
	providerswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/wire"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

// newInjectedSessionService keeps moved CLI tests on the same explicit graph
// as production. The old tests supplied only a clock to NewSessionService;
// that no longer constructs a runtime, so this test-only adapter composes the
// runtime once and preserves each test's inferencer/device seams.
func newInjectedSessionService(deps servicewire.SessionDependencies) serviceSession.SessionService {
	if deps.Clock == nil {
		deps.Clock = platformclock.Real{}
	}
	if deps.ModelCatalog == nil {
		deps.ModelCatalog = providerswire.NewModelCatalog()
	}
	if deps.Runtime == nil {
		factory := servicewire.NewSessionRuntimeFactory()
		deps.Runtime = servicewire.NewSessionRuntime(
			audioiowire.NewService(),
			deps.Clock,
			deps.ToolService,
			factory,
			deps.RuntimeFactory,
			deps.SessionInferencer,
			deps.ToolExecutor,
			newTestDeviceService(deps.DeviceRegistry),
			deps.RuntimeObserver,
			deps.MetricSampler,
			deps.Logger,
			deps.ModelCatalog,
			servicewire.NewBrowserConversationService(),
		)
	}
	return servicewire.NewSessionService(deps)
}

func newTestDeviceService(registry devicegw.DeviceRegistry) runtimedevices.Service {
	return runtimedeviceswire.NewService(registry, audioiowire.NewService())
}

func sessionRuntimeOptionsForTest(opts agentruntime.SessionRunOptions) agentruntime.SessionRunOptions {
	return opts.WithRuntimeFactory(servicewire.NewSessionRuntimeFactory())
}

func runSessionForTest(ctx context.Context, out io.Writer, opts agentruntime.SessionRunOptions) error {
	return agentruntime.RunSessionWithRuntimeFactory(ctx, out, opts, servicewire.NewSessionRuntimeFactory())
}

func runSessionWithInstructionsForTest(ctx context.Context, out io.Writer, opts agentruntime.SessionRunOptions, prompt string) error {
	return agentruntime.RunSessionWithInstructions(ctx, out, sessionRuntimeOptionsForTest(opts), prompt)
}

func runSessionWithInstructionsAndOptionsForTest(ctx context.Context, out io.Writer, opts agentruntime.SessionRunOptions, audioPath string, maxDuration time.Duration, seed agentruntime.SessionTextSeed, prompt string) error {
	return agentruntime.RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration(ctx, out, sessionRuntimeOptionsForTest(opts), audioPath, maxDuration, seed, prompt)
}

func runSessionWithImagesForTest(ctx context.Context, out io.Writer, opts agentruntime.SessionImageRunOptions) error {
	opts.SessionRunOptions = sessionRuntimeOptionsForTest(opts.SessionRunOptions)
	return agentruntime.RunSessionWithImages(ctx, out, opts)
}

func browserTestToolService(closeCount *int) serviceTools.Service {
	return serviceTools.Factory(func(*config.Config) (serviceTools.Capabilities, error) {
		return serviceTools.Capabilities{
			Definitions:            []messages.ToolDefinition{{Name: "browser_test"}},
			BrowserCapabilityState: webmcp.BrowserCapabilityConnectedUnselected,
			Initialize:             func(context.Context) error { return nil },
			Close: func() error {
				if closeCount != nil {
					(*closeCount)++
				}
				return nil
			},
		}, nil
	})
}
