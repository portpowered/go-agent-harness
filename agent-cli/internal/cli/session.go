package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/session"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	platformclock "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/platform/clock"
	"github.com/spf13/cobra"
)

// SessionToolCapabilities is the config-scoped tool surface used by a
// composed session command. Executor and Definitions are derived from the
// same loaded config snapshot so the session cannot advertise a tool that its
// executor does not expose.
type SessionToolCapabilities struct {
	Executor    messages.ToolExecutor
	Definitions []messages.ToolDefinition
}

// SessionToolCapabilitiesFactory builds the session tool surface from the
// config selected by --config-dir. It is optional so direct command
// constructors and callers that intentionally inject a no-tools session keep
// their existing behavior.
type SessionToolCapabilitiesFactory func(*config.Config) (SessionToolCapabilities, error)

const (
	// SessionTransportWebSocket is the default session transport. Keeping the
	// value explicit makes the command's default part of the public contract.
	SessionTransportWebSocket = "ws"
	// SessionTransportWebRTC selects the WebRTC session transport.
	SessionTransportWebRTC = "webrtc"
)

// ErrInvalidSessionTransport identifies a session --transport value that the
// CLI does not understand.
var ErrInvalidSessionTransport = errors.New("invalid session transport")

// ErrSessionWebRTCUnavailable identifies the deliberately deferred customer
// WebRTC path. The CLI still accepts and validates the transport flags so
// their specific errors remain useful, but it must not start a session until
// customer-reachable signaling and spoken-audio input are both wired.
var ErrSessionWebRTCUnavailable = errors.New("WebRTC session customer path is unavailable")

// SessionWebRTCUnavailableError explains why an otherwise valid WebRTC CLI
// selection cannot be started yet. It unwraps to a stable sentinel so callers
// can classify the capability failure without matching customer-facing text.
type SessionWebRTCUnavailableError struct{}

func (e *SessionWebRTCUnavailableError) Error() string {
	if e == nil {
		return ErrSessionWebRTCUnavailable.Error()
	}
	return "WebRTC CLI path is not yet customer-usable: no customer-reachable network signaling implementation and no supported spoken-audio input path are wired; use --transport ws with --audio-in or --audio-in-device"
}

func (*SessionWebRTCUnavailableError) Unwrap() error { return ErrSessionWebRTCUnavailable }

// SessionTransportError describes an invalid --transport value before any
// session provider or transport is initialized.
type SessionTransportError struct {
	Value string
}

func (e *SessionTransportError) Error() string {
	if e == nil {
		return ErrInvalidSessionTransport.Error()
	}
	return fmt.Sprintf("--transport must be one of %q or %q, got %q", SessionTransportWebSocket, SessionTransportWebRTC, e.Value)
}

func (e *SessionTransportError) Unwrap() error { return ErrInvalidSessionTransport }

// SessionSignalingError identifies an invalid relationship between the
// --transport and --signaling flags before session setup begins.
type SessionSignalingError struct {
	Transport string
	Missing   bool
}

func (e *SessionSignalingError) Error() string {
	if e == nil {
		return ErrSessionSignalingRequiresWebRTC.Error()
	}
	if e.Missing {
		return fmt.Sprintf("--transport %q requires --signaling <endpoint>; provide both --transport and --signaling", e.Transport)
	}
	return fmt.Sprintf("--signaling requires --transport %q; got --transport %q (the --signaling and --transport flags are incompatible)", SessionTransportWebRTC, e.Transport)
}

func (e *SessionSignalingError) Unwrap() error {
	if e != nil && e.Missing {
		return ErrSessionWebRTCRequiresSignaling
	}
	return ErrSessionSignalingRequiresWebRTC
}

// SessionMediaSourceError describes an invalid relationship between the
// --media-source, --audio-in, and --transport flags before session setup.
type SessionMediaSourceError struct {
	Transport string
	Source    string
	AudioIn   bool
	Empty     bool
}

