package cli

import (
	"context"
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
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
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
	// BrowserCapabilityState is the session-owned browser state used to
	// compose model-facing grounding. It is independent from whether the
	// current definition snapshot happens to contain first-class page tools.
	BrowserCapabilityState webmcp.BrowserCapabilityState
	// DisplayCapability is the immutable host-surface admission result used
	// to derive both display-dependent definitions and executor routes.
	DisplayCapability SessionDisplayCapability
	// RefreshDefinitions returns the final definition list after Initialize
	// has run: the composed static and stable broker definitions plus any
	// first-class page tools read from the connected browser catalog. Nil
	// means Definitions is already final.
	RefreshDefinitions func(context.Context) []messages.ToolDefinition
	// RefreshDefinitionsWithError is the error-preserving form used by the
	// live session publisher. A catalog read failure must not be collapsed into
	// an empty page surface and treated as a successful provider update.
	RefreshDefinitionsWithError func(context.Context) ([]messages.ToolDefinition, error)
	// Initialize is called synchronously after capability construction and
	// before the session provider can issue a browser tool call. Implementations
	// retain a classified failed state in the executor when initialization
	// returns an error, so a stale selection is observable instead of becoming
	// an identity-free selection_not_connected result.
	Initialize func(context.Context) error
	// Status reports the explicit lifecycle state of an optional capability.
	Status func() SessionCapabilityStatus
	// BrowserWatch exposes the already-owned broker observation stream to an
	// opt-in live session input boundary. It is nil for non-browser capability
	// sets; callers must use the returned context to stop the watch.
	BrowserWatch func(context.Context) <-chan webmcp.BrokerEvent
	// BrowserEventWatch exposes the richer adapter-owned semantic browser event
	// stream to the opt-in recording observer. It is independent from
	// BrowserWatch and never participates in tool execution or continuation.
	BrowserEventWatch func(context.Context) <-chan webmcp.BrowserEvent
	// Close transfers ownership of any capability resources to the session
	// coordinator. Nil means this capability has no closeable resources.
	Close func() error
}

type resolvedSessionToolSurface struct {
	executor          messages.ToolExecutor
	definitions       []messages.ToolDefinition
	base              []messages.ToolDefinition
	browserState      webmcp.BrowserCapabilityState
	refresh           func(context.Context) ([]messages.ToolDefinition, error)
	browserWatch      func(context.Context) <-chan webmcp.BrokerEvent
	browserEventWatch func(context.Context) <-chan webmcp.BrowserEvent
	capabilityClose   func() error
}

func sessionToolDiagnosticSink(out io.Writer) services.SessionToolDiagnosticSink {
	return services.SessionToolDiagnosticFunc(func(diagnostic services.SessionToolDiagnostic) {
		if diagnostic.Error == nil {
			return
		}
		_, _ = fmt.Fprintf(out, "tool diagnostic: tool=%q call_id=%q source=%q error_code=%q detail=%s\n", diagnostic.ToolName, diagnostic.ToolCallID, diagnostic.Source, diagnostic.ErrorCode, diagnostic.Error)
	})
}

func (c *SessionCommand) sessionRTCDeviceBinding(cmd *cobra.Command, input, output audio.DeviceID) services.RTCDeviceBindingRequest {
	return services.RTCDeviceBindingRequest{
		Registry:      c.deviceRegistry,
		InputDevice:   input,
		OutputDevice:  output,
		InputPresent:  cmd.Flags().Changed(services.SessionAudioInDeviceFlag),
		OutputPresent: cmd.Flags().Changed(services.SessionAudioOutDeviceFlag),
	}
}

