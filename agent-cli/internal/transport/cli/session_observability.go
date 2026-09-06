package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	serviceSelfPlay "github.com/portpowered/go-agent-harness/agent-cli/internal/services/selfplay"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli/internal/livehost"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	runtimeProviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	runtimeRecording "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// LiveCredentialReference stores a host-owned credential and returns an
// opaque selector suitable for session.LiveRequest. The raw credential never
// crosses the runtime request or event boundary.
type LiveCredentialReference func(string) string

// FileDeviceService is a named composition edge for finite file media. The
// name keeps the generated host graph distinct from the registry-backed
// devices.Service while the command itself stores only the common contract.
type FileDeviceService struct {
	runtimeDevices.Service
	Scheduler clock.Scheduler
}

// NewSessionCommand creates the session command with both public service
// contracts. Tests pass nil for the self-play service when they do not invoke
// that subcommand.
func NewSessionCommand(
	askFlags *flags.AskFlags,
	globalFlags *flags.GlobalFlags,
	sessionService serviceSession.Service,
	selfPlayService serviceSelfPlay.Service,
) *SessionCommand {
	return &SessionCommand{
		askFlags: askFlags, globalFlags: globalFlags, sessionService: sessionService,
		selfPlayService: selfPlayService,
	}
}

// NewSessionCommandWithLive adds the reusable continuous-session and device
// roles to the CLI host adapter. Text and finite/replay modes keep using the
// existing SessionService until their presentation adapters are migrated;
// bare and passive live invocations are admitted through OpenLive.
func NewSessionCommandWithLive(
	askFlags *flags.AskFlags,
	globalFlags *flags.GlobalFlags,
	sessionService serviceSession.Service,
	selfPlayService serviceSelfPlay.Service,
	liveService runtimeSession.LiveService,
	liveReplayService runtimeReplay.Service,
	deviceService runtimeDevices.Service,
	fileDeviceService FileDeviceService,
	liveCapabilities SessionToolCapabilitiesFactory,
	credentialReference LiveCredentialReference,
	storeFactory runtimeSession.FileStoreFactory,
	recordingService runtimeRecording.Service,
	modelAdmission runtimeProviders.ModelAdmission,
) *SessionCommand {
	return &SessionCommand{
		askFlags: askFlags, globalFlags: globalFlags, sessionService: sessionService,
		selfPlayService: selfPlayService, liveService: liveService,
		liveReplayService: liveReplayService,
		deviceService:     deviceService, fileDeviceService: fileDeviceService, liveCapabilities: liveCapabilities,
		liveCredentialReference: credentialReference, storeFactory: storeFactory, recordingService: recordingService, modelAdmission: modelAdmission,
	}
}

// runtimeLiveAdmission classifies an explicit replay through the replay
// service before the host opens files, devices, or a provider. It keeps route
// selection independent of capture JSON and gives invalid archives one
// authoritative error path.
func (c *SessionCommand) runtimeLiveAdmission(ctx context.Context, request serviceSession.Request) (bool, *runtimeReplay.CaptureInspection, error) {
	if strings.EqualFold(strings.TrimSpace(request.Transport), SessionTransportWebRTC) || c.liveService == nil {
		return false, nil, nil
	}
	if strings.TrimSpace(request.ReplayPath) == "" {
		return true, nil, nil
	}
	if c.liveReplayService == nil {
		// Preserve the runtime route so the invocation reports the missing
		// replay role instead of silently selecting the legacy session graph.
		return true, nil, nil
	}
	inspection, err := c.liveReplayService.InspectCapture(ctx, request.ReplayPath)
	if err != nil {
		return false, nil, fmt.Errorf("replay session capture %s: %w", request.ReplayPath, err)
	}
	return inspection.IsRealtime(), &inspection, nil
}

// runRuntimeLiveSession is the CLI host adapter for a complete continuous
// invocation. It resolves config values and renders typed observations; the
// runtime LiveRunner owns provider/device admission, pumps, cancellation, and
// terminal joining.
func (c *SessionCommand) runRuntimeLiveSession(ctx context.Context, out io.Writer, request serviceSession.Request, inspections ...*runtimeReplay.CaptureInspection) error {
	return c.runRuntimeLiveSessionWithAnnouncements(ctx, out, out, request, inspections...)
}