func (e *SessionMediaSourceError) Error() string {
	if e == nil {
		return ErrSessionMediaSourceRequiresWebRTC.Error()
	}
	if e.AudioIn {
		return "--media-source cannot be combined with --audio-in; provide only one audio input (the --media-source and --audio-in flags are incompatible)"
	}
	if e.Empty {
		return "--media-source requires a non-empty URL; provide --media-source <url>"
	}
	return fmt.Sprintf("--media-source requires --transport %q; got --transport %q (the --media-source and --transport flags are incompatible)", SessionTransportWebRTC, e.Transport)
}

func (e *SessionMediaSourceError) Unwrap() error {
	if e != nil && e.AudioIn {
		return ErrSessionMediaSourceConflictsWithAudioIn
	}
	if e != nil && e.Empty {
		return ErrSessionMediaSourceEmpty
	}
	return ErrSessionMediaSourceRequiresWebRTC
}

// ErrSessionSignalingRequiresWebRTC identifies --signaling used without the
// WebRTC transport.
var ErrSessionSignalingRequiresWebRTC = errors.New("session signaling requires WebRTC transport")

// ErrSessionWebRTCRequiresSignaling identifies WebRTC selected without an
// endpoint for its signaling exchange.
var ErrSessionWebRTCRequiresSignaling = errors.New("WebRTC session transport requires signaling")

// ErrSessionMediaSourceRequiresWebRTC identifies --media-source used without
// the WebRTC transport.
var ErrSessionMediaSourceRequiresWebRTC = errors.New("session media source requires WebRTC transport")

// ErrSessionMediaSourceConflictsWithAudioIn identifies --media-source used
// together with --audio-in.
var ErrSessionMediaSourceConflictsWithAudioIn = errors.New("session media source conflicts with audio input")

// ErrSessionMediaSourceEmpty identifies an explicitly provided empty
// --media-source value.
var ErrSessionMediaSourceEmpty = errors.New("session media source is empty")

func validateSessionTransport(raw string) (string, error) {
	transport := strings.ToLower(strings.TrimSpace(raw))
	switch transport {
	case SessionTransportWebSocket, SessionTransportWebRTC:
		return transport, nil
	default:
		return "", &SessionTransportError{Value: raw}
	}
}

// SessionCommand is the session group (parent command); subcommands are wired in routes.go.
type SessionCommand struct {
	askFlags                  *flags.AskFlags
	globalFlags               *flags.GlobalFlags
	toolExecutorOverride      messages.ToolExecutor
	sessionInferencerOverride messages.SessionInferencer
	sessionToolCapabilities   SessionToolCapabilitiesFactory
	streamObserver            services.SessionStreamObserver
	rtcRuntimeFactory         services.SessionRTCRuntimeFactory
	clockSource               platformclock.Source
	runtimeObserver           services.SessionRuntimeObserver
	deviceRegistry            audio.DeviceRegistry
	imagePaths                []string
}

// sessionVoiceFlagValue validates the public voice flag while Cobra parses
// arguments. The service package remains the single owner of the accepted
// voice set and validation error identity.
type sessionVoiceFlagValue struct {
	target *string
	err    error
}

func (v *sessionVoiceFlagValue) String() string {
	if v == nil || v.target == nil {
		return ""
	}
	return *v.target
}

func (v *sessionVoiceFlagValue) Set(value string) error {
	if err := services.ValidateOpenAIRealtimeVoice(value); err != nil {
		v.err = err
		return err
	}
	v.err = nil
	*v.target = value
	return nil
}

func (*sessionVoiceFlagValue) Type() string { return "string" }

// NewSessionCommand returns the session group command constructor. The tool
// executor is the single composed instance from the wire graph (the same value
// given to agent.NewExecutor); callers without one pass nil so session runs
// keep their no-tools behavior.
func NewSessionCommand(askFlags *flags.AskFlags, globalFlags *flags.GlobalFlags, toolExecutorOverride messages.ToolExecutor, sessionInferencerOverride messages.SessionInferencer) *SessionCommand {
	return NewSessionCommandWithRuntimeAndDeviceRegistryAndToolCapabilities(askFlags, globalFlags, toolExecutorOverride, sessionInferencerOverride, nil, nil, nil, nil)
}

