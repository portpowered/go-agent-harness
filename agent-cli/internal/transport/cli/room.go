package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli/internal/events"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli/internal/roomhost"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	"github.com/spf13/cobra"
)

const (
	// DefaultRoomOutputDir is retained for configured-room compatibility when
	// --out is omitted. Bare rooms use a fresh config-directory child instead.
	DefaultRoomOutputDir = runtimeRooms.DefaultRoomOutputDir
)

// RoomRunFunc is the service seam used by the room command. Keeping the seam
// at the structured-result boundary makes command tests independent of live
// provider credentials and network connections.
type RoomRunFunc func(context.Context, io.Writer, runtimeRooms.RoomRunOptions) (runtimeRooms.RoomResult, error)

// RoomSignalContextFunc owns signal/cancellation setup for one room command.
// The production implementation listens for SIGINT and SIGTERM; tests can
// inject a context cancellation without installing process-global handlers.
type RoomSignalContextFunc func(context.Context) (context.Context, func())

// RoomRunCommand implements `yui room run`.
type RoomRunCommand struct {
	globalFlags   *flags.GlobalFlags
	service       runtimeRooms.Service
	registry      devicegw.DeviceRegistry
	signalContext RoomSignalContextFunc
	run           RoomRunFunc
}

// NewRoomRunCommand injects room orchestration and the host device registry
// consulted by launch planning; it never constructs or opens a device.
func NewRoomRunCommand(globalFlags *flags.GlobalFlags, service runtimeRooms.Service, registry devicegw.DeviceRegistry) *RoomRunCommand {
	command := &RoomRunCommand{globalFlags: globalFlags, service: service, registry: registry, signalContext: defaultRoomSignalContext}
	if service != nil {
		command.run = service.Run
	}
	return command
}

// SetRunner replaces the room service used by this command. It is intended
// for hermetic command tests and does not change the production default.
func (c *RoomRunCommand) SetRunner(runner RoomRunFunc) {
	if c != nil && runner != nil {
		c.run = runner
	}
}

// SetSignalContextFactory replaces signal ownership for hermetic command
// tests. A nil factory restores the production SIGINT/SIGTERM behavior.
func (c *RoomRunCommand) SetSignalContextFactory(factory RoomSignalContextFunc) {
	if c == nil {
		return
	}
	if factory == nil {
		c.signalContext = defaultRoomSignalContext
		return
	}
	c.signalContext = factory
}

// RoomCommand is the parent `yui room` command.
type RoomCommand struct{}

// NewRoomCommand creates the room command group.
func NewRoomCommand() *RoomCommand { return &RoomCommand{} }

