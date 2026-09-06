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
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/observability"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

var _ contract.Runtime = (*Dispatcher)(nil)

type Dependencies struct {
	Clock             clock.Source
	PlanFactory       sessionRuntimeFactory
	ToolService       serviceTools.Service
	RuntimeFactory    SessionRTCRuntimeFactory
	SessionInferencer messages.SessionInferencer
	ToolExecutor      messages.ToolExecutor
	DeviceRegistry    devicegw.DeviceRegistry
	RuntimeObserver   SessionRuntimeObserver
	Observability     observability.Dependencies
}

type Dispatcher struct{ deps Dependencies }

func New(deps Dependencies) *Dispatcher { return &Dispatcher{deps: deps} }

func audioInput(input public.AudioInput) SessionAudioInput {
	return SessionAudioInput{Path: input.Path, Stdin: input.Stdin, SourceSampleRate: input.SourceSampleRate, CloseStdinOnCancel: input.CloseStdinOnCancel, MaxDuration: input.MaxDuration, Present: input.Present, DevicePresent: input.DevicePresent}
}

func textSeed(seed public.TextSeed) SessionTextSeed {
	return SessionTextSeed{Value: seed.Value, Present: seed.Present}
}

func (d *Dispatcher) Run(ctx context.Context, out io.Writer, request public.Request) (runErr error) {
	if d == nil || d.deps.Clock == nil {
		return errors.New("session clock is required")
	}
	if request.MaxDuration > 0 {
		capturePath := request.RecordPath
		if capturePath == "" {
			capturePath = request.ReplayPath
		}
		if capturePath != "" {
			artifactBase := strings.TrimSuffix(capturePath, filepath.Ext(capturePath))
			ctx = WithSessionDurationArtifactPaths(ctx, SessionDurationArtifactPaths{AudioPath: artifactBase + ".wav", TranscriptPath: artifactBase + ".jsonl"})
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
	if len(request.AudioTurns) > 0 {
		if len(request.ImagePaths) > 0 {
			return RunSessionWithImagesAndRecordingDirectoryAndAudioFilesAndOutputAndTextSeedAndMaxDuration(
				ctx, out, SessionImageRunOptions{
					SessionRunOptions: options,
					ImagePaths:        append([]string(nil), request.ImagePaths...),
				}, request.RecordDirectory, request.AudioOutputPath, request.MaxDuration,
				textSeed(request.TextSeed), request.AudioTurns, request.SystemPrompt,
			)
		}
		return RunSessionWithRecordingDirectoryAndInstructionsAndAudioFilesAndOutputAndTextSeedAndMaxDuration(
			ctx, out, options, request.RecordDirectory, request.AudioOutputPath,
			request.MaxDuration, textSeed(request.TextSeed), request.AudioTurns, request.SystemPrompt,
		)
	}
	if len(request.ImagePaths) > 0 {
		if request.RecordDirectory != "" {
			if request.AudioInput.Present {
				return RunSessionWithImagesAndRecordingDirectoryAndAudioInput(
					ctx, out, SessionImageRunOptions{
						SessionRunOptions: options,
						ImagePaths:        append([]string(nil), request.ImagePaths...),
						AudioOutPath:      request.AudioOutputPath,
						MaxDuration:       request.MaxDuration,
						TextSeed:          textSeed(request.TextSeed),
						SystemPrompt:      request.SystemPrompt,
					}, request.RecordDirectory, audioInput(request.AudioInput),
				)
			}
			return RunSessionWithImagesAndRecordingDirectory(
				ctx, out, SessionImageRunOptions{
					SessionRunOptions: options,
					ImagePaths:        append([]string(nil), request.ImagePaths...),
					AudioOutPath:      request.AudioOutputPath,
					MaxDuration:       request.MaxDuration,
					TextSeed:          textSeed(request.TextSeed),
					SystemPrompt:      request.SystemPrompt,
				}, request.RecordDirectory,
			)
		}
		if request.AudioInput.Present {
			return RunSessionWithImagesAndAudioInput(
				ctx, out, SessionImageRunOptions{
					SessionRunOptions: options,
					ImagePaths:        append([]string(nil), request.ImagePaths...),
					AudioOutPath:      request.AudioOutputPath,
					MaxDuration:       request.MaxDuration,
					TextSeed:          textSeed(request.TextSeed),
					SystemPrompt:      request.SystemPrompt,
				}, audioInput(request.AudioInput),
			)
		}
		return RunSessionWithImages(ctx, out, SessionImageRunOptions{
			SessionRunOptions: options,
			ImagePaths:        append([]string(nil), request.ImagePaths...),
			AudioOutPath:      request.AudioOutputPath,
			MaxDuration:       request.MaxDuration,
			TextSeed:          textSeed(request.TextSeed),
			SystemPrompt:      request.SystemPrompt,
		})
	}
	if request.AudioInput.Present {
		if request.RecordDirectory != "" {
			return RunSessionWithRecordingDirectoryAndInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration(
				ctx, out, options, request.RecordDirectory, request.AudioOutputPath,
				request.MaxDuration, textSeed(request.TextSeed), audioInput(request.AudioInput), request.SystemPrompt,
			)
		}
		return RunSessionWithInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration(
			ctx, out, options, request.AudioOutputPath, request.MaxDuration,
			textSeed(request.TextSeed), audioInput(request.AudioInput), request.SystemPrompt,
		)
	}
	if request.RecordDirectory != "" {
		return RunSessionWithRecordingDirectoryAndInstructionsAndAudioOutAndTextSeedAndMaxDuration(
			ctx, out, options, request.RecordDirectory, request.AudioOutputPath,
			request.MaxDuration, textSeed(request.TextSeed), request.SystemPrompt,
		)
	}
	return RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration(
		ctx, out, options, request.AudioOutputPath, request.MaxDuration,
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
		RuntimeObserver: d.deps.RuntimeObserver, Diagnostics: request.Diagnostics, ToolDiagnostics: request.ToolDiagnostics,
		Observability: d.deps.Observability, StreamObserver: request.StreamObserver,
		AudioInTurnBarge: request.AudioInTurnBarge, ClientOwnsAudioTurnBoundaries: request.ClientOwnsAudioTurnBoundaries,
		SessionUpdatedTimeout: request.SessionUpdatedTimeout, WaitForClose: request.WaitForClose,
		runtimeFactory: d.deps.PlanFactory,
	}
	if err := validateSessionCaptureOptions(options); err != nil {
		return SessionRunOptions{}, err
	}
	// Populate the request's device selection before bare-session preflight so
	// that the resolver can apply persisted defaults without acquiring a
	// registry or provider.  Bare sessions must fail with their typed
	// credential error before any external setup begins.
	options.RTCDeviceBinding.InputDevice = devicegw.DeviceID(request.AudioInputDevice)
	options.RTCDeviceBinding.OutputDevice = devicegw.DeviceID(request.AudioOutputDevice)
	options.RTCDeviceBinding.InputPresent = request.AudioInputDevicePresent
	options.RTCDeviceBinding.OutputPresent = request.AudioOutputDevicePresent
	options.RTCDeviceBinding.FeedbackWarningWriter = request.FeedbackWarningWriter
	options.RTCDeviceBinding.Observability = d.deps.Observability
	if request.InteractiveDevices {
		if !options.RTCDeviceBinding.InputPresent && options.RTCDeviceBinding.InputDevice == "" && request.LoadedConfig != nil && request.LoadedConfig.Session != nil {
			options.RTCDeviceBinding.InputDevice = devicegw.DeviceID(request.LoadedConfig.Session.InputDevice)
		}
		if !options.RTCDeviceBinding.OutputPresent && options.RTCDeviceBinding.OutputDevice == "" && request.LoadedConfig != nil && request.LoadedConfig.Session != nil {
			options.RTCDeviceBinding.OutputDevice = devicegw.DeviceID(request.LoadedConfig.Session.OutputDevice)
		}
		options.RTCDeviceBinding.InputPresent = true
		options.RTCDeviceBinding.OutputPresent = true
	}
	if options.RTCDeviceBinding.Registry == nil {
		options.RTCDeviceBinding.Registry = d.deps.DeviceRegistry
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
	if request.AudioDeviceServer != "" {
		registry, err := devicegw.NewRemoteDeviceRegistry(request.AudioDeviceServer)
		if err != nil {
			return SessionRunOptions{}, fmt.Errorf("connect audio device server: %w", err)
		}
		options.RTCDeviceBinding.Registry = registry
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
	if len(request.AudioInterrupts) > 0 {
		if options.BrowserWatch == nil {
			if options.CapabilityClose != nil {
				_ = options.CapabilityClose()
			}
			return SessionRunOptions{}, errors.New("--audio-interrupt requires an enabled WebMCP session capability")
		}
		inputs, err := PrepareSessionAudioInputs(request.AudioInterrupts)
		if err != nil {
			if options.CapabilityClose != nil {
				_ = options.CapabilityClose()
			}
			return SessionRunOptions{}, fmt.Errorf("prepare --audio-interrupt: %w", err)
		}
		interruptions, _ := StartSessionAudioInterruptionsOnBrowserTool(ctx, options.BrowserWatch(ctx), request.AudioInterruptTool, inputs)
		options.AudioInterruptions = interruptions
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