// NewSessionCommandWithRuntime constructs the session command with the
// composed clock and optional runtime observation sink. The legacy constructor
// above remains for callers that do not need runtime evidence.
func NewSessionCommandWithRuntime(
	askFlags *flags.AskFlags,
	globalFlags *flags.GlobalFlags,
	toolExecutorOverride messages.ToolExecutor,
	sessionInferencerOverride messages.SessionInferencer,
	clockSource platformclock.Source,
	runtimeObserver services.SessionRuntimeObserver,
) *SessionCommand {
	return NewSessionCommandWithRuntimeAndDeviceRegistryAndToolCapabilities(askFlags, globalFlags, toolExecutorOverride, sessionInferencerOverride, clockSource, runtimeObserver, nil, nil)
}

// NewSessionCommandWithRuntimeAndToolCapabilities constructs the session
// command with the composed clock, runtime observer, and optional config-aware
// session tool capability factory.
func NewSessionCommandWithRuntimeAndToolCapabilities(
	askFlags *flags.AskFlags,
	globalFlags *flags.GlobalFlags,
	toolExecutorOverride messages.ToolExecutor,
	sessionInferencerOverride messages.SessionInferencer,
	clockSource platformclock.Source,
	runtimeObserver services.SessionRuntimeObserver,
	sessionToolCapabilities SessionToolCapabilitiesFactory,
) *SessionCommand {
	return NewSessionCommandWithRuntimeAndDeviceRegistryAndToolCapabilities(askFlags, globalFlags, toolExecutorOverride, sessionInferencerOverride, clockSource, runtimeObserver, sessionToolCapabilities, nil)
}

// NewSessionCommandWithDeviceRegistry constructs the session command with the
// application-owned registry used by RTC device preflight. The four-argument
// constructor above remains the compatibility path for callers whose wire
// graph does not yet expose a concrete registry.
func NewSessionCommandWithDeviceRegistry(askFlags *flags.AskFlags, globalFlags *flags.GlobalFlags, toolExecutorOverride messages.ToolExecutor, sessionInferencerOverride messages.SessionInferencer, deviceRegistry audio.DeviceRegistry) *SessionCommand {
	return NewSessionCommandWithRuntimeAndDeviceRegistry(askFlags, globalFlags, toolExecutorOverride, sessionInferencerOverride, nil, nil, deviceRegistry)
}

// NewSessionCommandWithRuntimeAndDeviceRegistry composes the optional runtime
// observation seams and the shared audio registry into one command graph.
func NewSessionCommandWithRuntimeAndDeviceRegistry(
	askFlags *flags.AskFlags,
	globalFlags *flags.GlobalFlags,
	toolExecutorOverride messages.ToolExecutor,
	sessionInferencerOverride messages.SessionInferencer,
	clockSource platformclock.Source,
	runtimeObserver services.SessionRuntimeObserver,
	deviceRegistry audio.DeviceRegistry,
) *SessionCommand {
	return NewSessionCommandWithRuntimeAndDeviceRegistryAndToolCapabilities(askFlags, globalFlags, toolExecutorOverride, sessionInferencerOverride, clockSource, runtimeObserver, nil, deviceRegistry)
}

// NewSessionCommandWithRuntimeAndDeviceRegistryAndToolCapabilities composes
// the optional runtime observation, config-aware tool capability, and shared
// audio registry seams into one command graph.
func NewSessionCommandWithRuntimeAndDeviceRegistryAndToolCapabilities(
	askFlags *flags.AskFlags,
	globalFlags *flags.GlobalFlags,
	toolExecutorOverride messages.ToolExecutor,
	sessionInferencerOverride messages.SessionInferencer,
	clockSource platformclock.Source,
	runtimeObserver services.SessionRuntimeObserver,
	sessionToolCapabilities SessionToolCapabilitiesFactory,
	deviceRegistry audio.DeviceRegistry,
) *SessionCommand {
	return NewSessionCommandWithRuntimeAndDeviceRegistryAndToolCapabilitiesAndRTCRuntime(
		askFlags,
		globalFlags,
		toolExecutorOverride,
		sessionInferencerOverride,
		clockSource,
		runtimeObserver,
		sessionToolCapabilities,
		deviceRegistry,
		nil,
	)
}