// Generate returns the room command group.
func (c *RoomCommand) Generate() *cobra.Command {
	return &cobra.Command{
		Use:     "room",
		Short:   "Run participant rooms",
		Example: "  yui room run\n  yui room run --config ./room.yaml",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
}

// Generate returns the room run command.
func (c *RoomRunCommand) Generate() *cobra.Command {
	var configPath string
	var manifestPath string
	var replayPath string
	var outputDir string
	var streamAddress string
	var example bool

	cmd := &cobra.Command{
		Use:   "run [--config <file>] [--replay <bundle>]",
		Short: "Run a room, or start the bare customer-plus-agent room",
		Long: "Run an N-participant room from --config (or the legacy --manifest spelling). " +
			"With neither flag, start the interactive room with one human customer on the host default microphone and speakers and one OpenAI realtime agent. " +
			"An explicit --config is authoritative and overrides bare defaults. Validate a complete room manifest, start one isolated live session per participant, " +
			"and write redacted evidence to an empty output directory; bare rooms choose a fresh child of the effective config directory when --out is omitted. " +
			"With --replay <bundle>, admit a finalized room evidence directory and run every provider participant offline from its recorded session capture; the bundle is authoritative and cannot be combined with --config or --manifest. An optional HTTP " +
			"listener exposes forward-only JSON events at /events.\n\n" +
			"A room config/manifest is a schema-version-1 JSON or YAML document with two top-level keys: " +
			"\"room\" (an object naming at least one of max_turns/max_duration; interactive rooms may omit both) and " +
			"\"participants\" (a list of at least two). Every participant requires id, system_prompt, and tools " +
			"(a list; use [] for none); an all-agent room additionally needs opening_prompt set on at least one " +
			"participant so somebody speaks first. An agent participant also requires provider, model, and " +
			"api_key_env (naming the environment variable holding its credential); a human participant instead " +
			"requires input_device and output_device. Run `yui room run --example` to print a complete, valid " +
			"two-participant example manifest.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if example {
				return writeRoomExampleManifest(cmd.OutOrStdout())
			}
			return c.execute(cmd, configPath, manifestPath, replayPath, outputDir, streamAddress)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "Path to the authoritative schema-version-1 JSON or YAML room config; omit for bare defaults")
	cmd.Flags().StringVar(&manifestPath, "manifest", "", "Path to the schema-version-1 JSON or YAML room manifest")
	cmd.Flags().StringVar(&replayPath, "replay", "", "Replay a finalized room evidence bundle offline without credentials, host audio devices, or live provider connections")
	cmd.Flags().StringVar(&outputDir, "out", DefaultRoomOutputDir, "Empty directory for redacted room evidence (bare default: fresh child under the effective config directory)")
	cmd.Flags().StringVar(&streamAddress, "stream", "", "Optional TCP listen address for GET /events (for example 127.0.0.1:8080)")
	cmd.Flags().BoolVar(&example, "example", false, "Print a complete, valid example room manifest to stdout and exit")
	return cmd
}

// roomExampleManifest is a complete, schema-valid two-participant room
// manifest. A first-time user reverse-engineering the manifest shape one
// validation error at a time (opening_prompt and the required `tools: []`
// are easy to miss because neither is obvious from the field names alone)
// is exactly the friction `yui room run --example` exists to remove: this
// is real JSON that passes room.ParseManifest unmodified, not a schema
// description.
const roomExampleManifest = `{
  "schema_version": 1,
  "room": {
    "max_turns": 6,
    "max_duration": "120s"
  },
  "participants": [
    {
      "id": "alice",
      "kind": "agent",
      "system_prompt": "You are Alice. Speak briefly and address Bob by name.",
      "opening_prompt": "Greet Bob and ask what he would like to discuss.",
      "provider": "openai",
      "model": "gpt-realtime-2.1-mini",
      "api_key_env": "OPENAI_API_KEY",
      "voice": "cedar",
      "tools": []
    },
    {
      "id": "bob",
      "kind": "agent",
      "system_prompt": "You are Bob. Respond to Alice briefly and stay on topic.",
      "provider": "openai",
      "model": "gpt-realtime-2.1-mini",
      "api_key_env": "OPENAI_API_KEY",
      "voice": "marin",
      "tools": []
    }
  ]
}
`

// writeRoomExampleManifest prints roomExampleManifest to w for `yui room
// run --example`. Every provider participant needs its own credential
// available at run time: this example names OPENAI_API_KEY for both, so
// `OPENAI_API_KEY=... agent room run --config example.json` runs it as-is.
func writeRoomExampleManifest(w io.Writer) error {
	_, err := io.WriteString(w, roomExampleManifest)
	return err
}

func (c *RoomRunCommand) execute(cmd *cobra.Command, configPath, manifestPath, replayPath, outputDir, streamAddress string) error {
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	if c == nil || c.service == nil {
		return errors.New("room service is required")
	}
	configDir := roomConfigDir(roomRunGlobalFlags(c))
	plan, outputDir, err := c.admitRoomRun(roomhost.Paths{
		Config: configPath, Manifest: manifestPath, Replay: replayPath, ConfigDir: configDir,
	}, outputDir, cmd.Flags().Changed("out"))
	if err != nil {
		return err
	}
	// Every byte this command prints or returns after admission passes
	// through the same credential redaction the room applies to evidence.
	redactor := roomhost.SecretRedactor(plan, configDir)
	output := &roomCommandOutput{writer: cmd.OutOrStdout(), redact: redactor.Redact}
	return redactRoomError(redactor, c.runAdmitted(parent, plan, outputDir, output, redactor, streamAddress))
}

