// Package agentruntime owns session request translation and mode dispatch.
package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	contract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentruntime"
	public "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	runtimedevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	runtimeproviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	duration "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	durationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/wire"
	sessiontrace "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/observability"
)

var _ contract.Runtime = (*Dispatcher)(nil)

type Dependencies struct {
	AudioService      audioio.Service
	Clock             clock.Source
	PlanFactory       sessionRuntimeFactory
	ToolService       serviceTools.Service
	RuntimeFactory    SessionRTCRuntimeFactory
	SessionInferencer messages.SessionInferencer
	ToolExecutor      messages.ToolExecutor
	DeviceService     runtimedevices.Service
	RuntimeObserver   SessionRuntimeObserver
	Observability     observability.Dependencies
	ModelCatalog      runtimeproviders.ModelCatalog
}

type Dispatcher struct{ deps Dependencies }

func New(deps Dependencies) *Dispatcher { return &Dispatcher{deps: deps} }

func textSeed(seed public.TextSeed) SessionTextSeed {
	return SessionTextSeed{Value: seed.Value, Present: seed.Present}
}

// ErrLegacyAudioRuntimeRetired identifies requests that must enter through
// the service-owned live host. The legacy dispatcher remains for text and
// replay compatibility, but it must not retain a second file/device audio
// implementation after C189.
var ErrLegacyAudioRuntimeRetired = errors.New("legacy session audio runtime is retired; use the service-owned live session")