func (c *SessionCommand) runRuntimeLiveSessionWithAnnouncements(ctx context.Context, out, announcementOut io.Writer, request serviceSession.Request, inspections ...*runtimeReplay.CaptureInspection) error {
	var replayInspection *runtimeReplay.CaptureInspection
	if len(inspections) > 0 {
		replayInspection = inspections[0]
	}
	if request.AudioOutputPath != "-" {
		// Text and file-output sessions retain the established stdout startup
		// contract. A dash output is the duplex PCM transport, so diagnostics
		// belong on stderr and stdout must remain byte-clean.
		announcementOut = out
	}
	return livehost.Run(ctx, out, request, livehost.Dependencies{
		LiveService:        c.liveService,
		ReplayInspection:   replayInspection,
		BuildRequest:       c.runtimeLiveRequest,
		WriteAnnouncements: writeRuntimeLiveAnnouncements,
		AnnouncementOutput: announcementOut,
		DeviceService:      c.deviceService,
		FileDeviceService:  livehost.FileDeviceService{Service: c.fileDeviceService.Service, Scheduler: c.fileDeviceService.Scheduler},
		RecordingService:   c.recordingService,
		CredentialValues:   runtimeLiveCredentialValues,
	})
}

func (c *SessionCommand) runtimeLiveRequest(ctx context.Context, request serviceSession.Request, replayInspection *runtimeReplay.CaptureInspection) (runtimeSession.LiveRequest, error) {
	return livehost.BuildRequest(ctx, request, replayInspection, livehost.RequestDependencies{
		ReplayService:       c.liveReplayService,
		ModelAdmission:      c.modelAdmission,
		CredentialReference: c.liveCredentialReference,
		Capabilities:        c.runtimeLiveCapabilities,
		BindImagePreparer:   bindRuntimeLiveImagePreparer,
		OpenImages:          openRuntimeLiveImages,
	})
}

func runtimeLiveCredentialValues(request serviceSession.Request) ([]string, error) {
	if request.LoadedConfig == nil {
		return nil, errors.New("live session configuration is unavailable")
	}
	effective := request.LoadedConfig.ApplyOverrides("", request.Model, request.Provider, request.BaseURL)
	_, _, apiKey, _, err := livehost.ProviderValues(effective, request, nil)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, nil
	}
	return []string{apiKey}, nil
}

func (c *SessionCommand) runtimeLiveCapabilities(cfg *config.Config) (*runtimeSession.LiveCapabilities, error) {
	if c.liveCapabilities == nil || cfg == nil {
		return nil, nil
	}
	capabilities, err := c.liveCapabilities(cfg)
	if err != nil {
		return nil, err
	}
	binding := &runtimeSession.LiveCapabilities{
		Executor:           capabilities.Executor,
		Definitions:        append([]messages.ToolDefinition(nil), capabilities.Definitions...),
		InheritDefaults:    false,
		Initialize:         capabilities.Initialize,
		Close:              capabilities.Close,
		RefreshDefinitions: capabilities.RefreshDefinitionsWithError,
	}
	if binding.RefreshDefinitions == nil && capabilities.RefreshDefinitions != nil {
		binding.RefreshDefinitions = func(ctx context.Context) ([]messages.ToolDefinition, error) {
			return capabilities.RefreshDefinitions(ctx), nil
		}
	}
	if capabilities.BrowserEventWatch != nil {
		binding.BrowserWatch = livehost.MapBrowserEvents(capabilities.BrowserEventWatch)
	} else if capabilities.BrowserWatch != nil {
		binding.BrowserWatch = livehost.MapBrokerEvents(capabilities.BrowserWatch)
	}
	return binding, nil
}