func (c *RoomRunCommand) runAdmitted(parent context.Context, plan runtimeRooms.RoomRunPlan, outputDir string, output *roomCommandOutput, redactor runtimeRooms.RoomSecretRedactor, streamAddress string) error {
	outputLabel := outputDir
	if outputLabel == "" {
		outputLabel = "disabled"
	}
	output.printf("room starting: participants=%d output=%s\n", len(plan.Manifest.Participants), outputLabel)
	if err := output.err(); err != nil {
		return err
	}
	stream, err := startRoomStream(parent, output, plan, redactor, streamAddress)
	if err != nil {
		return err
	}
	runContext, stopSignals, err := c.newRunContext(parent)
	if err != nil {
		return errors.Join(err, stream.shutdown(parent))
	}
	defer stopSignals()

	options := c.roomRunOptions(plan, outputDir, output, stream.sink())
	var result runtimeRooms.RoomResult
	var runErr error
	if c.run == nil {
		runErr = errors.New("room run service is not configured")
	} else {
		result, runErr = c.run(runContext, io.Discard, options)
	}
	streamErr := stream.shutdown(parent)
	writeRoomResult(output, result)
	return errors.Join(runErr, streamErr, output.err())
}

// newRunContext installs the command's signal ownership for one run.
func (c *RoomRunCommand) newRunContext(parent context.Context) (context.Context, func(), error) {
	newSignalContext := c.signalContext
	if newSignalContext == nil {
		newSignalContext = defaultRoomSignalContext
	}
	runContext, stopSignals := newSignalContext(parent)
	if runContext == nil {
		if stopSignals != nil {
			stopSignals()
		}
		return nil, nil, errors.New("room signal context factory returned a nil context")
	}
	if stopSignals == nil {
		stopSignals = func() {}
	}
	return runContext, stopSignals, nil
}

// roomStream is the optional --stream listener and the room stream it serves.
type roomStream struct {
	stream runtimeRooms.RoomEventStream
	server *events.Server
}

// startRoomStream opens the redacting room stream and its listener when
// --stream is set, then announces every participant as joined.
func startRoomStream(ctx context.Context, output *roomCommandOutput, plan runtimeRooms.RoomRunPlan, redactor runtimeRooms.RoomSecretRedactor, address string) (*roomStream, error) {
	if strings.TrimSpace(address) == "" {
		return &roomStream{}, nil
	}
	stream, err := roomhost.EventStream(plan, redactor)
	if err != nil {
		return nil, fmt.Errorf("configure room stream: %w", err)
	}
	server, err := events.Start(address, stream)
	if err != nil {
		return nil, errors.Join(err, stream.Close())
	}
	active := &roomStream{stream: stream, server: server}
	output.printf("room stream listening: %s\n", server.URL())
	if err := output.err(); err != nil {
		return nil, errors.Join(err, active.shutdown(ctx))
	}
	for _, participant := range plan.Manifest.Participants {
		stream.PublishRoomEvent(runtimeRooms.RoomStreamEventParticipantJoined, participant.ID, "")
	}
	return active, nil
}

// sink is the run's live event sink, or nil when no stream is configured.
func (s *roomStream) sink() runtimeRooms.EventSink {
	if s == nil || s.stream == nil {
		return nil
	}
	return s.stream
}