// NewSessionCommandWithRuntimeAndDeviceRegistryAndToolCapabilitiesAndRTCRuntime
// is the production composition constructor. The generated Wire graph passes
// the concrete runtime factory directly, so a shipped WebRTC command cannot
// silently lose it between graph assembly and option generation.
func NewSessionCommandWithRuntimeAndDeviceRegistryAndToolCapabilitiesAndRTCRuntime(
	askFlags *flags.AskFlags,
	globalFlags *flags.GlobalFlags,
	toolExecutorOverride messages.ToolExecutor,
	sessionInferencerOverride messages.SessionInferencer,
	clockSource platformclock.Source,
	runtimeObserver services.SessionRuntimeObserver,
	sessionToolCapabilities SessionToolCapabilitiesFactory,
	deviceRegistry audio.DeviceRegistry,
	rtcRuntimeFactory services.SessionRTCRuntimeFactory,
) *SessionCommand {
	return &SessionCommand{
		askFlags:                  askFlags,
		globalFlags:               globalFlags,
		toolExecutorOverride:      toolExecutorOverride,
		sessionInferencerOverride: sessionInferencerOverride,
		sessionToolCapabilities:   sessionToolCapabilities,
		rtcRuntimeFactory:         rtcRuntimeFactory,
		clockSource:               clockSource,
		runtimeObserver:           runtimeObserver,
		deviceRegistry:            deviceRegistry,
	}
}

// SetSessionStreamObserver adds an optional observer for deltas consumed by a
// session loop. It is primarily useful to verify emitted tool-result streams
// through the CLI composition root without changing normal command output.
func (c *SessionCommand) SetSessionStreamObserver(observer services.SessionStreamObserver) {
	if c == nil {
		return
	}
	c.streamObserver = observer
}

// SetDeviceRegistry injects the registry used by session RTC device
// preflight. It is primarily useful to the application wire graph and to
// deterministic callers that provide a virtual registry.
func (c *SessionCommand) SetDeviceRegistry(registry audio.DeviceRegistry) {
	c.deviceRegistry = registry
}

// SetSessionRTCRuntimeFactory supplies the protocol-owning WebRTC runtime to
// the session command. The production graph installs this through the
// constructor above; the setter remains for compatibility with embedding and
// focused command tests that replace the runtime seam.
func (c *SessionCommand) SetSessionRTCRuntimeFactory(factory services.SessionRTCRuntimeFactory) {
	if c == nil {
		return
	}
	c.rtcRuntimeFactory = factory
}

