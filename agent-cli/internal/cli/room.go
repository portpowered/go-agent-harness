package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/spf13/cobra"
)

const (
	// DefaultRoomOutputDir is retained for configured-room compatibility when
	// --out is omitted. Bare rooms use a fresh config-directory child instead.
	DefaultRoomOutputDir = services.DefaultRoomOutputDir

	roomStreamShutdownTimeout = 5 * time.Second
)

// RoomRunFunc is the service seam used by the room command. Keeping the seam
// at the structured-result boundary makes command tests independent of live
// provider credentials and network connections.
type RoomRunFunc func(context.Context, io.Writer, services.RoomRunOptions) (services.RoomResult, error)

// RoomSignalContextFunc owns signal/cancellation setup for one room command.
// The production implementation listens for SIGINT and SIGTERM; tests can
// inject a context cancellation without installing process-global handlers.
type RoomSignalContextFunc func(context.Context) (context.Context, func())

// RoomRunCommand implements `yui room run`.
type RoomRunCommand struct {
	globalFlags    *flags.GlobalFlags
	deviceRegistry audio.DeviceRegistry
	signalContext  RoomSignalContextFunc
	run            RoomRunFunc
}

// NewRoomRunCommand creates the room runner command using the host audio
// registry. An explicit registry can be supplied through
// NewRoomRunCommandWithDeviceRegistry for hermetic composition tests.
func NewRoomRunCommand(globalFlags *flags.GlobalFlags) *RoomRunCommand {
	return &RoomRunCommand{
		globalFlags:    globalFlags,
		deviceRegistry: newDefaultDeviceRegistry(),
		signalContext:  defaultRoomSignalContext,
		run:            services.RunRoomWithResult,
	}
}

// NewRoomRunCommandWithDeviceRegistry composes a room command with the same
// registry abstraction used by device and session commands.
func NewRoomRunCommandWithDeviceRegistry(globalFlags *flags.GlobalFlags, registry audio.DeviceRegistry) *RoomRunCommand {
	command := NewRoomRunCommand(globalFlags)
	command.deviceRegistry = registry
	return command
}