func (s *roomStream) shutdown(ctx context.Context) error {
	if s == nil || s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

// admitRoomRun resolves the service's run plan and evidence destination for
// one invocation. Output validation errors name the --out flag.
func (c *RoomRunCommand) admitRoomRun(paths roomhost.Paths, requested string, explicit bool) (runtimeRooms.RoomRunPlan, string, error) {
	plan, err := c.service.ResolveRunPlan(roomhost.RunPlanOptions(paths, c.registry))
	if err != nil {
		return runtimeRooms.RoomRunPlan{}, "", err
	}
	outputDir, err := c.service.ResolveRunOutput(plan, requested, explicit)
	if err != nil {
		return runtimeRooms.RoomRunPlan{}, "", err
	}
	if err := c.service.ValidateRunOutput(plan, outputDir); err != nil {
		return runtimeRooms.RoomRunPlan{}, "", fmt.Errorf("validate --out %q: %w", outputDir, err)
	}
	return plan, outputDir, nil
}

// roomRunOptions binds the command's progress output and host capabilities
// to the admitted plan. Replays never receive host config or browsers.
func (c *RoomRunCommand) roomRunOptions(plan runtimeRooms.RoomRunPlan, outputDir string, output *roomCommandOutput, sink runtimeRooms.EventSink) runtimeRooms.RoomRunOptions {
	participants := len(plan.Manifest.Participants)
	readyParticipants := 0
	options := roomhost.RunOptions(plan)
	options.OutputDir = outputDir
	options.WorkDir = globalWorkDir(roomRunGlobalFlags(c))
	options.AllowPaths = globalAllowPaths(roomRunGlobalFlags(c))
	options.EventSink = sink
	options.OnDiagnostic = func(participantID string, record runtimeRooms.RoomDiagnosticRecord) {
		writeRoomDiagnosticProgress(output, participantID, record)
	}
	options.OnParticipantReady = func(ready runtimeRooms.RoomParticipantReady) {
		output.printf("participant %q ready: kind=%s input=%s output=%s provider=%s model=%s\n", ready.ParticipantID, ready.Kind, ready.InputDevice, ready.OutputDevice, ready.Provider, ready.Model)
		readyParticipants++
		if readyParticipants == participants {
			output.printf("room running: participants=%d\n", participants)
		}
	}
	options.OnParticipantTerminated = func(result runtimeRooms.RoomParticipantResult) {
		output.printf("participant %q: %s turns=%d connected=%t\n", result.ParticipantID, result.TerminationReason, result.TurnsCompleted, result.Connected)
	}
	if !plan.Replay() {
		configDir := roomConfigDir(roomRunGlobalFlags(c))
		options.ConfigDir, options.ConfigCredential = configDir, roomhost.ConfigCredential(configDir)
		options.BrowserCapabilitiesFactory = newRoomParticipantBrowserCapabilitiesFactory(configDir)
	}
	return options
}

func defaultRoomSignalContext(parent context.Context) (context.Context, func()) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}

func roomRunGlobalFlags(command *RoomRunCommand) *flags.GlobalFlags {
	if command == nil {
		return nil
	}
	return command.globalFlags
}

func roomConfigDir(globalFlags *flags.GlobalFlags) string {
	if globalFlags == nil {
		return ""
	}
	return globalFlags.ConfigDir()
}

func writeRoomDiagnosticProgress(output *roomCommandOutput, participantID string, record runtimeRooms.RoomDiagnosticRecord) {
	if output == nil {
		return
	}
	switch record.Event {
	case serviceSession.SessionDiagnosticEventTurn:
		turn := record.Fields["turn_index"]
		if turn == "" {
			turn = "?"
		}
		output.printf("participant %q: %s turn=%s\n", participantID, record.Event, turn)
	case serviceSession.SessionDiagnosticEventToolCall:
		output.printf("participant %q: %s\n", participantID, record.Event)
	case serviceSession.SessionDiagnosticEventFailure:
		output.printf("participant %q: %s\n", participantID, record.Event)
	}
}