// Generate returns the cobra command for the session group.
func (c *SessionCommand) Generate() *cobra.Command {
	var prompt string
	var voice string
	recordDirPath := ""
	audioOutPath := ""
	transport := SessionTransportWebSocket
	signaling := ""
	mediaSource := ""
	var maxDuration time.Duration
	var waitForClose bool
	var audioIn string
	var audioInTurns []string
	var audioInDevice audio.DeviceID
	var audioOutDevice audio.DeviceID
	voiceFlag := &sessionVoiceFlagValue{target: &voice}
	cmd := &cobra.Command{
		Use:   "session [message]",
		Short: "Run or manage agent sessions",
		Long: "Run a bidirectional session inference capture or replay a session capture file.\n" +
			"Use --record <file>.json to capture live session traffic, --record-dir <dir> for a complete both-side recording directory, or --replay <file>.json to replay a saved capture without live provider network calls.\n" +
			"Use repeatable audio-in-turn paths with record-dir to replay multiple finite spoken turns through one persistent session.\n\n" +
			"WebRTC customer availability is deferred and currently unavailable: --transport webrtc, --signaling, and --media-source are reserved for a future customer-reachable network signaling and spoken-audio implementation. The current CLI has only in-process loopback signaling and no WebRTC spoken-audio input wiring, so a valid WebRTC selection returns an actionable error before session setup. For file, stdin, or microphone speech input, use the supported --transport ws path with --audio-in or --audio-in-device.\n\n" +
			"Session history management remains available through the show, list, and delete subcommands.",
		Args: cobra.ArbitraryArgs,
		PreRunE: func(_ *cobra.Command, _ []string) error {
			return services.ValidateOpenAIRealtimeVoice(voice)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			selectedTransport, err := validateSessionTransport(transport)
			if err != nil {
				return err
			}
			if err := validateSessionSignaling(selectedTransport, signaling, cmd.Flags().Changed("signaling")); err != nil {
				return err
			}
			if err := validateSessionMediaSource(selectedTransport, mediaSource, cmd.Flags().Changed("media-source"), cmd.Flags().Changed("audio-in")); err != nil {
				return err
			}
			if err := services.ValidateSessionAudioDeviceConflicts(
				cmd.Flags().Changed("audio-in"),
				cmd.Flags().Changed("audio-out"),
				cmd.Flags().Changed(services.SessionAudioInDeviceFlag),
				cmd.Flags().Changed(services.SessionAudioOutDeviceFlag),
			); err != nil {
				return err
			}
			if err := services.ValidateSessionMaxDuration(maxDuration); err != nil {
				return err
			}
			if selectedTransport == SessionTransportWebRTC {
				return &SessionWebRTCUnavailableError{}
			}
			if c.askFlags.RecordCapturePath == "" && c.askFlags.ReplayCapturePath == "" && recordDirPath == "" && len(audioInTurns) == 0 {
				return cmd.Help()
			}
			sessionContext := cmd.Context()
			if maxDuration > 0 {
				capturePath := c.askFlags.RecordCapturePath
				if capturePath == "" {
					capturePath = c.askFlags.ReplayCapturePath
				}
				if capturePath != "" {
					artifactBase := strings.TrimSuffix(capturePath, filepath.Ext(capturePath))
					sessionContext = services.WithSessionDurationArtifactPaths(sessionContext, services.SessionDurationArtifactPaths{
						AudioPath:      artifactBase + ".wav",
						TranscriptPath: artifactBase + ".jsonl",
					})
				}
			}
			toolExecutor := c.toolExecutorOverride
			var toolDefinitions []messages.ToolDefinition
			var loadedConfig *config.Config
			if c.sessionToolCapabilities != nil {
				storage, err := config.NewDefaultConfigStorage(c.globalFlags.ConfigDir())
				if err != nil {
					return fmt.Errorf("load session config: %w", err)
				}
				loadedConfig, err = storage.Load()
				if err != nil {
					return fmt.Errorf("load session config: %w", err)
				}
				capabilities, err := c.sessionToolCapabilities(loadedConfig)
				if err != nil {
					return fmt.Errorf("configure session tools: %w", err)
				}
				toolExecutor = capabilities.Executor
				toolDefinitions = append([]messages.ToolDefinition(nil), capabilities.Definitions...)
			}
			sessionOptions := services.SessionRunOptions{
				RecordPath:        c.askFlags.RecordCapturePath,
				ReplayPath:        c.askFlags.ReplayCapturePath,
				Provider:          c.askFlags.Provider,
				Model:             c.askFlags.Model,
				ModelProvided:     cmd.Flags().Changed("model"),
				APIKey:            c.askFlags.APIKey,
				BaseURL:           c.askFlags.BaseURL,
				ConfigDir:         c.globalFlags.ConfigDir(),
				Prompt:            strings.Join(args, " "),
				Voice:             voice,
				Transport:         selectedTransport,
				Signaling:         signaling,
				MediaSource:       mediaSource,
				RTCRuntimeFactory: c.rtcRuntimeFactory,
				SessionInferencer: c.sessionInferencerOverride,
				ToolExecutor:      toolExecutor,
				ToolDefinitions:   toolDefinitions,
				LoadedConfig:      loadedConfig,
				WaitForClose:      waitForClose,
				StreamObserver:    c.streamObserver,
				Clock:             c.clockSource,
				RuntimeObserver:   c.runtimeObserver,
				RTCDeviceBinding: services.RTCDeviceBindingRequest{
					Registry:      c.deviceRegistry,
					InputDevice:   audioInDevice,
					OutputDevice:  audioOutDevice,
					InputPresent:  cmd.Flags().Changed(services.SessionAudioInDeviceFlag),
					OutputPresent: cmd.Flags().Changed(services.SessionAudioOutDeviceFlag),
				},
			}
			seed := services.SessionTextSeed{
				Value:   prompt,
				Present: cmd.Flags().Changed("prompt"),
			}
			audioInput := services.SessionAudioInput{
				Path:          audioIn,
				Stdin:         cmd.InOrStdin(),
				Present:       cmd.Flags().Changed("audio-in"),
				DevicePresent: cmd.Flags().Lookup("audio-in-device") != nil && cmd.Flags().Changed("audio-in-device"),
			}
			if len(audioInTurns) > 0 {
				if audioInput.Present || audioInput.DevicePresent {
					return fmt.Errorf("--audio-in and --audio-in-turn cannot be used together")
				}
				if recordDirPath == "" {
					return fmt.Errorf("--audio-in-turn requires --record-dir")
				}
				if len(c.imagePaths) > 0 {
					return fmt.Errorf("--audio-in-turn cannot be combined with --image")
				}
				return services.RunSessionWithRecordingDirectoryAndInstructionsAndAudioFilesAndOutputAndTextSeedAndMaxDuration(
					sessionContext,
					cmd.OutOrStdout(),
					sessionOptions,
					recordDirPath,
					audioOutPath,
					maxDuration,
					seed,
					audioInTurns,
					c.askFlags.SystemPrompt,
				)
			}
			if len(c.imagePaths) > 0 {
				if recordDirPath != "" {
					if audioInput.Present {
						return services.RunSessionWithImagesAndRecordingDirectoryAndAudioInput(sessionContext, cmd.OutOrStdout(), services.SessionImageRunOptions{
							SessionRunOptions: sessionOptions,
							ImagePaths:        append([]string(nil), c.imagePaths...),
							AudioOutPath:      audioOutPath,
							MaxDuration:       maxDuration,
							TextSeed:          seed,
							SystemPrompt:      c.askFlags.SystemPrompt,
						}, recordDirPath, audioInput)
					}
					return services.RunSessionWithImagesAndRecordingDirectory(sessionContext, cmd.OutOrStdout(), services.SessionImageRunOptions{
						SessionRunOptions: sessionOptions,
						ImagePaths:        append([]string(nil), c.imagePaths...),
						AudioOutPath:      audioOutPath,
						MaxDuration:       maxDuration,
						TextSeed:          seed,
						SystemPrompt:      c.askFlags.SystemPrompt,
					}, recordDirPath)
				}
				if audioInput.Present {
					return services.RunSessionWithImagesAndAudioInput(sessionContext, cmd.OutOrStdout(), services.SessionImageRunOptions{
						SessionRunOptions: sessionOptions,
						ImagePaths:        append([]string(nil), c.imagePaths...),
						AudioOutPath:      audioOutPath,
						MaxDuration:       maxDuration,
						TextSeed:          seed,
						SystemPrompt:      c.askFlags.SystemPrompt,
					}, audioInput)
				}
				return services.RunSessionWithImages(sessionContext, cmd.OutOrStdout(), services.SessionImageRunOptions{
					SessionRunOptions: sessionOptions,
					ImagePaths:        append([]string(nil), c.imagePaths...),
					AudioOutPath:      audioOutPath,
					MaxDuration:       maxDuration,
					TextSeed:          seed,
					SystemPrompt:      c.askFlags.SystemPrompt,
				})
			}
			if audioInput.Present {
				if recordDirPath != "" {
					return services.RunSessionWithRecordingDirectoryAndInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration(sessionContext, cmd.OutOrStdout(), sessionOptions, recordDirPath, audioOutPath, maxDuration, seed, audioInput, c.askFlags.SystemPrompt)
				}
				return services.RunSessionWithInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration(sessionContext, cmd.OutOrStdout(), sessionOptions, audioOutPath, maxDuration, seed, audioInput, c.askFlags.SystemPrompt)
			}
			if recordDirPath != "" {
				return services.RunSessionWithRecordingDirectoryAndInstructionsAndAudioOutAndTextSeedAndMaxDuration(sessionContext, cmd.OutOrStdout(), sessionOptions, recordDirPath, audioOutPath, maxDuration, seed, c.askFlags.SystemPrompt)
			}
			return services.RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration(sessionContext, cmd.OutOrStdout(), sessionOptions, audioOutPath, maxDuration, seed, c.askFlags.SystemPrompt)
		},
	}
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		if voiceFlag.err != nil {
			return voiceFlag.err
		}
		return err
	})
	cmd.Flags().StringVar(&c.askFlags.RecordCapturePath, "record", "", "Record bidirectional session traffic to a JSON capture file")
	cmd.Flags().StringVar(&recordDirPath, "record-dir", "", "Record a complete both-side session directory separately from --record")
	cmd.Flags().StringVar(&c.askFlags.ReplayCapturePath, "replay", "", "Replay bidirectional session traffic from a JSON capture file without live provider network calls")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Seed the realtime session with text")
	cmd.Flags().StringVar(&c.askFlags.SystemPrompt, "system-prompt", "", "Path to system prompt file or literal text")
	cmd.Flags().StringVar(&c.askFlags.Provider, "provider", "", "Session provider ID (use grok or openai for live record mode)")
	cmd.Flags().DurationVar(&maxDuration, "max-duration", 0, "Maximum session duration as a Go duration; exits cleanly when the bound is reached")
	cmd.Flags().BoolVar(&waitForClose, "wait-for-close", false, "Keep the session running after a completed response until the provider closes it")
	cmd.Flags().StringVar(&c.askFlags.Model, "model", "", "Session model ID for live record mode")
	cmd.Flags().Var(voiceFlag, "voice", fmt.Sprintf("OpenAI Realtime audio output voice (supported: %s)", strings.Join(services.SupportedOpenAIRealtimeVoices(), ", ")))
	cmd.Flags().StringVar(&c.askFlags.APIKey, "api-key", "", "Session provider API key for live record mode")
	cmd.Flags().StringVar(&audioIn, "audio-in", "", "Stream a .wav/.pcm/.raw file incrementally; use - for raw PCM16 standard input")
	cmd.Flags().StringArrayVar(&audioInTurns, "audio-in-turn", nil, "Add a finite .wav/.pcm/.raw spoken turn to one persistent --record-dir session (repeatable)")
	cmd.Flags().StringVar(&audioInDevice, services.SessionAudioInDeviceFlag, "", "Capture RTC audio from a registry device ID; empty or default selects the input default")
	cmd.Flags().StringVar(&audioOutPath, "audio-out", "", "Write assistant PCM16 audio to a .wav/.pcm/.raw path or - for stdout")
	cmd.Flags().StringVar(&audioOutDevice, services.SessionAudioOutDeviceFlag, "", "Play RTC audio to a registry device ID; empty or default selects the output default")
	cmd.Flags().StringVar(&c.askFlags.BaseURL, "base-url", "", "Session provider base URL override")
	cmd.Flags().StringArrayVar(&c.imagePaths, "image", nil, "Attach a local image to the realtime user turn (repeatable; order is preserved)")
	cmd.Flags().StringVar(&mediaSource, "media-source", "", "Deferred/unavailable WebRTC receive-only external media source; requires --transport webrtc and cannot be combined with --audio-in")
	cmd.Flags().StringVar(&transport, "transport", SessionTransportWebSocket, "Session transport: ws (default, supported) or webrtc (deferred/unavailable customer path)")
	cmd.Flags().StringVar(&signaling, "signaling", "", "Deferred/unavailable WebRTC signaling endpoint; customer-reachable network signaling is not wired yet; requires --transport webrtc, and --transport webrtc requires this flag")
	cmd.AddCommand(NewSessionSelfPlayCommand(c.globalFlags).Generate())
	return cmd
}