// writeRuntimeLiveAnnouncements preserves the CLI's operator-facing startup
// contract while keeping filesystem policy and tool names out of the neutral
// runtime request. The reusable service receives only the already-composed
// capability binding; the host prints its immutable scope before provider
// output can start.
func writeRuntimeLiveAnnouncements(out io.Writer, request serviceSession.Request, liveRequest runtimeSession.LiveRequest, replayInspection *runtimeReplay.CaptureInspection) error {
	if out == nil {
		return errors.New("live output writer is nil")
	}
	replayPath := liveRequest.Replay.InputCapturePath
	if replayPath != "" && replayInspection != nil && replayInspection.IntegrityWarning != "" {
		if _, err := fmt.Fprintln(out, replayInspection.IntegrityWarning); err != nil {
			return err
		}
	}
	policy, err := cliTools.ResolveFilesystemPolicy(request.WorkDir, request.AllowPaths...)
	if err != nil {
		return fmt.Errorf("resolve live filesystem scope: %w", err)
	}
	if _, err := fmt.Fprintln(out, "Filesystem scope: "+policy.ScopeDescription()); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, cliTools.FilesystemScopeStartupNotice); err != nil {
		return err
	}

	definitions := []messages.ToolDefinition(nil)
	if liveRequest.Capabilities != nil {
		definitions = liveRequest.Capabilities.Definitions
	}
	definitions = announcedReplayTools(definitions, replayInspection)
	definitions = messages.CanonicalToolDefinitions(definitions)
	names := make([]string, 0, len(definitions))
	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		name := strings.TrimSpace(definition.Name)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	if len(names) == 0 {
		_, err = fmt.Fprintln(out, "Tools: none")
		return err
	}
	_, err = fmt.Fprintln(out, "Tools: "+strings.Join(names, ", "))
	return err
}

// openRuntimeLiveImages resolves the command's image paths at the host edge.
// The reusable live runtime receives immutable content parts and never reads
// host paths or performs MIME discovery itself.
func openRuntimeLiveImages(paths []string) ([]messages.ContentPart, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	parts := make([]messages.ContentPart, 0, len(paths))
	for _, path := range paths {
		part, err := openRuntimeLiveImage(path)
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
	return parts, nil
}

func openRuntimeLiveImage(path string) (messages.ImagePart, error) {
	if strings.TrimSpace(path) == "" {
		return messages.ImagePart{}, fmt.Errorf("--image path is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return messages.ImagePart{}, fmt.Errorf("session image %q is missing: %w", path, err)
		}
		return messages.ImagePart{}, fmt.Errorf("session image %q cannot be read: %w", path, err)
	}
	if len(data) == 0 {
		return messages.ImagePart{}, fmt.Errorf("image %q is empty", path)
	}
	mediaType := http.DetectContentType(data)
	if mediaType != "image/png" && mediaType != "image/jpeg" {
		return messages.ImagePart{}, fmt.Errorf("image %q has unsupported MIME type %q (supported: image/png, image/jpeg)", path, mediaType)
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return messages.ImagePart{}, fmt.Errorf("image %q is not valid %s content: %w", path, mediaType, err)
	}
	return messages.ImagePart{Bytes: data, MediaType: mediaType}, nil
}

// bindRuntimeLiveImagePreparer gives a live participant's read_image route the
// same host-side image validation used for opening an initial image turn. The
// runtime service performs the filesystem authorization before invoking this
// callback; the callback only resolves the already-authorized bytes into the
// provider-neutral typed part.
func bindRuntimeLiveImagePreparer(executor messages.ToolExecutor) messages.ToolExecutor {
	binder, ok := executor.(runtimeTools.SessionImagePreparerBinder)
	if !ok {
		return executor
	}
	return binder.WithSessionImagePreparer(func(paths []string) ([]messages.ImagePart, error) {
		content, err := openRuntimeLiveImages(paths)
		if err != nil {
			return nil, err
		}
		parts := make([]messages.ImagePart, 0, len(content))
		for _, item := range content {
			part, ok := item.(messages.ImagePart)
			if !ok {
				return nil, fmt.Errorf("live image preparer received unexpected content part %T", item)
			}
			part.Bytes = append([]byte(nil), part.Bytes...)
			parts = append(parts, part)
		}
		return parts, nil
	})
}

// announcedReplayTools shows the initially advertised tools that the current
// host can execute. Recorded metadata cannot expand the current allowlist.
func announcedReplayTools(definitions []messages.ToolDefinition, inspection *runtimeReplay.CaptureInspection) []messages.ToolDefinition {
	if inspection == nil || !inspection.InitialToolsKnown {
		return definitions
	}
	names := make(map[string]bool, len(inspection.InitialTools))
	for _, name := range inspection.InitialTools {
		names[name] = true
	}
	selected := make([]messages.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if names[definition.Name] {
			selected = append(selected, definition)
		}
	}
	return selected
}
