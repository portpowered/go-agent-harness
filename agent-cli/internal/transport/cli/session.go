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
	hostServices "github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	serviceDevices "github.com/portpowered/go-agent-harness/agent-cli/internal/services/devices"
	serviceSelfPlay "github.com/portpowered/go-agent-harness/agent-cli/internal/services/selfplay"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	runtimeRecording "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
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
	// before the session provider can issue a browser tool call. An initialization
	// error prevents provider startup, because advertising browser tools without
	// a usable browser leaves the model in an unrecoverable session.
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

func sessionToolDiagnosticSink(out io.Writer) serviceSession.SessionToolDiagnosticSink {
	return serviceSession.SessionToolDiagnosticFunc(func(diagnostic serviceSession.SessionToolDiagnostic) {
		if diagnostic.Error == nil {
			return
		}
		_, _ = fmt.Fprintf(out, "tool diagnostic: tool=%q call_id=%q source=%q error_code=%q detail=%s\n", diagnostic.ToolName, diagnostic.ToolCallID, diagnostic.Source, diagnostic.ErrorCode, diagnostic.Error)
	})
}

// sessionAudioDiagnosticSink surfaces the local playback queue's cumulative
// drop accounting (see go-audio/pkg/audio/device_playback.go) at
// output-device teardown. Without this sink, DroppedSamples/OverflowEvents
// are computed correctly by every live session but are never observable
// anywhere (not stdout, not stderr, not a recording artifact); this is the
// only wiring in the CLI that reads SessionDiagnosticEventPlaybackOverflow.
// Only the playback-overflow event is printed; every other diagnostic event
// this generic sink may receive (turn, terminal, tool-continuation, ...)
// is intentionally left silent, matching prior CLI behavior.
func sessionAudioDiagnosticSink(out io.Writer) serviceSession.SessionDiagnosticSink {
	return serviceSession.SessionDiagnosticFunc(func(record serviceSession.SessionDiagnosticRecord) {
		if record.Event != serviceSession.SessionDiagnosticEventPlaybackOverflow {
			return
		}
		_, _ = fmt.Fprintf(out, "playback diagnostic: event=%q device=%q sample_rate=%s dropped_samples=%s overflow_events=%s peak_queued_samples=%s capacity_samples=%s latency_target_ms=%s\n",
			record.Event,
			record.Fields[serviceSession.SessionDiagnosticFieldPlaybackDeviceID],
			record.Fields[serviceSession.SessionDiagnosticFieldPlaybackSampleRate],
			record.Fields[serviceSession.SessionDiagnosticFieldPlaybackDroppedSamples],
			record.Fields[serviceSession.SessionDiagnosticFieldPlaybackOverflowEvents],
			record.Fields[serviceSession.SessionDiagnosticFieldPlaybackPeakQueuedSamples],
			record.Fields[serviceSession.SessionDiagnosticFieldPlaybackCapacitySamples],
			record.Fields[serviceSession.SessionDiagnosticFieldPlaybackLatencyTargetMillis],
		)
	})
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

// SessionCommand is the session group (parent command); subcommands are wired in core_router.go.
type SessionCommand struct {
	askFlags        *flags.AskFlags
	globalFlags     *flags.GlobalFlags
	storeFactory    runtimeSession.FileStoreFactory
	streamObserver  serviceSession.SessionStreamObserver
	sessionService  serviceSession.Service
	selfPlayService serviceSelfPlay.Service
	// liveService and deviceService are the embeddable runtime path used by
	// production composition for continuous sessions. The legacy service
	// remains optional so focused CLI tests can inject only the text/session
	// contract without constructing audio or provider transports.
	liveService             runtimeSession.LiveService
	liveReplayService       runtimeReplay.Service
	deviceService           runtimeDevices.Service
	fileDeviceService       FileDeviceService
	recordingService        runtimeRecording.Service
	liveCapabilities        SessionToolCapabilitiesFactory
	liveCredentialReference LiveCredentialReference
	feedbackWarningWriter   io.Writer
	imagePaths              []string
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
	if err := serviceSession.ValidateOpenAIRealtimeVoice(value); err != nil {
		v.err = err
		return err
	}
	v.err = nil
	*v.target = value
	return nil
}

func (*sessionVoiceFlagValue) Type() string { return "string" }

// SetSessionStreamObserver adds an optional observer for deltas consumed by a
// session loop. It is primarily useful to verify emitted tool-result streams
// through the CLI composition root without changing normal command output.
func (c *SessionCommand) SetSessionStreamObserver(observer serviceSession.SessionStreamObserver) {
	if c == nil {
		return
	}
	c.streamObserver = observer
}

// Generate returns the cobra command for the session group.
// sessionCommandLongHelp is the session command long help, hoisted out of
// Generate to keep the constructor within the function-length gate.
const sessionCommandLongHelp = "Run a bidirectional session inference capture or replay a session capture file.\n" +
	"Use --record <file>.json to capture live session traffic, --record-dir <dir> for a complete both-side recording directory, or --replay <file>.json to replay a saved capture without live provider network calls.\n" +
	"With no capture, prompt, file-audio, scheduled-turn, image, or browser flags, bare `yui session` starts a live OpenAI Realtime voice session over WebSocket on the default microphone and speakers; use --provider, --model, --api-key, --voice, or --audio-in-device/--audio-out-device to override its live defaults.\n" +
	"Use repeatable finite spoken-turn inputs with --record-dir to replay multiple turns through one persistent session; scheduled turns are completion-gated by default. The optional scheduled barge mode releases each later turn against its identified active, non-terminal prior response. Ordinary scheduled turns do not interrupt responses.\n\n" +
	"WebMCP browser sessions: use --browser-tools webmcp without --browser-cdp-url or --browser-ws-endpoint for an agent-managed local Chrome; no CDP port is required. With only --browser-tools and a provider, the command starts an interactive microphone session, prints Starting and Listening state, and waits for cancellation or provider termination. Supplying either endpoint keeps the externally managed browser path, and the agent never closes an external browser.\n\n" +
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
	"replay-timing",
	"prompt",
	"system-prompt",
	"audio-in",
	"audio-out",
	"trace-audio",
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
// list in here routes those invocations to real validation instead (e.g. live
// provider setup or the actual --audio-in file-not-found error).
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

// isPassiveLiveInvocation reports whether every explicit session-mode flag is
// a passive observer (--record and/or --audio-out), with no prompt, input
// audio, scheduled turn, or image alongside it. Browser and device-selection
// flags are modifiers rather than session drivers. Observing an interactive
// voice session must preserve its lifetime and implicit audio devicegw.
// This is the operator's flagship
// shape: an otherwise-bare live microphone conversation that additionally
// captures a side recording. Unlike a fully bare invocation (which admits
// without needing --record at all), this deliberately keeps --record's
// explicit-mode admission and capture-recording wiring; it only restores the
// implicit microphone/speaker devices and the keep-open-until-provider-closes
// semantics that bare mode gets. Without this, the mere presence of --record
// silently dropped both, and the session closed within milliseconds of
// opening instead of running the interactive conversation it was recording.
func isPassiveLiveInvocation(cmd *cobra.Command, args []string, imagePaths []string) bool {
	if cmd == nil || len(args) > 0 || len(imagePaths) > 0 {
		return false
	}
	if !cmd.Flags().Changed("record") && !cmd.Flags().Changed("audio-out") && !cmd.Flags().Changed("trace-audio") {
		return false
	}
	for _, name := range sessionModeFlagNames {
		if name != "record" && name != "audio-out" && name != "trace-audio" && cmd.Flags().Changed(name) {
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
	var prompt, voice, reasoningEffort, audioIn string
	recordDirPath, audioOutPath := "", ""
	var traceAudio bool
	transport := SessionTransportWebSocket
	signaling, mediaSource := "", ""
	var maxDuration time.Duration
	var waitForClose, noInputTranscription, computerUse, experimentalTools, noTerminalTools bool
	var audioInTurns []string
	var audioInTurnBarge bool
	var audioInterrupts []string
	var audioInterruptTool string
	var audioInDevice devicegw.DeviceID
	var audioOutDevice devicegw.DeviceID
	var audioDeviceServer string
	browserFlags := flags.NewBrowserFlags()
	voiceFlag := &sessionVoiceFlagValue{target: &voice}
	cmd := &cobra.Command{
		Use:          "session [message]",
		Short:        "Run or manage agent sessions",
		Long:         sessionCommandLongHelp,
		Example:      sessionCommandExample,
		Args:         cobra.ArbitraryArgs,
		SilenceUsage: true,
		PreRunE:      func(_ *cobra.Command, _ []string) error { return validateSessionModelOptions(voice, reasoningEffort) },
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.runSessionCommand(cmd, args, sessionCommandRunState{
				Prompt: prompt, Voice: voice, ReasoningEffort: reasoningEffort,
				RecordDirectory: recordDirPath, AudioOutputPath: audioOutPath,
				TraceAudio: traceAudio, Transport: transport, Signaling: signaling,
				MediaSource: mediaSource, MaxDuration: maxDuration,
				WaitForClose: waitForClose, NoInputTranscription: noInputTranscription,
				ComputerUse: computerUse, ExperimentalTools: experimentalTools,
				NoTerminalTools: noTerminalTools, AudioInputPath: audioIn,
				AudioTurns: append([]string(nil), audioInTurns...), AudioTurnBarge: audioInTurnBarge,
				AudioInterrupts: append([]string(nil), audioInterrupts...), AudioInterruptTool: audioInterruptTool,
				AudioInputDevice: audioInDevice, AudioOutputDevice: audioOutDevice,
				AudioDeviceServer: audioDeviceServer, BrowserTools: browserFlags.Tools, BrowserFlags: browserFlags,
			})
		},
	}
	c.registerSessionFlags(cmd, sessionFlagTargets{
		voiceFlag: voiceFlag, browserFlags: browserFlags,
		recordDirPath: &recordDirPath, prompt: &prompt,
		maxDuration: &maxDuration, waitForClose: &waitForClose,
		noInputTranscription: &noInputTranscription, audioIn: &audioIn,
		reasoningEffort: &reasoningEffort, computerUse: &computerUse, experimentalTools: &experimentalTools, noTerminalTools: &noTerminalTools,
		audioInTurns: &audioInTurns, audioInTurnBarge: &audioInTurnBarge,
		audioInterrupts: &audioInterrupts, audioInterruptTool: &audioInterruptTool,
		audioInDevice: &audioInDevice, audioOutPath: &audioOutPath,
		audioOutDevice: &audioOutDevice, audioDeviceServer: &audioDeviceServer,
		traceAudio:  &traceAudio,
		mediaSource: &mediaSource, transport: &transport, signaling: &signaling,
	})
	return cmd
}

// sessionFlagTargets collects the local variables that (*SessionCommand).Generate's
// flags bind into, so registerSessionFlags can wire them up in one place
// without Generate itself growing past the funlen budget.
type sessionFlagTargets struct {
	voiceFlag            *sessionVoiceFlagValue
	browserFlags         *flags.BrowserFlags
	recordDirPath        *string
	prompt               *string
	maxDuration          *time.Duration
	waitForClose         *bool
	noInputTranscription *bool
	reasoningEffort      *string
	computerUse          *bool
	experimentalTools    *bool
	noTerminalTools      *bool
	audioIn              *string
	audioInTurns         *[]string
	audioInTurnBarge     *bool
	audioInterrupts      *[]string
	audioInterruptTool   *string
	audioInDevice        *devicegw.DeviceID
	audioOutPath         *string
	audioOutDevice       *devicegw.DeviceID
	audioDeviceServer    *string
	traceAudio           *bool
	mediaSource          *string
	transport            *string
	signaling            *string
}

// registerSessionFlags registers the `session` command's flags. It is a pure
// extraction from Generate with no behaviour change.
func (c *SessionCommand) registerSessionFlags(cmd *cobra.Command, t sessionFlagTargets) {
	setSessionFlagErrorFunc(cmd, t.voiceFlag)
	cmd.Flags().StringVar(&c.askFlags.RecordCapturePath, "record", "", "Record bidirectional session traffic to a JSON capture file")
	cmd.Flags().StringVar(t.recordDirPath, "record-dir", "", "Record a complete both-side session directory separately from --record")
	cmd.Flags().StringVar(&c.askFlags.ReplayCapturePath, "replay", "", "Replay bidirectional session traffic from a JSON capture file without live provider network calls")
	cmd.Flags().StringVar(&c.askFlags.ReplayTiming, "replay-timing", "immediate", "Replay cadence: immediate for fast order validation, or recorded to preserve timestamp_ms timing")
	cmd.Flags().StringVar(t.prompt, "prompt", "", "Seed the realtime session with text")
	cmd.Flags().StringVar(&c.askFlags.SystemPrompt, "system-prompt", "", "Path to system prompt file or literal text")
	cmd.Flags().StringVar(&c.askFlags.Provider, "provider", "", "Session provider ID (use grok or openai for live record mode)")
	cmd.Flags().DurationVar(t.maxDuration, "max-duration", 0, "Maximum session duration as a Go duration; exits cleanly when the bound is reached")
	cmd.Flags().BoolVar(t.waitForClose, "wait-for-close", false, "Keep the session running after a completed response until the provider closes it")
	cmd.Flags().StringVar(&c.askFlags.Model, "model", "", "Session model ID for live record mode")
	cmd.Flags().BoolVar(t.noInputTranscription, "no-input-transcription", false, "Disable customer-speech transcription for live OpenAI audio-input sessions")
	cmd.Flags().StringVar(t.reasoningEffort, "reasoning-effort", "", "OpenAI Realtime reasoning effort: minimal, low, medium, high, or xhigh")
	cmd.Flags().BoolVar(t.computerUse, "computer-use", false, "Expose host screen and pointer computer-control tools")
	cmd.Flags().BoolVar(t.experimentalTools, "experimental-tools", false, "Expose experimental skill, sleep, and web tools")
	cmd.Flags().BoolVar(t.noTerminalTools, "no-terminal-tools", false, "Hide built-in shell and filesystem tools from the model")
	cmd.Flags().Var(t.voiceFlag, "voice", fmt.Sprintf("OpenAI Realtime audio output voice (supported: %s)", strings.Join(serviceSession.SupportedOpenAIRealtimeVoices(), ", ")))
	cmd.Flags().StringVar(&c.askFlags.APIKey, "api-key", "", "Session provider API key for live record mode")
	cmd.Flags().StringVar(t.audioIn, "audio-in", "", "Stream a .wav/.pcm/.raw file incrementally; use - for raw PCM16 standard input")
	cmd.Flags().StringArrayVar(t.audioInTurns, "audio-in-turn", nil, "Add a finite .wav/.pcm/.raw spoken turn to one persistent --record-dir session (repeatable)")
	cmd.Flags().BoolVar(t.audioInTurnBarge, "audio-in-turn-barge", false, "Release later --audio-in-turn inputs against an active prior response; without this flag scheduled turns remain completion-gated")
	cmd.Flags().StringArrayVar(t.audioInterrupts, "audio-interrupt", nil, "Release finite .wav/.pcm/.raw audio after the first observed in-flight WebMCP invocation (repeatable; live browser sessions only)")
	cmd.Flags().StringVar(t.audioInterruptTool, "audio-interrupt-on-tool", "", "With --audio-interrupt, wait for this WebMCP tool's in-flight invocation instead of the first one")
	cmd.Flags().StringVar(t.audioInDevice, serviceDevices.SessionAudioInDeviceFlag, "", "Capture RTC audio from a registry device ID; empty or default selects the input default")
	cmd.Flags().StringVar(t.audioOutPath, "audio-out", "", "Write assistant PCM16 to a .wav/.pcm/.raw path or -; with --audio-out-device, tap device-bound PCM")
	cmd.Flags().BoolVar(t.traceAudio, "trace-audio", false, "Record audio boundary WAVs and timing JSONL (included automatically with --record-dir)")
	cmd.Flags().StringVar(t.audioOutDevice, serviceDevices.SessionAudioOutDeviceFlag, "", "Play RTC audio to a registry device ID; empty or default selects the output default")
	cmd.Flags().StringVar(t.audioDeviceServer, "audio-device-server", "", "Use a loopback audio-device server host:port instead of platform devices")
	cmd.Flags().StringVar(&c.askFlags.BaseURL, "base-url", "", "Session provider base URL override")
	cmd.Flags().StringArrayVar(&c.imagePaths, "image", nil, "Attach a local image to the realtime user turn (repeatable; order is preserved)")
	cmd.Flags().StringVar(t.mediaSource, "media-source", "", "Deferred/unavailable WebRTC receive-only external media source; requires --transport webrtc and cannot be combined with --audio-in")
	cmd.Flags().StringVar(t.transport, "transport", SessionTransportWebSocket, "Session transport: ws (default, supported) or webrtc (deferred/unavailable customer path)")
	cmd.Flags().StringVar(t.signaling, "signaling", "", "Deferred/unavailable WebRTC signaling endpoint; customer-reachable network signaling is not wired yet; requires --transport webrtc, and --transport webrtc requires this flag")
	registerSessionBrowserFlags(cmd, t.browserFlags)
	cmd.AddCommand(NewSessionSelfPlayCommand(c.globalFlags, c.selfPlayService).Generate())
}

func setSessionFlagErrorFunc(cmd *cobra.Command, voiceFlag *sessionVoiceFlagValue) {
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		if voiceFlag.err != nil {
			return voiceFlag.err
		}
		if err != nil && err.Error() == "unknown flag: --audio-device-out" {
			return fmt.Errorf("%w (did you mean --audio-out-device?)", err)
		}
		return err
	})
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

// getSessionStorage opens the runtime-owned store for a CLI command. Paths
// are resolved by the host, while persistence and its codecs remain owned by
// services/session. A nil factory is retained only for help-only compatibility
// constructors; executable production commands are always wired with one.
func getSessionStorage(globalFlags *flags.GlobalFlags, storeFactory runtimeSession.FileStoreFactory) (runtimeSession.ManagedStore, error) {
	if storeFactory == nil {
		return nil, errors.New("session file store factory is required")
	}
	return hostServices.NewSessionStoreWithFactory(globalFlags, storeFactory)
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

// SessionShowCommand wraps the session show subcommand.
type SessionShowCommand struct {
	flags        *flags.GlobalFlags
	storeFactory runtimeSession.FileStoreFactory
}

// NewSessionShowCommand composes the session show transport with the
// runtime-owned durable store.
func NewSessionShowCommand(flags *flags.GlobalFlags, storeFactory runtimeSession.FileStoreFactory) *SessionShowCommand {
	return &SessionShowCommand{flags: flags, storeFactory: storeFactory}
}

// Generate returns the cobra command for session show.
func (c *SessionShowCommand) Generate() *cobra.Command {
	return &cobra.Command{
		Use:   "show <session-id>",
		Short: "Show a session by ID",
		Long:  "Load and print the conversation history for the given session.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			storage, err := getSessionStorage(c.flags, c.storeFactory)
			if err != nil {
				return err
			}
			sessionID := args[0]
			msgs, err := storage.Load(cmd.Context(), sessionID)
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
	flags        *flags.GlobalFlags
	storeFactory runtimeSession.FileStoreFactory
}

// NewSessionListCommand composes the session list transport with the
// runtime-owned durable store.
func NewSessionListCommand(flags *flags.GlobalFlags, storeFactory runtimeSession.FileStoreFactory) *SessionListCommand {
	return &SessionListCommand{flags: flags, storeFactory: storeFactory}
}

// Generate returns the cobra command for session list.
func (c *SessionListCommand) Generate() *cobra.Command {
	limitValue := runtimeSession.DefaultSessionListLimit
	sinceValue := ""
	filterValue := ""
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List saved sessions",
		Long: fmt.Sprintf("List session IDs with last modified time, newest first. By default, the %d newest\n"+
			"matching sessions are shown. Use --limit, --since, and --filter together to narrow the\n"+
			"result set.", runtimeSession.DefaultSessionListLimit),
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			options, err := parseSessionListOptions(limitValue, sinceValue, filterValue)
			if err != nil {
				return err
			}
			storage, err := getSessionStorage(c.flags, c.storeFactory)
			if err != nil {
				return err
			}
			infos, err := storage.List(cmd.Context(), options)
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
	cmd.Flags().IntVar(&limitValue, "limit", limitValue, fmt.Sprintf("Maximum number of sessions to print (1-%d; default %d)", runtimeSession.MaxSessionListLimit, runtimeSession.DefaultSessionListLimit))
	cmd.Flags().StringVar(&sinceValue, "since", sinceValue, "Include sessions modified at or after this RFC3339 timestamp")
	cmd.Flags().StringVar(&filterValue, "filter", filterValue, "Case-insensitive literal substring to match in session IDs")
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		if strings.Contains(err.Error(), "--limit") {
			return fmt.Errorf("--limit must be an integer between 1 and %d: %w", runtimeSession.MaxSessionListLimit, err)
		}
		return err
	})
	return cmd
}

func parseSessionListOptions(limitValue int, sinceValue, filterValue string) (runtimeSession.SessionListOptions, error) {
	if limitValue < 1 || limitValue > runtimeSession.MaxSessionListLimit {
		return runtimeSession.SessionListOptions{}, fmt.Errorf("--limit must be between 1 and %d: got %d", runtimeSession.MaxSessionListLimit, limitValue)
	}

	var since *time.Time
	if sinceValue != "" {
		parsed, parseErr := time.Parse(time.RFC3339, sinceValue)
		if parseErr != nil {
			return runtimeSession.SessionListOptions{}, fmt.Errorf("--since must be an RFC3339 timestamp (for example 2026-08-31T00:00:00Z): %q", sinceValue)
		}
		since = &parsed
	}

	return runtimeSession.SessionListOptions{Limit: limitValue, Since: since, Filter: filterValue}, nil
}

// SessionDeleteCommand wraps the session delete subcommand.
type SessionDeleteCommand struct {
	flags        *flags.GlobalFlags
	storeFactory runtimeSession.FileStoreFactory
}

// NewSessionDeleteCommand composes the session delete transport with the
// runtime-owned durable store.
func NewSessionDeleteCommand(flags *flags.GlobalFlags, storeFactory runtimeSession.FileStoreFactory) *SessionDeleteCommand {
	return &SessionDeleteCommand{flags: flags, storeFactory: storeFactory}
}

// Generate returns the cobra command for session delete.
func (c *SessionDeleteCommand) Generate() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <session-id>",
		Short: "Delete a session by ID",
		Long:  "Remove the session file. Use session list to see IDs.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			storage, err := getSessionStorage(c.flags, c.storeFactory)
			if err != nil {
				return err
			}
			sessionID := args[0]
			if err := storage.Delete(cmd.Context(), sessionID); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Deleted session %s\n", sessionID); err != nil {
				return err
			}
			return nil
		},
	}
}