func validateSessionSignaling(transport, signaling string, provided bool) error {
	if provided && transport != SessionTransportWebRTC {
		return &SessionSignalingError{Transport: transport}
	}
	if transport == SessionTransportWebRTC && strings.TrimSpace(signaling) == "" {
		return &SessionSignalingError{Transport: transport, Missing: true}
	}
	return nil
}

func validateSessionMediaSource(transport, source string, provided, audioInProvided bool) error {
	if !provided {
		return nil
	}
	if audioInProvided {
		return &SessionMediaSourceError{Transport: transport, Source: source, AudioIn: true}
	}
	if transport != SessionTransportWebRTC {
		return &SessionMediaSourceError{Transport: transport, Source: source}
	}
	if strings.TrimSpace(source) == "" {
		return &SessionMediaSourceError{Transport: transport, Source: source, Empty: true}
	}
	return nil
}

// getSessionStorage resolves workspace from global flags and returns session storage.
func getSessionStorage(globalFlags *flags.GlobalFlags) (*session.Storage, error) {
	workspaceDir := globalFlags.ConfigDir()
	if workspaceDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("get workspace dir: %w", err)
		}
		workspaceDir = filepath.Join(home, config.ConfigDirName)
	}
	workspaceDir, err := filepath.Abs(workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("get workspace dir: %w", err)
	}
	return session.NewStorage(workspaceDir), nil
}

