package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	gwproviders "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/spf13/cobra"
)

const sessionCommandExample = "  yui session\n" +
	"  yui session --voice marin --model gpt-realtime-2.1\n" +
	"  yui session --record session.json\n" +
	"  yui session --browser-tools webmcp --browser-open https://example.com/"

// SetFeedbackWarningWriter exposes feedback-classification text only to
// diagnostics and tests. Production CLI sessions leave it nil and remain
// silent; feedback suppression itself is unaffected.
func (c *SessionCommand) SetFeedbackWarningWriter(writer io.Writer) {
	if c == nil {
		return
	}
	c.feedbackWarningWriter = writer
}

// SetHoldToneConfig installs an explicit local gap-cue policy for embedded
// command owners. Ordinary CLI construction leaves it nil and uses defaults.
func (c *SessionCommand) SetHoldToneConfig(config serviceSession.HoldToneConfig) {
	if c == nil {
		return
	}
	copy := config
	c.holdToneConfig = &copy
}

func decorateSessionCommandError(err error) error {
	if err == nil || strings.Contains(err.Error(), "classification=") {
		return err
	}
	classification := gwproviders.SessionErrorClassification("", "", err.Error())
	if classification != gwproviders.ErrorClassRateLimited {
		return err
	}
	return fmt.Errorf("[classification=%s]: %w", classification, err)
}

// sessionCommandRunState is the mutable flag snapshot used by one command
// invocation. Keeping it separate from cobra's command object lets the host
// admission path be tested and shaped independently from flag registration.
type sessionCommandRunState struct {
	Prompt               string
	Voice                string
	ReasoningEffort      string
	RecordDirectory      string
	AudioOutputPath      string
	TraceAudio           bool
	Transport            string
	Signaling            string
	MediaSource          string
	MaxDuration          time.Duration
	WaitForClose         bool
	NoInputTranscription bool
	ComputerUse          bool
	ExperimentalTools    bool
	NoTerminalTools      bool
	AudioInputPath       string
	AudioTurns           []string
	AudioTurnBarge       bool
	AudioInterrupts      []string
	AudioInterruptTool   string
	AudioInputDevice     devicegw.DeviceID
	AudioOutputDevice    devicegw.DeviceID
	AudioDeviceServer    string
	BrowserTools         string
	BrowserFlags         *flags.BrowserFlags
}

func (c *SessionCommand) runSessionCommand(cmd *cobra.Command, args []string, state sessionCommandRunState) (runErr error) {
	defer func() { runErr = decorateSessionCommandError(runErr) }()
	var endpointValidator runtimeDevices.RemoteEndpointValidator
	if validator, ok := c.deviceService.(runtimeDevices.RemoteEndpointValidator); ok {
		endpointValidator = validator
	}
	selectedTransport, err := validateSessionCommandPreflight(sessionCommandPreflight{
		cmd: cmd, browserTools: state.BrowserTools, transport: state.Transport,
		signaling: state.Signaling, mediaSource: state.MediaSource,
		audioInTurnBarge: state.AudioTurnBarge, audioInTurns: len(state.AudioTurns), audioDeviceServer: state.AudioDeviceServer, maxDuration: state.MaxDuration,
		deviceService: endpointValidator,
	})
	if err != nil {
		return err
	}
	hasSessionMode := sessionHasExplicitMode(cmd, args, c.imagePaths)
	browserFlags := state.BrowserFlags
	if browserFlags == nil {
		browserFlags = flags.NewBrowserFlags()
	}
	bareSession, loadedConfig, err := resolveSessionAdmission(c.globalFlags, cmd, browserFlags, args, hasSessionMode, c.imagePaths)
	if err != nil {
		return err
	}
	if !hasSessionMode && !browserToolsAdmission(cmd) && !bareSession {
		return cmd.Help()
	}
	passiveLive := isPassiveLiveInvocation(cmd, args, c.imagePaths)
	browserToolsInteractive := browserToolsAdmission(cmd) && (!hasSessionMode || passiveLive)
	sessionContext, stopSignal, cancellationIntent := newSessionSignalContext(cmd.Context())
	defer stopSignal()
	request, err := c.buildSessionRequest(cmd, args, state, selectedTransport, bareSession, passiveLive, browserToolsInteractive, loadedConfig, cancellationIntent)
	if err != nil {
		return err
	}
	return c.runSessionRequest(sessionContext, cmd.OutOrStdout(), cmd.ErrOrStderr(), request)
}