func writeRoomResult(output *roomCommandOutput, result runtimeRooms.RoomResult) {
	if output == nil {
		return
	}
	reason := result.TerminationReason
	if reason == "" {
		reason = result.Reason
	}
	if reason == "" {
		reason = runtimeRooms.RoomTerminationFailed
	}
	output.printf("room stopped: reason=%s participants=%d active=%d\n", reason, len(result.Participants), len(result.ActiveParticipants))
	ids := make([]string, 0, len(result.Participants))
	for id := range result.Participants {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		participant := result.Participants[id]
		participantID := participant.ParticipantID
		if participantID == "" {
			participantID = participant.ID
		}
		participantReason := participant.TerminationReason
		if participantReason == "" {
			participantReason = participant.Reason
		}
		line := fmt.Sprintf("participant %q: %s turns=%d connected=%t", participantID, participantReason, participant.TurnsCompleted, participant.Connected)
		if participant.Classification != "" {
			line += " classification=" + participant.Classification
		}
		output.printf("%s\n", line)
	}
}

// roomCommandOutput serializes progress lines and redacts every line with
// the room's credential redactor before it reaches the writer.
type roomCommandOutput struct {
	mu       sync.Mutex
	writer   io.Writer
	redact   func(string) string
	writeErr error
}

func (o *roomCommandOutput) printf(format string, args ...any) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.writeErr != nil {
		return
	}
	if o.writer == nil {
		o.writer = io.Discard
	}
	line := fmt.Sprintf(format, args...)
	if o.redact != nil {
		line = o.redact(line)
	}
	_, o.writeErr = io.WriteString(o.writer, line)
}

func (o *roomCommandOutput) err() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.writeErr
}

// redactedError keeps the error chain for errors.Is/As while presenting a
// message with the room's credentials removed.
type redactedError struct {
	err     error
	message string
}

func (e redactedError) Error() string { return e.message }
func (e redactedError) Unwrap() error { return e.err }

func redactRoomError(redactor runtimeRooms.RoomSecretRedactor, err error) error {
	if err == nil || redactor == nil {
		return err
	}
	message := redactor.Redact(err.Error())
	if message == err.Error() {
		return err
	}
	return redactedError{err: err, message: message}
}

// newRoomParticipantBrowserCapabilitiesFactory composes one independent
// browser owner per room participant. The session browser composition stays
// the single source for broker tools, initialization, and cleanup; each
// participant gets a fresh in-memory selection store. Room admission and
// scoping of the resulting capability belong to the rooms service.
func newRoomParticipantBrowserCapabilitiesFactory(configDir string) runtimeRooms.BrowserCapabilitiesFactory {
	browserFactory := NewSessionToolCapabilitiesFactory(roomBrowserOnlyStaticExecutor{}, func(browser config.BrowserConfig) (webmcp.Broker, error) {
		doctorFactory := NewProductionWebMCPDoctorFactory(WithWebMCPProductionSelectionStore(discovery.NewMemorySelectionStore()))
		return newSessionBrowserBrokerWithDoctorFactory(browser, doctorFactory)
	})
	return func(participant runtimeRooms.Participant) (runtimeRooms.BrowserCapabilities, error) {
		if participant.BrowserTools == nil {
			return runtimeRooms.BrowserCapabilities{}, errors.New("room browser capability requested for a participant without browserTools")
		}
		capabilities, err := browserFactory(&config.Config{Browser: config.BrowserConfigForRoomTools(*participant.BrowserTools), ConfigDir: configDir})
		if err != nil {
			return runtimeRooms.BrowserCapabilities{}, err
		}
		return runtimeRooms.BrowserCapabilities{
			Executor: capabilities.Executor, Definitions: capabilities.Definitions,
			ToolDefinitionBase:     append([]messages.ToolDefinition(nil), capabilities.Definitions...),
			RefreshToolDefinitions: capabilities.RefreshDefinitionsWithError,
			BrowserWatch:           roomhost.BrowserWatch(capabilities.BrowserEventWatch, capabilities.BrowserWatch),
			Initialize:             capabilities.Initialize, Close: capabilities.Close,
		}, nil
	}
}

// roomBrowserOnlyStaticExecutor is the empty static surface handed to the
// session capability factory so it composes only the browser tools.
type roomBrowserOnlyStaticExecutor struct{}

func (roomBrowserOnlyStaticExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name}, errors.New("room browser-only static executor has no tools")
}