func resolveSessionToolSurface(ctx context.Context, capabilities SessionToolCapabilities) resolvedSessionToolSurface {
	if capabilities.Initialize != nil {
		// Initialization is deliberately completed before the provider receives
		// the executor. A failed initializer remains represented by the broker's
		// classified failed state and is surfaced by later tool calls.
		_ = capabilities.Initialize(ctx)
	}
	result := resolvedSessionToolSurface{
		executor:          capabilities.Executor,
		definitions:       append([]messages.ToolDefinition(nil), capabilities.Definitions...),
		base:              append([]messages.ToolDefinition(nil), capabilities.Definitions...),
		browserState:      capabilities.BrowserCapabilityState,
		refresh:           capabilities.RefreshDefinitionsWithError,
		browserWatch:      capabilities.BrowserWatch,
		browserEventWatch: capabilities.BrowserEventWatch,
	}
	if capabilities.Status != nil {
		status := capabilities.Status()
		if status.BrowserCapabilityState != "" {
			result.browserState = status.BrowserCapabilityState
		}
	}
	if result.refresh == nil && capabilities.RefreshDefinitions != nil {
		result.refresh = func(ctx context.Context) ([]messages.ToolDefinition, error) {
			return capabilities.RefreshDefinitions(ctx), nil
		}
	}
	if capabilities.RefreshDefinitions != nil {
		result.definitions = capabilities.RefreshDefinitions(ctx)
	}
	if capabilities.Close != nil {
		// Ownership transfers to the service coordinator after capability
		// construction succeeds.
		result.capabilityClose = capabilities.Close
	}
	return result
}

// SessionCapabilityState is the lifecycle state of a request-scoped session
// capability. Browser capabilities begin initializing, then become ready or
// retain a classified failure for every subsequent tool call.
type SessionCapabilityState string

const (
	SessionCapabilityInitializing SessionCapabilityState = "initializing"
	SessionCapabilityReady        SessionCapabilityState = "ready"
	SessionCapabilityFailed       SessionCapabilityState = "failed"
)

// SessionCapabilityStatus is a read-only snapshot of capability setup.
type SessionCapabilityStatus struct {
	State                  SessionCapabilityState
	Err                    error
	BrowserCapabilityState webmcp.BrowserCapabilityState
}