func (c *SessionCommand) buildSessionRequest(cmd *cobra.Command, args []string, state sessionCommandRunState, selectedTransport string, bareSession, passiveLive, browserToolsInteractive bool, loadedConfig *config.Config, cancellationIntent serviceSession.SessionCancellationIntent) (serviceSession.Request, error) {
	audioInput := sessionAudioInputFromCommand(cmd, state.AudioInputPath)
	if err := validateScheduledAudio(state, audioInput, c.askFlags.ReplayCapturePath); err != nil {
		return serviceSession.Request{}, err
	}
	if err := validateAudioInterrupt(state); err != nil {
		return serviceSession.Request{}, err
	}
	workDir := globalWorkDir(c.globalFlags)
	if workDir == "" && loadedConfig != nil {
		workDir = loadedConfig.FilesystemWorkDir
	}
	return serviceSession.Request{
		RecordPath: c.askFlags.RecordCapturePath, ReplayPath: c.askFlags.ReplayCapturePath, ReplayTiming: c.askFlags.ReplayTiming,
		Provider: c.askFlags.Provider, ProviderProvided: cmd.Flags().Changed("provider"), Model: c.askFlags.Model, ModelProvided: cmd.Flags().Changed("model"),
		NoInputTranscription: state.NoInputTranscription, APIKey: c.askFlags.APIKey, BaseURL: c.askFlags.BaseURL, ConfigDir: c.globalFlags.ConfigDir(),
		WorkDir: workDir, AllowPaths: globalAllowPaths(c.globalFlags), Prompt: strings.Join(args, " "),
		PromptProvided: cmd.Flags().Changed("prompt") || len(args) > 0, Voice: state.Voice, ReasoningEffort: state.ReasoningEffort,
		Transport: selectedTransport, TransportProvided: cmd.Flags().Changed("transport"), Signaling: state.Signaling, MediaSource: state.MediaSource,
		CancellationIntent: cancellationIntent, LoadedConfig: loadedConfig, BrowserToolsEnabled: !bareSession && browserConfigEnablesTools(loadedConfig), BareLive: bareSession || passiveLive,
		BrowserToolsInteractive: browserToolsInteractive, ToolDiagnostics: sessionToolDiagnosticSink(cmd.ErrOrStderr()),
		Diagnostics: sessionAudioDiagnosticSink(cmd.ErrOrStderr()), WaitForClose: state.WaitForClose || passiveLive, StreamObserver: c.streamObserver,
		AudioInTurnBarge: state.AudioTurnBarge, InteractiveDevices: browserToolsInteractive || passiveLive || bareSession,
		TraceAudio: state.TraceAudio, RecordDirectory: state.RecordDirectory, AudioOutputPath: state.AudioOutputPath, MaxDuration: state.MaxDuration, TextSeed: serviceSession.TextSeed{Value: state.Prompt, Present: cmd.Flags().Changed("prompt")},
		AudioInput: audioInput, AudioTurns: append([]string(nil), state.AudioTurns...), AudioInterrupts: append([]string(nil), state.AudioInterrupts...),
		AudioInterruptTool: state.AudioInterruptTool, AudioInputPacing: c.audioInPacing, SystemPrompt: c.askFlags.SystemPrompt, ImagePaths: append([]string(nil), c.imagePaths...),
		AudioInputDevice: string(state.AudioInputDevice), AudioOutputDevice: string(state.AudioOutputDevice), AudioInputDevicePresent: cmd.Flags().Changed("audio-in-device"), AudioOutputDevicePresent: cmd.Flags().Changed("audio-out-device"),
		AudioDeviceServer: state.AudioDeviceServer, HoldToneConfig: c.holdToneConfig, FeedbackWarningWriter: c.feedbackWarningWriter, ComputerUse: state.ComputerUse, ExperimentalTools: state.ExperimentalTools, NoTerminalTools: state.NoTerminalTools,
	}, nil
}

func validateScheduledAudio(state sessionCommandRunState, audioInput serviceSession.AudioInput, replayPath string) error {
	if len(state.AudioTurns) == 0 {
		return nil
	}
	if audioInput.Present || audioInput.DevicePresent {
		return fmt.Errorf("--audio-in and --audio-in-turn cannot be used together")
	}
	if state.RecordDirectory == "" && replayPath == "" {
		return fmt.Errorf("--audio-in-turn requires --record-dir")
	}
	return nil
}

func sessionAudioInputFromCommand(cmd *cobra.Command, path string) serviceSession.AudioInput {
	return serviceSession.AudioInput{
		Path:               path,
		Stdin:              cmd.InOrStdin(),
		CloseStdinOnCancel: path == "-",
		Present:            cmd.Flags().Changed("audio-in"),
		DevicePresent:      cmd.Flags().Lookup("audio-in-device") != nil && cmd.Flags().Changed("audio-in-device"),
	}
}