// SetDeviceRegistry replaces the registry used during side-effect-free bare
// launch resolution. It is intended for application wiring and fake-device
// command tests.
func (c *RoomRunCommand) SetDeviceRegistry(registry audio.DeviceRegistry) {
	if c == nil {
		return
	}
	c.deviceRegistry = registry
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
	filesystemPolicy, err := cliTools.ResolveFilesystemPolicy(
		globalWorkDir(roomRunGlobalFlags(c)),
		globalAllowPaths(roomRunGlobalFlags(c))...,
	)
	if err != nil {
		return fmt.Errorf("resolve filesystem scope: %w", err)
	}
	replayPath = strings.TrimSpace(replayPath)
	var launchPlan services.RoomLaunchPlan
	var replayPlan services.RoomReplayPlan
	var replayMode bool
	if replayPath != "" {
		if strings.TrimSpace(configPath) != "" || strings.TrimSpace(manifestPath) != "" {
			return fmt.Errorf("%w: --replay cannot be combined with --config or --manifest", services.ErrRoomReplaySourceConflict)
		}
		replayPlan, err = services.LoadRoomReplayPlan(replayPath)
		if err != nil {
			return err
		}
		replayMode = true
	} else {
		launchPlan, err = services.ResolveRoomLaunchPlan(services.RoomLaunchOptions{
			ConfigPath:     configPath,
			ManifestPath:   manifestPath,
			ConfigDir:      roomConfigDir(roomRunGlobalFlags(c)),
			DeviceRegistry: roomRunDeviceRegistry(c),
		})
		if err != nil {
			return err
		}
	}
	roomManifest := replayPlan.Manifest()
	if !replayMode {
		roomManifest = launchPlan.Manifest
	}

	outputExplicit := cmd.Flags().Changed("out")
	if replayMode {
		outputDir = resolveRoomReplayCommandOutputDir(outputDir)
	} else {
		outputDir, err = resolveRoomCommandOutputDir(launchPlan, outputDir, outputExplicit)
		if err != nil {
			return err
		}
	}
	if outputDir != "" {
		if replayMode {
			if err := services.ValidateRoomReplayOutput(replayPlan, outputDir); err != nil {
				return fmt.Errorf("validate --out %q: %w", outputDir, err)
			}
		}
		if err := services.ValidateRoomEvidenceOutput(outputDir); err != nil {
			return fmt.Errorf("validate --out %q: %w", outputDir, err)
		}
	}

	outputLabel := outputDir
	if outputLabel == "" {
		outputLabel = "disabled"
	}
	output := &roomCommandOutput{writer: cmd.OutOrStdout()}
	output.printf("room starting: participants=%d output=%s\n", len(roomManifest.Participants), outputLabel)
	if err := output.err(); err != nil {
		return err
	}
	readyParticipants := 0

	participantIDs := make([]string, 0, len(roomManifest.Participants))
	for _, participant := range roomManifest.Participants {
		participantIDs = append(participantIDs, participant.ID)
	}

	var broker *services.RoomEventBroker
	var eventServer *roomEventServer
	if strings.TrimSpace(streamAddress) != "" {
		broker, err = services.NewRoomEventBroker(participantIDs)
		if err != nil {
			return fmt.Errorf("configure room stream: %w", err)
		}
		eventServer, err = startRoomEventServer(streamAddress, broker)
		if err != nil {
			_ = broker.Close()
			return err
		}
		output.printf("room stream listening: http://%s/events\n", eventServer.listener.Addr().String())
		if output.err() != nil {
			_ = eventServer.shutdown(broker)
			return output.err()
		}
	}

	var newSignalContext RoomSignalContextFunc = defaultRoomSignalContext
	if c != nil && c.signalContext != nil {
		newSignalContext = c.signalContext
	}
	runContext, stopSignals := newSignalContext(parent)
	if runContext == nil {
		if stopSignals != nil {
			stopSignals()
		}
		contextErr := errors.New("room signal context factory returned a nil context")
		if eventServer != nil {
			return errors.Join(contextErr, eventServer.shutdown(broker))
		}
		return contextErr
	}
	if stopSignals == nil {
		stopSignals = func() {}
	}
	defer stopSignals()

	options := services.RoomRunOptions{
		Manifest:         roomManifest,
		ReplayPath:       replayPath,
		OutputDir:        outputDir,
		ConfigDir:        roomConfigDir(roomRunGlobalFlags(c)),
		WorkDir:          filesystemPolicy.PrimaryRoot(),
		AllowPaths:       filesystemPolicy.AdditionalRoots(),
		FilesystemPolicy: filesystemPolicy,
		ReplayPlan:       nil,
		LaunchPlan:       nil,
		Stream:           broker,
		OnDiagnostic: func(participantID string, record services.SessionDiagnosticRecord) {
			writeRoomDiagnosticProgress(output, participantID, record)
		},
		OnParticipantReady: func(ready services.RoomParticipantReady) {
			output.printf("participant %q ready: kind=%s input=%s output=%s provider=%s model=%s\n", ready.ParticipantID, ready.Kind, ready.InputDevice, ready.OutputDevice, ready.Provider, ready.Model)
			readyParticipants++
			if readyParticipants == len(roomManifest.Participants) {
				output.printf("room running: participants=%d\n", len(roomManifest.Participants))
			}
		},
		OnParticipantTerminated: func(result services.RoomParticipantResult) {
			output.printf("participant %q: %s turns=%d connected=%t\n", result.ParticipantID, result.TerminationReason, result.TurnsCompleted, result.Connected)
		},
	}
	if replayMode {
		options.ReplayPlan = &replayPlan
		options.ConfigDir = ""
	} else {
		options.LaunchPlan = &launchPlan
		options.BrowserCapabilitiesFactory = NewRoomParticipantBrowserCapabilitiesFactory(roomConfigDir(roomRunGlobalFlags(c)))
		options.DeviceRegistry = roomRunDeviceRegistry(c)
	}

	var result services.RoomResult
	var runErr error
	if c == nil || c.run == nil {
		runErr = errors.New("room run service is not configured")
	} else {
		result, runErr = c.run(runContext, io.Discard, options)
	}
	if runErr == nil {
		runErr = roomAllParticipantsFailedError(result)
	}

	var streamErr error
	if eventServer != nil {
		streamErr = eventServer.shutdown(broker)
	}

	writeRoomResult(output, result)
	return errors.Join(runErr, streamErr, output.err())
}