// SessionShowCommand wraps the session show subcommand.
type SessionShowCommand struct {
	flags *flags.GlobalFlags
}

// NewSessionShowCommand creates the SessionShowCommand with the given flags.
func NewSessionShowCommand(flags *flags.GlobalFlags) *SessionShowCommand {
	return &SessionShowCommand{flags: flags}
}

// Generate returns the cobra command for session show.
func (c *SessionShowCommand) Generate() *cobra.Command {
	return &cobra.Command{
		Use:   "show <session-id>",
		Short: "Show a session by ID",
		Long:  "Load and print the conversation history for the given session.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			storage, err := getSessionStorage(c.flags)
			if err != nil {
				return err
			}
			sessionID := args[0]
			msgs, err := storage.Load(sessionID)
			if err != nil {
				return err
			}
			if msgs == nil {
				return fmt.Errorf("session %s not found", sessionID)
			}
			return writeSessionTo(cmd.OutOrStdout(), sessionID, msgs)
		},
	}
}

func writeSessionTo(w io.Writer, sessionID string, msgs []messages.Message) error {
	if _, err := fmt.Fprintf(w, "Session: %s\n", sessionID); err != nil {
		return err
	}
	for _, m := range msgs {
		role := string(m.Role)
		text := m.TextContent()
		if _, err := fmt.Fprintf(w, "[%s] %s\n", role, text); err != nil {
			return err
		}
	}
	return nil
}