// SessionCapabilityInitializer is the optional lifecycle seam exposed by a
// browser-backed capability set. It is deliberately separate from the frozen
// messages.ToolExecutor interface.
type SessionCapabilityInitializer interface {
	InitializeSession(context.Context) error
	SessionCapabilityStatus() SessionCapabilityStatus
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
// sessionCommandLongHelp is the session command long help, hoisted out of
// Generate to keep the constructor within the function-length gate.
const sessionCommandLongHelp = "Run a bidirectional session inference capture or replay a session capture file.\n" +
	"Use --record <file>.json to capture live session traffic, --record-dir <dir> for a complete both-side recording directory, or --replay <file>.json to replay a saved capture without live provider network calls.\n" +
	"With no capture, prompt, file-audio, scheduled-turn, image, or browser flags, bare `agent session` starts a live OpenAI Realtime voice session over WebSocket on the default microphone and speakers; use --provider, --model, --api-key, --voice, or --audio-in-device/--audio-out-device to override its live defaults.\n" +
	"Use repeatable finite spoken-turn inputs with --record-dir to replay multiple turns through one persistent session; scheduled turns are completion-gated by default. The optional scheduled barge mode releases each later turn against its identified active, non-terminal prior response. Ordinary scheduled turns do not interrupt responses.\n\n" +
	"WebMCP browser sessions: use --browser-tools webmcp without --browser-cdp-url or --browser-ws-endpoint for an agent-managed local Chrome; no CDP port is required. Supplying either endpoint keeps the externally managed browser path, and the agent never closes an external browser.\n\n" +
	"WebRTC customer availability is deferred and currently unavailable: --transport webrtc, --signaling, and --media-source are reserved for a future customer-reachable network signaling and spoken-audio implementation. The current CLI has only in-process loopback signaling and no WebRTC spoken-audio input wiring, so a valid WebRTC selection returns an actionable error before session setup. For file, stdin, or microphone speech input, use the supported --transport ws path with its file/stdin or device audio-input options.\n\n" +
	"Input transcription is enabled by default only for live OpenAI sessions that accept audio input; use --no-input-transcription to opt out. Replay always follows its recorded session.update handshake.\n\n" +
	"Session history management remains available through the show, list, and delete subcommands.\n\n" +
	filesystemPolicyHelp

// sessionModeFlagNames lists the non-browser flags whose presence names an
// explicit session action. Pure browser flags are deliberately excluded: they
// keep their own dedicated non-admission contract via hasSessionBrowserFlag
// and browserToolsAdmission, tested by TestSessionBrowserNonAdmissionReturnsHelpWithoutSetup.
var sessionModeFlagNames = []string{
	"record",
	"record-dir",
	"replay",
	"prompt",
	"system-prompt",
	"audio-in",
	"audio-out",
	"audio-in-turn",
	"audio-in-turn-barge",
	"audio-interrupt",
	"audio-interrupt-on-tool",
	"image",
}

// sessionHasExplicitMode reports whether the invocation names a concrete
// session action: positional prompt words, an attached image, or one of
// sessionModeFlagNames.
//
// This is the fix for "session --prompt exits 0 and prints a help dump
// instead of doing the work": the gate below used to treat only a narrow
// subset of these flags (record/replay/record-dir/audio-in-turn/
// audio-interrupt) as "has session mode", so --prompt, --audio-in,
// --system-prompt, --audio-out, positional args, and other entries in this
// list fell through to cmd.Help() with a nil error — including
// --audio-in pointing at a file that does not exist, since the file-open
// validation that would have reported it never ran. Folding the complete
// list in here routes those invocations to real validation instead (e.g.
// "agent session requires --record or --replay", or the actual --audio-in
// file-not-found error).
func sessionHasExplicitMode(cmd *cobra.Command, args []string, imagePaths []string) bool {
	if len(args) > 0 || len(imagePaths) > 0 {
		return true
	}
	if cmd == nil {
		return false
	}
	for _, name := range sessionModeFlagNames {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

func isBareSessionInvocation(cmd *cobra.Command, args []string, hasSessionMode bool, imagePaths []string) bool {
	if cmd == nil || hasSessionMode || len(args) > 0 || len(imagePaths) > 0 || hasSessionBrowserFlag(cmd) {
		return false
	}
	for _, name := range sessionModeFlagNames {
		if cmd.Flags().Changed(name) {
			return false
		}
	}
	return true
}

func resolveSessionAdmission(globalFlags *flags.GlobalFlags, cmd *cobra.Command, browserFlags *flags.BrowserFlags, args []string, hasSessionMode bool, imagePaths []string) (bool, *config.Config, error) {
	bareSession := isBareSessionInvocation(cmd, args, hasSessionMode, imagePaths)
	var loadedConfig *config.Config
	if hasSessionMode || hasSessionBrowserFlag(cmd) || bareSession {
		var err error
		loadedConfig, err = resolveSessionBrowserConfig(globalFlags, cmd, browserFlags)
		if err != nil {
			return false, nil, err
		}
	}
	// Browser configuration is intentionally not a standalone admission
	// trigger. Preserve that contract when a persisted config enables the
	// browser capability: an otherwise empty invocation still prints help
	// instead of silently starting a non-browser bare session.
	if bareSession && browserConfigEnablesTools(loadedConfig) {
		bareSession = false
	}
	return bareSession, loadedConfig, nil
}

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
	var noInputTranscription bool
	var audioIn string
	var audioInTurns []string
	var audioInTurnBarge bool
	var audioInterrupts []string
	var audioInterruptTool string
	var audioInDevice audio.DeviceID
	var audioOutDevice audio.DeviceID
	browserFlags := flags.NewBrowserFlags()
	voiceFlag := &sessionVoiceFlagValue{target: &voice}
	cmd := &cobra.Command{
		Use:          "session [message]",
		Short:        "Run or manage agent sessions",
		Long:         sessionCommandLongHelp,
		Args:         cobra.ArbitraryArgs,
		SilenceUsage: true,
		PreRunE: func(_ *cobra.Command, _ []string) error {
			return services.ValidateOpenAIRealtimeVoice(voice)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := services.ValidateSessionAudioInTurnBarge(audioInTurnBarge, len(audioInTurns)); err != nil {
				return err
			}
			if err := validateBrowserToolsBackend(browserFlags.Tools, browserToolsAdmission(cmd)); err != nil {
				return err
			}
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
			filesystemPolicy, err := cliTools.ResolveFilesystemPolicy(
				globalWorkDir(c.globalFlags),
				globalAllowPaths(c.globalFlags)...,
			)
			if err != nil {
				return fmt.Errorf("resolve filesystem scope: %w", err)
			}
			hasSessionMode := sessionHasExplicitMode(cmd, args, c.imagePaths)
			bareSession, loadedConfig, err := resolveSessionAdmission(c.globalFlags, cmd, browserFlags, args, hasSessionMode, c.imagePaths)
			if err != nil {
				return err
			}
			if !hasSessionMode && !browserToolsAdmission(cmd) && !bareSession {
				return cmd.Help()
			}
			loadedConfig = withFilesystemPolicyMetadata(loadedConfig, filesystemPolicy)
			sessionContext, stopSignal, cancellationIntent := newSessionSignalContext(cmd.Context())
			defer stopSignal()
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
			seed := services.SessionTextSeed{
				Value:   prompt,
				Present: cmd.Flags().Changed("prompt"),
			}
			audioInput := sessionAudioInputFromCommand(cmd, audioIn)
			// Validate command-only combinations before browser setup; ownership transfers to the service coordinator after construction.
			if len(audioInTurns) > 0 {
				if audioInput.Present || audioInput.DevicePresent {
					return fmt.Errorf("--audio-in and --audio-in-turn cannot be used together")
				}
				// A replay drives its scheduled audio turns from the recorded
				// capture rather than a live provider; --record-dir observes a
				// live recording and is not required to replay one.
				if recordDirPath == "" && c.askFlags.ReplayCapturePath == "" {
					return fmt.Errorf("--audio-in-turn requires --record-dir")
				}
			}
			if len(audioInterrupts) == 0 && strings.TrimSpace(audioInterruptTool) != "" {
				return fmt.Errorf("--audio-interrupt-on-tool requires --audio-interrupt")
			}
			toolExecutor := c.toolExecutorOverride
			var toolDefinitions []messages.ToolDefinition
			var toolDefinitionBase []messages.ToolDefinition
			var refreshDefinitionsWithError func(context.Context) ([]messages.ToolDefinition, error)
			var capabilityClose func() error
			var browserWatch func(context.Context) <-chan webmcp.BrokerEvent
			var browserEventWatch func(context.Context) <-chan webmcp.BrowserEvent
			browserCapabilityState := webmcp.BrowserCapabilityDisabled
			if c.sessionToolCapabilities != nil && !bareSession {
				if loadedConfig == nil {
					loadedConfig, err = resolveSessionBrowserConfig(c.globalFlags, cmd, browserFlags)
					if err != nil {
						return err
					}
					loadedConfig = withFilesystemPolicyMetadata(loadedConfig, filesystemPolicy)
				}
				capabilities, err := c.sessionToolCapabilities(loadedConfig)
				if err != nil {
					return fmt.Errorf("configure session tools: %w", err)
				}
				surface := resolveSessionToolSurface(sessionContext, capabilities)
				toolExecutor = surface.executor
				toolDefinitions = surface.definitions
				toolDefinitionBase = surface.base
				refreshDefinitionsWithError = surface.refresh
				browserWatch, browserEventWatch = surface.browserWatch, surface.browserEventWatch
				if surface.browserState != "" {
					browserCapabilityState = surface.browserState
				}
				capabilityClose = surface.capabilityClose
			}
			toolExecutor = cliTools.ApplyFilesystemPolicy(toolExecutor, filesystemPolicy)
			audioInterruptions, capabilityClose, err := prepareSessionAudioInterruptions(cmd, audioInterrupts, audioInterruptTool, browserWatch, capabilityClose)
			if err != nil {
				return err
			}
			sessionOptions := services.SessionRunOptions{
				RecordPath:             c.askFlags.RecordCapturePath,
				ReplayPath:             c.askFlags.ReplayCapturePath,
				Provider:               c.askFlags.Provider,
				ProviderProvided:       cmd.Flags().Changed("provider"),
				Model:                  c.askFlags.Model,
				ModelProvided:          cmd.Flags().Changed("model"),
				NoInputTranscription:   noInputTranscription,
				APIKey:                 c.askFlags.APIKey,
				BaseURL:                c.askFlags.BaseURL,
				ConfigDir:              c.globalFlags.ConfigDir(),
				WorkDir:                filesystemPolicy.PrimaryRoot(),
				AllowPaths:             filesystemPolicy.AdditionalRoots(),
				FilesystemPolicy:       filesystemPolicy,
				Prompt:                 strings.Join(args, " "),
				PromptProvided:         cmd.Flags().Changed("prompt") || len(args) > 0,
				Voice:                  voice,
				Transport:              selectedTransport,
				TransportProvided:      cmd.Flags().Changed("transport"),
				Signaling:              signaling,
				MediaSource:            mediaSource,
				RTCRuntimeFactory:      c.rtcRuntimeFactory,
				SessionInferencer:      c.sessionInferencerOverride,
				ToolExecutor:           toolExecutor,
				ToolDefinitions:        toolDefinitions,
				ToolDefinitionBase:     toolDefinitionBase,
				RefreshToolDefinitions: refreshDefinitionsWithError,
				BrowserWatch:           browserWatch,
				BrowserEventWatch:      browserEventWatch,
				BrowserCapabilityState: browserCapabilityState,
				AudioInterruptions:     audioInterruptions,
				CapabilityClose:        capabilityClose,
				CancellationIntent:     cancellationIntent,
				LoadedConfig:           loadedConfig,
				BrowserToolsEnabled:    !bareSession && browserConfigEnablesTools(loadedConfig),
				ToolDiagnostics:        sessionToolDiagnosticSink(cmd.ErrOrStderr()),
				WaitForClose:           waitForClose,
				StreamObserver:         c.streamObserver,
				Clock:                  c.clockSource,
				RuntimeObserver:        c.runtimeObserver,
				AudioInTurnBarge:       audioInTurnBarge,
				RTCDeviceBinding:       c.sessionRTCDeviceBinding(cmd, audioInDevice, audioOutDevice),
			}
			if bareSession {
				sessionOptions, err = services.ResolveBareSessionOptions(sessionOptions)
				if err != nil {
					return err
				}
			}
			if len(audioInTurns) > 0 {
				if len(c.imagePaths) > 0 {
					return services.RunSessionWithImagesAndRecordingDirectoryAndAudioFilesAndOutputAndTextSeedAndMaxDuration(
						sessionContext,
						cmd.OutOrStdout(),
						services.SessionImageRunOptions{
							SessionRunOptions: sessionOptions,
							ImagePaths:        append([]string(nil), c.imagePaths...),
						},
						recordDirPath,
						audioOutPath,
						maxDuration,
						seed,
						audioInTurns,
						c.askFlags.SystemPrompt,
					)
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
	setSessionFlagErrorFunc(cmd, voiceFlag)
	cmd.Flags().StringVar(&c.askFlags.RecordCapturePath, "record", "", "Record bidirectional session traffic to a JSON capture file")
	cmd.Flags().StringVar(&recordDirPath, "record-dir", "", "Record a complete both-side session directory separately from --record")
	cmd.Flags().StringVar(&c.askFlags.ReplayCapturePath, "replay", "", "Replay bidirectional session traffic from a JSON capture file without live provider network calls")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Seed the realtime session with text")
	cmd.Flags().StringVar(&c.askFlags.SystemPrompt, "system-prompt", "", "Path to system prompt file or literal text")
	cmd.Flags().StringVar(&c.askFlags.Provider, "provider", "", "Session provider ID (use grok or openai for live record mode)")
	cmd.Flags().DurationVar(&maxDuration, "max-duration", 0, "Maximum session duration as a Go duration; exits cleanly when the bound is reached")
	cmd.Flags().BoolVar(&waitForClose, "wait-for-close", false, "Keep the session running after a completed response until the provider closes it")
	cmd.Flags().StringVar(&c.askFlags.Model, "model", "", "Session model ID for live record mode")
	cmd.Flags().BoolVar(&noInputTranscription, "no-input-transcription", false, "Disable customer-speech transcription for live OpenAI audio-input sessions")
	cmd.Flags().Var(voiceFlag, "voice", fmt.Sprintf("OpenAI Realtime audio output voice (supported: %s)", strings.Join(services.SupportedOpenAIRealtimeVoices(), ", ")))
	cmd.Flags().StringVar(&c.askFlags.APIKey, "api-key", "", "Session provider API key for live record mode")
	cmd.Flags().StringVar(&audioIn, "audio-in", "", "Stream a .wav/.pcm/.raw file incrementally; use - for raw PCM16 standard input")
	cmd.Flags().StringArrayVar(&audioInTurns, "audio-in-turn", nil, "Add a finite .wav/.pcm/.raw spoken turn to one persistent --record-dir session (repeatable)")
	cmd.Flags().BoolVar(&audioInTurnBarge, "audio-in-turn-barge", false, "Release later --audio-in-turn inputs against an active prior response; without this flag scheduled turns remain completion-gated")
	cmd.Flags().StringArrayVar(&audioInterrupts, "audio-interrupt", nil, "Release finite .wav/.pcm/.raw audio after the first observed in-flight WebMCP invocation (repeatable; live browser sessions only)")
	cmd.Flags().StringVar(&audioInterruptTool, "audio-interrupt-on-tool", "", "With --audio-interrupt, wait for this WebMCP tool's in-flight invocation instead of the first one")
	cmd.Flags().StringVar(&audioInDevice, services.SessionAudioInDeviceFlag, "", "Capture RTC audio from a registry device ID; empty or default selects the input default")
	cmd.Flags().StringVar(&audioOutPath, "audio-out", "", "Write assistant PCM16 audio to a .wav/.pcm/.raw path or - for stdout")
	cmd.Flags().StringVar(&audioOutDevice, services.SessionAudioOutDeviceFlag, "", "Play RTC audio to a registry device ID; empty or default selects the output default")
	cmd.Flags().StringVar(&c.askFlags.BaseURL, "base-url", "", "Session provider base URL override")
	cmd.Flags().StringArrayVar(&c.imagePaths, "image", nil, "Attach a local image to the realtime user turn (repeatable; order is preserved)")
	cmd.Flags().StringVar(&mediaSource, "media-source", "", "Deferred/unavailable WebRTC receive-only external media source; requires --transport webrtc and cannot be combined with --audio-in")
	cmd.Flags().StringVar(&transport, "transport", SessionTransportWebSocket, "Session transport: ws (default, supported) or webrtc (deferred/unavailable customer path)")
	cmd.Flags().StringVar(&signaling, "signaling", "", "Deferred/unavailable WebRTC signaling endpoint; customer-reachable network signaling is not wired yet; requires --transport webrtc, and --transport webrtc requires this flag")
	registerSessionBrowserFlags(cmd, browserFlags)
	cmd.AddCommand(NewSessionSelfPlayCommand(c.globalFlags).Generate())
	return cmd
}

func setSessionFlagErrorFunc(cmd *cobra.Command, voiceFlag *sessionVoiceFlagValue) {
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		if voiceFlag.err != nil {
			return voiceFlag.err
		}
		return err
	})
}

func prepareSessionAudioInterruptions(
	cmd *cobra.Command,
	audioInterrupts []string,
	audioInterruptTool string,
	browserWatch func(context.Context) <-chan webmcp.BrokerEvent,
	capabilityClose func() error,
) (<-chan services.ScheduledAudioInput, func() error, error) {
	if len(audioInterrupts) == 0 {
		return nil, capabilityClose, nil
	}
	if browserWatch == nil {
		if capabilityClose != nil {
			_ = capabilityClose()
		}
		return nil, nil, errors.New("--audio-interrupt requires an enabled WebMCP session capability")
	}
	interruptInputs, err := services.PrepareSessionAudioInputs(audioInterrupts)
	if err != nil {
		if capabilityClose != nil {
			_ = capabilityClose()
		}
		return nil, nil, fmt.Errorf("prepare --audio-interrupt: %w", err)
	}
	interruptContext, stopInterruptions := context.WithCancel(cmd.Context())
	interruptions, _ := services.StartSessionAudioInterruptionsOnBrowserTool(
		interruptContext,
		browserWatch(interruptContext),
		audioInterruptTool,
		interruptInputs,
	)
	previousClose := capabilityClose
	return interruptions, func() error {
		stopInterruptions()
		if previousClose != nil {
			return previousClose()
		}
		return nil
	}, nil
}

func sessionAudioInputFromCommand(cmd *cobra.Command, path string) services.SessionAudioInput {
	return services.SessionAudioInput{
		Path:               path,
		Stdin:              cmd.InOrStdin(),
		CloseStdinOnCancel: path == "-",
		Present:            cmd.Flags().Changed("audio-in"),
		DevicePresent:      cmd.Flags().Lookup("audio-in-device") != nil && cmd.Flags().Changed("audio-in-device"),
	}
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
	configDir := globalFlags.ConfigDir()
	workspaceDir := configDir
	if workspaceDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("get workspace dir: %w", err)
		}
		configDir = filepath.Join(home, config.ConfigDirName)
		workspaceDir = configDir
	}
	configDir, err := filepath.Abs(configDir)
	if err != nil {
		return nil, fmt.Errorf("get config dir: %w", err)
	}
	return session.NewStorageWithWorkspace(configDir, workspaceDir), nil
}

func globalWorkDir(globalFlags *flags.GlobalFlags) string {
	if globalFlags == nil {
		return ""
	}
	return globalFlags.WorkDir()
}

func globalAllowPaths(globalFlags *flags.GlobalFlags) []string {
	if globalFlags == nil {
		return nil
	}
	return globalFlags.AllowPaths()
}

func withFilesystemPolicyMetadata(cfg *config.Config, policy *cliTools.FilesystemPolicy) *config.Config {
	if cfg == nil || policy == nil {
		return cfg
	}
	copy := *cfg
	copy.FilesystemWorkDir = policy.PrimaryRoot()
	copy.FilesystemAllowPaths = policy.AdditionalRoots()
	return &copy
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
	limitValue := session.DefaultSessionListLimit
	sinceValue := ""
	filterValue := ""
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List saved sessions",
		Long: fmt.Sprintf("List session IDs with last modified time, newest first. By default, the %d newest\n"+
			"matching sessions are shown. Use --limit, --since, and --filter together to narrow the\n"+
			"result set.", session.DefaultSessionListLimit),
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			options, err := parseSessionListOptions(limitValue, sinceValue, filterValue)
			if err != nil {
				return err
			}
			storage, err := getSessionStorage(c.flags)
			if err != nil {
				return err
			}
			infos, err := storage.ListWithOptions(options)
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
	cmd.Flags().IntVar(&limitValue, "limit", limitValue, fmt.Sprintf("Maximum number of sessions to print (1-%d; default %d)", session.MaxSessionListLimit, session.DefaultSessionListLimit))
	cmd.Flags().StringVar(&sinceValue, "since", sinceValue, "Include sessions modified at or after this RFC3339 timestamp")
	cmd.Flags().StringVar(&filterValue, "filter", filterValue, "Case-insensitive literal substring to match in session IDs")
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		if strings.Contains(err.Error(), "--limit") {
			return fmt.Errorf("--limit must be an integer between 1 and %d: %w", session.MaxSessionListLimit, err)
		}
		return err
	})
	return cmd
}

func parseSessionListOptions(limitValue int, sinceValue, filterValue string) (session.SessionListOptions, error) {
	if limitValue < 1 || limitValue > session.MaxSessionListLimit {
		return session.SessionListOptions{}, fmt.Errorf("--limit must be between 1 and %d: got %d", session.MaxSessionListLimit, limitValue)
	}

	var since *time.Time
	if sinceValue != "" {
		parsed, parseErr := time.Parse(time.RFC3339, sinceValue)
		if parseErr != nil {
			return session.SessionListOptions{}, fmt.Errorf("--since must be an RFC3339 timestamp (for example 2026-08-31T00:00:00Z): %q", sinceValue)
		}
		since = &parsed
	}

	return session.SessionListOptions{Limit: limitValue, Since: since, Filter: filterValue}, nil
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