func validateAudioInterrupt(state sessionCommandRunState) error {
	if len(state.AudioInterrupts) == 0 && strings.TrimSpace(state.AudioInterruptTool) != "" {
		return fmt.Errorf("--audio-interrupt-on-tool requires --audio-interrupt")
	}
	return nil
}

func (c *SessionCommand) runSessionRequest(ctx context.Context, out, errOut io.Writer, request serviceSession.Request) error {
	if c.liveService == nil {
		return errors.New("session live service is not configured")
	}
	replayInspection, err := c.inspectSessionReplay(ctx, request)
	if err != nil {
		return err
	}
	if replaysTurnTranscript(request, replayInspection) {
		return c.runReplayTranscript(ctx, out, request, replayInspection)
	}
	return c.runRuntimeLiveSessionWithAnnouncements(ctx, out, errOut, request, replayInspection)
}

// NewSessionRequestService exposes the session command's live admission path
// through the request-level session contract used by probe owners. Requests
// without a loaded configuration resolve one from their config directory; a
// replay without a directory uses an empty in-memory snapshot and never
// creates host configuration as a side effect.
func NewSessionRequestService(command *SessionCommand) serviceSession.SessionService {
	return sessionRequestService{command: command}
}

type sessionRequestService struct{ command *SessionCommand }

func (s sessionRequestService) Run(ctx context.Context, out io.Writer, request serviceSession.Request) error {
	if s.command == nil {
		return errors.New("session command is not configured")
	}
	if out == nil {
		return errors.New("session output is required")
	}
	if request.LoadedConfig == nil {
		loaded, err := sessionRequestConfig(request)
		if err != nil {
			return err
		}
		request.LoadedConfig = loaded
	}
	return s.command.runSessionRequest(ctx, out, out, request)
}

func sessionRequestConfig(request serviceSession.Request) (*config.Config, error) {
	if strings.TrimSpace(request.ReplayPath) != "" && strings.TrimSpace(request.ConfigDir) == "" {
		return &config.Config{}, nil
	}
	storage, err := config.NewDefaultConfigStorage(request.ConfigDir)
	if err != nil {
		return nil, fmt.Errorf("load session config: %w", err)
	}
	loaded, err := storage.Load()
	if err != nil {
		return nil, fmt.Errorf("load session config: %w", err)
	}
	return loaded, nil
}

// initializeRuntimeLiveCapabilities completes capability initialization
// before the live request is composed. The browser state observed after
// initialization grounds the provider instructions, so the provider never
// receives browser tools whose connection or selection state is still
// unknown. A failed initialization closes the capability it constructed.
func initializeRuntimeLiveCapabilities(ctx context.Context, capabilities *SessionToolCapabilities) error {
	if capabilities.Initialize != nil {
		if err := capabilities.Initialize(ctx); err != nil {
			if capabilities.Close != nil {
				err = errors.Join(err, capabilities.Close())
			}
			return fmt.Errorf("initialize session tools: %w", err)
		}
		capabilities.Initialize = nil
	}
	if capabilities.Status != nil {
		if state := capabilities.Status().BrowserCapabilityState; state != "" {
			capabilities.BrowserCapabilityState = state
		}
	}
	return nil
}

// sessionAudioInPacingFlag is the hidden test-harness option that selects how
// finite file audio inputs (--audio-in, --audio-in-turn, --audio-interrupt)
// are paced. The default is the production real-time cadence; hermetic tests
// against a scripted provider may accelerate ("20x") or disable ("unpaced")
// it. Pacing never changes which samples are sent, their order, or turn
// boundaries.
const sessionAudioInPacingFlag = "audio-in-pacing"

// filePacingFlag binds a runtime FilePacing to a pflag value through its
// text encoding ("realtime", "unpaced", "20x").
type filePacingFlag struct {
	target *runtimeDevices.FilePacing
}

func (f *filePacingFlag) String() string {
	if f.target == nil {
		return runtimeDevices.FilePacing{}.String()
	}
	return f.target.String()
}

func (f *filePacingFlag) Set(value string) error {
	var pacing runtimeDevices.FilePacing
	if err := pacing.UnmarshalText([]byte(value)); err != nil {
		return err
	}
	*f.target = pacing
	return nil
}

func (f *filePacingFlag) Type() string { return "pacing" }

// registerSessionPacingFlag registers the hidden --audio-in-pacing option.
// MarkHidden fails only for an unknown name, a programming error.
func registerSessionPacingFlag(cmd *cobra.Command, target *runtimeDevices.FilePacing) {
	cmd.Flags().Var(&filePacingFlag{target: target}, sessionAudioInPacingFlag, "Test harness: deliver finite file audio inputs at realtime (default), unpaced, or a speed multiplier such as 20x")
	if err := cmd.Flags().MarkHidden(sessionAudioInPacingFlag); err != nil {
		panic(err)
	}
}