// roomAllParticipantsFailedError reports a non-nil error when a room run
// reports success (runErr == nil) but every participant actually failed.
// #321 fault isolation is untouched: it lives entirely inside services.RunRoom
// and keeps one participant's failure from taking down a surviving peer. This
// check runs only after that result comes back, purely to fix the exit code:
// it fires exclusively when there is no surviving peer at all, so a genuine
// partial failure (or full success) still exits 0 exactly as before.
func roomAllParticipantsFailedError(result services.RoomResult) error {
	if len(result.Participants) == 0 {
		return nil
	}
	ids := make([]string, 0, len(result.Participants))
	for id, participant := range result.Participants {
		if participant.TerminationReason != services.ParticipantTerminationError {
			return nil
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	details := make([]string, 0, len(ids))
	for _, id := range ids {
		details = append(details, fmt.Sprintf("%s: %s", id, result.Participants[id].Error))
	}
	return fmt.Errorf("room run: all %d participant(s) failed (%s)", len(ids), strings.Join(details, "; "))
}

func resolveRoomReplayCommandOutputDir(requested string) string {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return DefaultRoomOutputDir
	}
	return requested
}

func resolveRoomCommandOutputDir(plan services.RoomLaunchPlan, requested string, explicit bool) (string, error) {
	if !plan.Manifest.Room.RecordingEnabled() {
		return "", nil
	}
	if !explicit {
		if destination := plan.Manifest.Room.RecordingDirectory(); destination != "" {
			return destination, nil
		}
		if plan.Mode == services.RoomLaunchModeBare {
			return services.CreateFreshRoomRunDirectory(plan.ConfigDir)
		}
	}
	requested = strings.TrimSpace(requested)
	if requested == "" {
		requested = DefaultRoomOutputDir
	}
	return requested, nil
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

func roomRunDeviceRegistry(command *RoomRunCommand) audio.DeviceRegistry {
	if command == nil {
		return nil
	}
	return command.deviceRegistry
}

func roomConfigDir(globalFlags *flags.GlobalFlags) string {
	if globalFlags == nil {
		return ""
	}
	return globalFlags.ConfigDir()
}

func writeRoomDiagnosticProgress(output *roomCommandOutput, participantID string, record services.SessionDiagnosticRecord) {
	if output == nil {
		return
	}
	switch record.Event {
	case services.SessionDiagnosticEventTurn:
		turn := record.Fields["turn_index"]
		if turn == "" {
			turn = "?"
		}
		output.printf("participant %q: %s turn=%s\n", participantID, record.Event, turn)
	case services.SessionDiagnosticEventToolCall:
		output.printf("participant %q: %s\n", participantID, record.Event)
	case services.SessionDiagnosticEventFailure:
		output.printf("participant %q: %s\n", participantID, record.Event)
	}
}

func writeRoomResult(output *roomCommandOutput, result services.RoomResult) {
	if output == nil {
		return
	}
	reason := result.TerminationReason
	if reason == "" {
		reason = result.Reason
	}
	if reason == "" {
		reason = services.RoomTerminationFailed
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

type roomCommandOutput struct {
	mu       sync.Mutex
	writer   io.Writer
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
	_, o.writeErr = fmt.Fprintf(o.writer, format, args...)
}

func (o *roomCommandOutput) err() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.writeErr
}

type roomEventServer struct {
	server   *http.Server
	listener net.Listener
	done     chan error
}

func startRoomEventServer(address string, broker *services.RoomEventBroker) (*roomEventServer, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return nil, errors.New("room stream address is empty")
	}
	if broker == nil {
		return nil, errors.New("room stream broker is nil")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listen room stream on %q: %w", address, err)
	}
	server := &http.Server{
		Handler:           broker,
		ReadHeaderTimeout: 5 * time.Second,
	}
	eventServer := &roomEventServer{
		server:   server,
		listener: listener,
		done:     make(chan error, 1),
	}
	go func() {
		serveErr := server.Serve(listener)
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		eventServer.done <- serveErr
	}()
	return eventServer, nil
}

func (s *roomEventServer) shutdown(broker *services.RoomEventBroker) error {
	if s == nil {
		return nil
	}
	if broker != nil {
		_ = broker.Close()
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), roomStreamShutdownTimeout)
	defer cancel()
	shutdownErr := s.server.Shutdown(shutdownContext)
	if shutdownErr != nil {
		_ = s.server.Close()
	}
	serveErr := <-s.done
	return errors.Join(shutdownErr, serveErr)
}