// SessionListCommand wraps the session list subcommand.
type SessionListCommand struct {
	flags *flags.GlobalFlags
}

// NewSessionListCommand creates the SessionListCommand with the given flags.
func NewSessionListCommand(flags *flags.GlobalFlags) *SessionListCommand {
	return &SessionListCommand{flags: flags}
}

// Generate returns the cobra command for session list.
func (c *SessionListCommand) Generate() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all sessions",
		Long:  "List session IDs with last modified time, newest first.",
		RunE: func(cmd *cobra.Command, args []string) error {
			storage, err := getSessionStorage(c.flags)
			if err != nil {
				return err
			}
			infos, err := storage.List()
			if err != nil {
				return err
			}
			if len(infos) == 0 {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "No sessions found.")
				return err
			}
			for _, info := range infos {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s  %s\n", info.ID, info.ModTime.Format(time.RFC3339)); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// SessionDeleteCommand wraps the session delete subcommand.
type SessionDeleteCommand struct {
	flags *flags.GlobalFlags
}

// NewSessionDeleteCommand creates the SessionDeleteCommand with the given flags.
func NewSessionDeleteCommand(flags *flags.GlobalFlags) *SessionDeleteCommand {
	return &SessionDeleteCommand{flags: flags}
}

// Generate returns the cobra command for session delete.
func (c *SessionDeleteCommand) Generate() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <session-id>",
		Short: "Delete a session by ID",
		Long:  "Remove the session file. Use session list to see IDs.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			storage, err := getSessionStorage(c.flags)
			if err != nil {
				return err
			}
			sessionID := args[0]
			if err := storage.Delete(sessionID); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Deleted session %s\n", sessionID); err != nil {
				return err
			}
			return nil
		},
	}
}
