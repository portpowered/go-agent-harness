package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
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
	selectedTransport, err := validateSessionCommandPreflight(sessionCommandPreflight{
		cmd: cmd, browserTools: state.BrowserTools, transport: state.Transport,
		signaling: state.Signaling, mediaSource: state.MediaSource,
		audioInTurnBarge: state.AudioTurnBarge, audioInTurns: len(state.AudioTurns), maxDuration: state.MaxDuration,
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
	return c.runSessionRequest(sessionContext, cmd.OutOrStdout(), request)
}

func (c *SessionCommand) buildSessionRequest(cmd *cobra.Command, args []string, state sessionCommandRunState, selectedTransport string, bareSession, passiveLive, browserToolsInteractive bool, loadedConfig *config.Config, cancellationIntent *serviceSession.SessionCancellationIntent) (serviceSession.Request, error) {
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
		AudioInterruptTool: state.AudioInterruptTool, SystemPrompt: c.askFlags.SystemPrompt, ImagePaths: append([]string(nil), c.imagePaths...),
		AudioInputDevice: string(state.AudioInputDevice), AudioOutputDevice: string(state.AudioOutputDevice), AudioInputDevicePresent: cmd.Flags().Changed("audio-in-device"), AudioOutputDevicePresent: cmd.Flags().Changed("audio-out-device"),
		AudioDeviceServer: state.AudioDeviceServer, FeedbackWarningWriter: c.feedbackWarningWriter, ComputerUse: state.ComputerUse, ExperimentalTools: state.ExperimentalTools, NoTerminalTools: state.NoTerminalTools,
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

func (c *SessionCommand) runSessionRequest(ctx context.Context, out io.Writer, request serviceSession.Request) error {
	useLive, replayInspection, err := c.runtimeLiveAdmission(ctx, request)
	if err != nil {
		return err
	}
	if useLive {
		return c.runRuntimeLiveSession(ctx, out, request, replayInspection)
	}
	if c.sessionService == nil {
		return fmt.Errorf("session service is not configured")
	}
	return c.sessionService.Run(ctx, out, request)
}
