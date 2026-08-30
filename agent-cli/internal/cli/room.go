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
	"github.com/spf13/cobra"
)

const (
	// DefaultRoomOutputDir is the deterministic evidence directory used when
	// --out is omitted. The normal room service still requires it to be empty.
	DefaultRoomOutputDir = services.DefaultRoomOutputDir

	roomStreamShutdownTimeout = 5 * time.Second
)

// RoomRunFunc is the service seam used by the room command. Keeping the seam
// at the structured-result boundary makes command tests independent of live
// provider credentials and network connections.
type RoomRunFunc func(context.Context, io.Writer, services.RoomRunOptions) (services.RoomResult, error)

// RoomRunCommand implements `agent room run`.
type RoomRunCommand struct {
	globalFlags    *flags.GlobalFlags
	deviceRegistry audio.DeviceRegistry
	run            RoomRunFunc
}

// NewRoomRunCommand creates the room runner command using the host audio
// registry. An explicit registry can be supplied through
// NewRoomRunCommandWithDeviceRegistry for hermetic composition tests.
func NewRoomRunCommand(globalFlags *flags.GlobalFlags) *RoomRunCommand {
	return &RoomRunCommand{
		globalFlags:    globalFlags,
		deviceRegistry: newDefaultDeviceRegistry(),
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

// RoomCommand is the parent `agent room` command.
type RoomCommand struct{}

// NewRoomCommand creates the room command group.
func NewRoomCommand() *RoomCommand { return &RoomCommand{} }

// Generate returns the room command group.
func (c *RoomCommand) Generate() *cobra.Command {
	return &cobra.Command{
		Use:   "room",
		Short: "Run participant rooms",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
}

// Generate returns the room run command.
func (c *RoomRunCommand) Generate() *cobra.Command {
	var configPath string
	var manifestPath string
	var outputDir string
	var streamAddress string

	cmd := &cobra.Command{
		Use:   "run [--config <file>]",
		Short: "Run a room, or start the bare customer-plus-agent room",
		Long: "Run an N-participant room from --config (or the legacy --manifest spelling). " +
			"With neither flag, start the interactive room with one human customer on the host default microphone and speakers and one OpenAI realtime agent. " +
			"An explicit --config is authoritative and overrides bare defaults. Validate a complete room manifest, start one isolated live session per participant, " +
			"and write redacted evidence to an empty output directory. An optional HTTP " +
			"listener exposes forward-only JSON events at /events.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return c.execute(cmd, configPath, manifestPath, outputDir, streamAddress)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "Path to the authoritative schema-version-1 JSON or YAML room config; omit for bare defaults")
	cmd.Flags().StringVar(&manifestPath, "manifest", "", "Path to the schema-version-1 JSON or YAML room manifest")
	cmd.Flags().StringVar(&outputDir, "out", DefaultRoomOutputDir, "Empty directory for redacted room evidence (default: room-run)")
	cmd.Flags().StringVar(&streamAddress, "stream", "", "Optional TCP listen address for GET /events (for example 127.0.0.1:8080)")
	return cmd
}

func (c *RoomRunCommand) execute(cmd *cobra.Command, configPath, manifestPath, outputDir, streamAddress string) error {
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	launchPlan, err := services.ResolveRoomLaunchPlan(services.RoomLaunchOptions{
		ConfigPath:     configPath,
		ManifestPath:   manifestPath,
		ConfigDir:      roomConfigDir(roomRunGlobalFlags(c)),
		DeviceRegistry: roomRunDeviceRegistry(c),
	})
	if err != nil {
		return err
	}
	manifest := launchPlan.Manifest

	outputDir = strings.TrimSpace(outputDir)
	if outputDir == "" {
		outputDir = DefaultRoomOutputDir
	}
	if err := services.ValidateRoomEvidenceOutput(outputDir); err != nil {
		return fmt.Errorf("validate --out %q: %w", outputDir, err)
	}

	output := &roomCommandOutput{writer: cmd.OutOrStdout()}
	output.printf("room starting: participants=%d output=%s\n", len(manifest.Participants), outputDir)
	if err := output.err(); err != nil {
		return err
	}

	participantIDs := make([]string, 0, len(manifest.Participants))
	for _, participant := range manifest.Participants {
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

	runContext, stopSignals := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	options := services.RoomRunOptions{
		Manifest:                   manifest,
		LaunchPlan:                 &launchPlan,
		OutputDir:                  outputDir,
		ConfigDir:                  roomConfigDir(roomRunGlobalFlags(c)),
		BrowserCapabilitiesFactory: NewRoomParticipantBrowserCapabilitiesFactory(roomConfigDir(roomRunGlobalFlags(c))),
		Stream:                     broker,
		OnDiagnostic: func(participantID string, record services.SessionDiagnosticRecord) {
			writeRoomDiagnosticProgress(output, participantID, record)
		},
		OnParticipantTerminated: func(result services.RoomParticipantResult) {
			output.printf("participant %q: %s turns=%d connected=%t\n", result.ParticipantID, result.TerminationReason, result.TurnsCompleted, result.Connected)
		},
	}

	var result services.RoomResult
	var runErr error
	if c == nil || c.run == nil {
		runErr = errors.New("room run service is not configured")
	} else {
		result, runErr = c.run(runContext, io.Discard, options)
	}

	var streamErr error
	if eventServer != nil {
		streamErr = eventServer.shutdown(broker)
	}

	writeRoomResult(output, result)
	return errors.Join(runErr, streamErr, output.err())
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
	output.printf("room stopped: reason=%s participants=%d\n", reason, len(result.Participants))
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
		output.printf("participant %q: %s turns=%d connected=%t\n", participantID, participantReason, participant.TurnsCompleted, participant.Connected)
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