func (d *Dispatcher) Run(ctx context.Context, out io.Writer, request public.Request) (runErr error) {
	if d == nil || d.deps.Clock == nil {
		return errors.New("session clock is required")
	}
	if request.AudioInput.Present || len(request.AudioTurns) > 0 || request.AudioOutputPath != "" || len(request.AudioInterrupts) > 0 {
		return ErrLegacyAudioRuntimeRetired
	}
	if request.MaxDuration > 0 {
		capturePath := request.RecordPath
		if capturePath == "" {
			capturePath = request.ReplayPath
		}
		if capturePath != "" {
			artifactBase := strings.TrimSuffix(capturePath, filepath.Ext(capturePath))
			ctx = durationwire.NewService().WithArtifactPaths(ctx, duration.SessionDurationArtifactPaths{AudioPath: artifactBase + ".wav", TranscriptPath: artifactBase + ".jsonl"})
		}
	}
	options, err := d.requestOptions(ctx, request)
	if err != nil {
		return err
	}
	trace, err := prepareTrace(&request, &options, d.deps.Clock)
	if err != nil {
		return err
	}
	if trace != nil {
		defer func() { runErr = errors.Join(runErr, trace.finish(request.RecordDirectory, runErr == nil)) }()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if out == nil {
		return fmt.Errorf("session output is required")
	}
	if len(request.ImagePaths) > 0 {
		if request.RecordDirectory != "" {
			return RunSessionWithImagesAndRecordingDirectory(
				ctx, out, SessionImageRunOptions{
					SessionRunOptions: options,
					ImagePaths:        append([]string(nil), request.ImagePaths...),
					MaxDuration:       request.MaxDuration,
					TextSeed:          textSeed(request.TextSeed),
					SystemPrompt:      request.SystemPrompt,
				}, request.RecordDirectory,
			)
		}
		return RunSessionWithImages(ctx, out, SessionImageRunOptions{
			SessionRunOptions: options,
			ImagePaths:        append([]string(nil), request.ImagePaths...),
			MaxDuration:       request.MaxDuration,
			TextSeed:          textSeed(request.TextSeed),
			SystemPrompt:      request.SystemPrompt,
		})
	}
	if request.RecordDirectory != "" {
		return RunSessionWithRecordingDirectoryAndInstructionsAndAudioOutAndTextSeedAndMaxDuration(
			ctx, out, options, request.RecordDirectory, "",
			request.MaxDuration, textSeed(request.TextSeed), request.SystemPrompt,
		)
	}
	return RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration(
		ctx, out, options, "", request.MaxDuration,
		textSeed(request.TextSeed), request.SystemPrompt,
	)
}

func (d *Dispatcher) requestOptions(ctx context.Context, request public.Request) (SessionRunOptions, error) {
	options := SessionRunOptions{
		RecordPath: request.RecordPath, ReplayPath: request.ReplayPath, ReplayTiming: request.ReplayTiming,
		Provider: request.Provider, ProviderProvided: request.ProviderProvided, Model: request.Model, ModelProvided: request.ModelProvided,
		NoInputTranscription: request.NoInputTranscription,
		APIKey:               request.APIKey, BaseURL: request.BaseURL, ConfigDir: request.ConfigDir, WorkDir: request.WorkDir,
		AllowPaths: append([]string(nil), request.AllowPaths...), Prompt: request.Prompt, PromptProvided: request.PromptProvided,
		Voice: request.Voice, ReasoningEffort: request.ReasoningEffort, SessionInferencer: d.deps.SessionInferencer,
		AudioOutputRequested:     request.AudioOutputRequested,
		RecordSessionCapturePath: request.RecordSessionCapturePath,
		Transport:                request.Transport, TransportProvided: request.TransportProvided, BareLive: request.BareLive,
		Signaling: request.Signaling, SignalingEndpoint: request.SignalingEndpoint,
		MediaSource: request.MediaSource, ToolExecutor: d.deps.ToolExecutor,
		BrowserToolsEnabled: request.BrowserToolsEnabled, BrowserToolsInteractive: request.BrowserToolsInteractive, LoadedConfig: request.LoadedConfig,
		CancellationIntent:   request.CancellationIntent,
		ToolExecutionTimeout: request.ToolExecutionTimeout, Clock: d.deps.Clock,
		AudioService:    d.deps.AudioService,
		RuntimeObserver: d.deps.RuntimeObserver, Diagnostics: request.Diagnostics, ToolDiagnostics: request.ToolDiagnostics,
		DeviceService: d.deps.DeviceService,
		Observability: d.deps.Observability, StreamObserver: request.StreamObserver,
		RTCBinding:       runtimedevices.RTCBindingRequest{HoldToneConfig: request.HoldToneConfig, RemoteEndpoint: request.AudioDeviceServer},
		AudioInTurnBarge: request.AudioInTurnBarge, ClientOwnsAudioTurnBoundaries: request.ClientOwnsAudioTurnBoundaries,
		SessionUpdatedTimeout: request.SessionUpdatedTimeout, WaitForClose: request.WaitForClose,
		runtimeFactory: d.deps.PlanFactory,
		ModelCatalog:   d.deps.ModelCatalog,
	}
	if err := validateSessionCaptureOptions(options); err != nil {
		return SessionRunOptions{}, err
	}
	// Populate device selection before bare-session preflight so persisted
	// defaults resolve without acquiring external resources.
	options.RTCBinding.InputDevice = request.AudioInputDevice
	options.RTCBinding.OutputDevice = request.AudioOutputDevice
	options.RTCBinding.InputPresent = request.AudioInputDevicePresent
	options.RTCBinding.OutputPresent = request.AudioOutputDevicePresent
	options.RTCBinding.FeedbackWarningWriter = request.FeedbackWarningWriter
	if request.InteractiveDevices {
		if !options.RTCBinding.InputPresent && options.RTCBinding.InputDevice == "" && request.LoadedConfig != nil && request.LoadedConfig.Session != nil {
			options.RTCBinding.InputDevice = request.LoadedConfig.Session.InputDevice
		}
		if !options.RTCBinding.OutputPresent && options.RTCBinding.OutputDevice == "" && request.LoadedConfig != nil && request.LoadedConfig.Session != nil {
			options.RTCBinding.OutputDevice = request.LoadedConfig.Session.OutputDevice
		}
		options.RTCBinding.InputPresent = true
		options.RTCBinding.OutputPresent = true
	}
	if request.BareLive {
		var err error
		options, err = ResolveBareSessionOptions(options)
		if err != nil {
			return SessionRunOptions{}, err
		}
	}
	if options.RTCRuntimeFactory == nil {
		options.RTCRuntimeFactory = d.deps.RuntimeFactory
	}
	if request.LoadedConfig != nil {
		options.LoadedConfig = applyToolVisibility(request.LoadedConfig, request.ComputerUse, request.ExperimentalTools, request.NoTerminalTools)
	}
	if d.deps.ToolService != nil {
		capabilities, err := d.resolveSessionToolCapabilities(request, options.LoadedConfig)
		if err != nil {
			return SessionRunOptions{}, err
		}
		if capabilities.Initialize != nil {
			if err := capabilities.Initialize(ctx); err != nil {
				if capabilities.Close != nil {
					_ = capabilities.Close()
				}
				return SessionRunOptions{}, fmt.Errorf("initialize session tools: %w", err)
			}
		}
		options.ToolExecutor = capabilities.Executor
		options.ToolDefinitions = append([]messages.ToolDefinition(nil), capabilities.Definitions...)
		options.ToolDefinitionBase = append([]messages.ToolDefinition(nil), capabilities.Definitions...)
		options.RefreshToolDefinitions = capabilities.RefreshDefinitionsWithError
		options.BrowserWatch, options.BrowserEventWatch = capabilities.BrowserWatch, capabilities.BrowserEventWatch
		options.BrowserCapabilityState, options.CapabilityClose = capabilities.BrowserCapabilityState, capabilities.Close
	}
	policy, err := cliTools.ResolveFilesystemPolicy(request.WorkDir, request.AllowPaths...)
	if err != nil {
		return SessionRunOptions{}, fmt.Errorf("resolve filesystem scope: %w", err)
	}
	options.FilesystemPolicy = policy
	options.WorkDir, options.AllowPaths = policy.PrimaryRoot(), policy.AdditionalRoots()
	if options.LoadedConfig != nil {
		copyCfg := *options.LoadedConfig
		copyCfg.FilesystemWorkDir, copyCfg.FilesystemAllowPaths = policy.PrimaryRoot(), policy.AdditionalRoots()
		options.LoadedConfig = &copyCfg
	}
	return options, nil
}

// resolveSessionToolCapabilities applies request-scoped filesystem values to
// a private config snapshot before asking the injected service for a surface.
// The config file is a policy source; resolving it before --workdir and
// --allow-path overrides would bind filesystem tools to the process cwd.
func (d *Dispatcher) resolveSessionToolCapabilities(request public.Request, cfg *config.Config) (serviceTools.Capabilities, error) {
	toolConfig := cfg
	if toolConfig == nil {
		toolConfig = &config.Config{}
	} else {
		copyConfig := *toolConfig
		copyConfig.Tools.List = append([]config.ToolEntry(nil), toolConfig.Tools.List...)
		copyConfig.FilesystemAllowPaths = append([]string(nil), toolConfig.FilesystemAllowPaths...)
		toolConfig = &copyConfig
	}
	toolConfig.FilesystemWorkDir = request.WorkDir
	toolConfig.FilesystemAllowPaths = append([]string(nil), request.AllowPaths...)
	capabilities, err := d.deps.ToolService.Resolve(toolConfig)
	if err != nil {
		return serviceTools.Capabilities{}, fmt.Errorf("configure session tools: %w", err)
	}
	return capabilities, nil
}

func applyToolVisibility(cfg *config.Config, computerUse, experimentalTools, noTerminalTools bool) *config.Config {
	if cfg == nil {
		cfg = &config.Config{}
	}
	copyCfg := *cfg
	copyCfg.Tools.List = append([]config.ToolEntry(nil), cfg.Tools.List...)
	set := func(id string, enabled bool) {
		for i := range copyCfg.Tools.List {
			if copyCfg.Tools.List[i].ID == id {
				copyCfg.Tools.List[i].Enabled = enabled
				return
			}
		}
		copyCfg.Tools.List = append(copyCfg.Tools.List, config.ToolEntry{ID: id, Enabled: enabled})
	}
	if !computerUse {
		set("show", false)
		set("mouse", false)
	}
	if !experimentalTools {
		for _, id := range []string{"load_skill", "sleep", "web_fetch", "web_search"} {
			set(id, false)
		}
	}
	if noTerminalTools {
		for _, id := range []string{"exec", "read_file", "read_image", "write_file", "edit_file", "append_file", "list_dir"} {
			set(id, false)
		}
	}
	return &copyCfg
}

func traceCredentials(r *public.Request) []string {
	values := []string{r.APIKey}
	if config := r.LoadedConfig; config != nil {
		if config.Model.OpenAI != nil {
			values = append(values, config.Model.OpenAI.APIKey)
		}
		if config.Model.Claude != nil {
			values = append(values, config.Model.Claude.APIKey)
		}
		if config.Model.OpenRouter != nil {
			values = append(values, config.Model.OpenRouter.APIKey)
		}
		if config.Model.Local != nil {
			values = append(values, config.Model.Local.APIKey)
		}
		if config.Model.Fal != nil {
			values = append(values, config.Model.Fal.APIKey)
		}
		if config.Model.Grok != nil {
			values = append(values, config.Model.Grok.APIKey)
		}
	}
	return values
}

func setTraceBinding(o *SessionRunOptions, b sessiontrace.DeviceBinding) {
	o.RTCBinding.PreGateSamplesObserver = runtimedevices.CaptureSamplesObserver(b.PreGateSamplesObserver)
	o.RTCBinding.UploadedSamplesObserver = runtimedevices.CaptureSamplesObserver(b.UploadedSamplesObserver)
	o.RTCBinding.PlaybackSamplesObserver = runtimedevices.PlaybackSamplesObserver(b.PlaybackSamplesObserver)
	o.RTCBinding.RenderedSamplesObserver = runtimedevices.RenderedSamplesObserver(b.RenderedSamplesObserver)
	o.RTCBinding.RenderedSamplesUnavailable = b.RenderedSamplesUnavailable
}
