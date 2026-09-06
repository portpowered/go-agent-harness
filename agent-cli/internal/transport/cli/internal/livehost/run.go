package livehost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	cliOutput "github.com/portpowered/go-agent-harness/agent-cli/internal/output"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	runtimeRecording "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// FileDeviceService is the host-composed file media service and its timing
// role. The reusable runtime receives only the device service contract.
type FileDeviceService struct {
	Service   runtimeDevices.Service
	Scheduler clock.Scheduler
}

// RequestBuilder resolves CLI configuration into the neutral runtime request.
// It is deliberately a callback so this host package does not own provider,
// capability, or prompt policy.
type RequestBuilder func(context.Context, serviceSession.Request, *runtimeReplay.CaptureInspection) (runtimeSession.LiveRequest, error)

// AnnouncementWriter owns operator-facing startup text at the CLI boundary.
type AnnouncementWriter func(io.Writer, serviceSession.Request, runtimeSession.LiveRequest, *runtimeReplay.CaptureInspection) error

// Dependencies are the explicit host edges needed to run one live session.
// No process-wide discovery or default runtime graph is performed here.
type Dependencies struct {
	LiveService        runtimeSession.LiveService
	ReplayInspection   *runtimeReplay.CaptureInspection
	BuildRequest       RequestBuilder
	WriteAnnouncements AnnouncementWriter
	// AnnouncementOutput keeps operator-facing startup text off a binary audio
	// stream. When unset, Run preserves the historical behavior of writing
	// announcements to out.
	AnnouncementOutput io.Writer
	DeviceService      runtimeDevices.Service
	FileDeviceService  FileDeviceService
	RecordingService   runtimeRecording.Service
	CredentialValues   func(serviceSession.Request) ([]string, error)
	CaptureComplete    func(serviceSession.Request) []runtimeSession.LiveControl
}

// Run admits a single host invocation into the reusable live runtime. File
// sources and sinks are opened before admission and remain host-owned until
// this function joins the runtime. Provider, media, and terminal lifecycle
// policy remains in runtimeSession.LiveRunner.
func Run(ctx context.Context, out io.Writer, request serviceSession.Request, deps Dependencies) (runErr error) {
	runner, err := liveRunner(deps.LiveService)
	if err != nil {
		return err
	}
	if deps.BuildRequest == nil {
		return errors.New("live request builder is unavailable")
	}
	liveRequest, err := deps.BuildRequest(ctx, request, deps.ReplayInspection)
	if err != nil {
		return err
	}
	if deps.WriteAnnouncements != nil {
		announcementOut := deps.AnnouncementOutput
		if announcementOut == nil {
			announcementOut = out
		}
		if err := deps.WriteAnnouncements(announcementOut, request, liveRequest, deps.ReplayInspection); err != nil {
			return err
		}
	}
	recorder, err := openRecorder(request, &liveRequest, deps)
	if err != nil {
		return err
	}
	finishRecorder := func(cause error) error {
		if recorder == nil {
			return cause
		}
		return errors.Join(cause, recorder.Finalize(context.WithoutCancel(ctx), cause))
	}
	filePorts, err := OpenFilePorts(request, out, liveRequest.OutputAudioSampleRate)
	if err != nil {
		return finishRecorder(err)
	}
	if filePorts != nil {
		defer func() { runErr = errors.Join(runErr, filePorts.Close()) }()
	}
	configureLegacyReplayInput(filePorts, request, liveRequest)
	options := liveRunOptions(out, request, liveRequest, recorder, filePorts, deps)
	return suppressExpectedDuration(runner.RunLive(ctx, options))
}

// suppressExpectedDuration keeps the CLI's historical exit contract for an
// explicit max-duration stop while preserving every independent lifecycle
// failure joined by the runtime. The terminal event and recording still carry
// the duration classification; only the process exit value is translated.
func suppressExpectedDuration(err error) error {
	if err == nil {
		return nil
	}
	// Leave unrelated typed causes untouched. In particular, a scheduled
	// audio error carries its own concrete counters through Unwrap; traversing
	// that wrapper merely because it has an Unwrap method would replace the
	// type with the sentinel and break errors.As for the caller.
	if !errors.Is(err, runtimeSession.ErrLiveDurationExceeded) {
		return err
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		return retainNonDurationCauses(joined.Unwrap())
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		cause := wrapped.Unwrap()
		if cause != nil {
			retained := suppressExpectedDuration(cause)
			if retained == nil {
				return nil
			}
			// Preserve the outer operation context while exposing the retained
			// cause to errors.Is/errors.As. This matters when a fmt.Errorf
			// wrapper surrounds an errors.Join(duration, independentFailure).
			return retainedLiveError{message: err.Error(), cause: retained}
		}
	}
	return nil
}

func retainNonDurationCauses(causes []error) error {
	kept := make([]error, 0, len(causes))
	for _, cause := range causes {
		if retained := suppressExpectedDuration(cause); retained != nil {
			kept = append(kept, retained)
		}
	}
	return errors.Join(kept...)
}

type retainedLiveError struct {
	message string
	cause   error
}

func (e retainedLiveError) Error() string { return e.message }

func (e retainedLiveError) Unwrap() error { return e.cause }

func liveRunner(service runtimeSession.LiveService) (runtimeSession.LiveRunner, error) {
	if service == nil {
		return nil, errors.New("live session runner is not configured")
	}
	runner, ok := service.(runtimeSession.LiveRunner)
	if !ok || runner == nil {
		return nil, errors.New("live session runner is not configured")
	}
	return runner, nil
}

func openRecorder(request serviceSession.Request, liveRequest *runtimeSession.LiveRequest, deps Dependencies) (runtimeSession.LiveRecorder, error) {
	if request.RecordDirectory == "" {
		return openSemanticRecorder(request.RecordPath, deps.RecordingService)
	}
	if deps.RecordingService == nil {
		return nil, errors.New("live recording service is unavailable")
	}
	if deps.CredentialValues == nil {
		return nil, errors.New("live credential resolver is unavailable")
	}
	replayInputPath := ""
	if liveRequest != nil {
		replayInputPath = strings.TrimSpace(liveRequest.Replay.InputCapturePath)
	}
	providerCapturePath := request.RecordPath
	if providerCapturePath == "" && replayInputPath != "" {
		// A replay-backed directory has a verified provider capture already. Use
		// that source as the immutable provider artifact instead of asking an
		// injected replay session to manufacture a second raw capture.
		providerCapturePath = replayInputPath
	}
	credentialRequest := request
	credentialRequest.ReplayPath = liveRequest.Replay.InputCapturePath
	credentialRequest.Provider = liveRequest.Provider
	credentialRequest.Model = liveRequest.Model
	credentialRequest.BaseURL = liveRequest.BaseURL
	credentials, err := deps.CredentialValues(credentialRequest)
	if err != nil {
		return nil, err
	}
	recorder, err := deps.RecordingService.OpenLiveEvidence(runtimeRecording.LiveEvidenceOptions{
		Destination:         request.RecordDirectory,
		SessionID:           liveRequest.SessionID,
		ParticipantID:       liveRequest.ParticipantID,
		Provider:            liveRequest.Provider,
		Model:               liveRequest.Model,
		Credentials:         credentials,
		ProviderCapturePath: providerCapturePath,
	})
	if err != nil {
		return nil, fmt.Errorf("open live recording: %w", err)
	}
	if request.RecordPath == "" && replayInputPath == "" {
		if providerCapture, ok := recorder.(runtimeRecording.ProviderCapture); ok {
			if path := strings.TrimSpace(providerCapture.ProviderCapturePath()); path != "" {
				configureCapturePath(liveRequest, path)
			}
		}
	}
	return recorder, nil
}

func openSemanticRecorder(recordPath string, service runtimeRecording.Service) (runtimeSession.LiveRecorder, error) {
	if recordPath == "" {
		return nil, nil
	}
	if service == nil {
		return nil, errors.New("live recording service is unavailable")
	}
	recorder, err := service.OpenLiveSemanticEvidence(recordPath)
	if err != nil {
		return nil, fmt.Errorf("open live semantic recording: %w", err)
	}
	return recorder, nil
}

// configureCapturePath exists as a narrow hook for the caller-owned request
// copy. The recorder path is applied in Run before options are built.
func configureCapturePath(request *runtimeSession.LiveRequest, path string) {
	if request == nil || path == "" {
		return
	}
	request.Replay.OutputCapturePath = path
}

func configureLegacyReplayInput(filePorts *FilePorts, request serviceSession.Request, liveRequest runtimeSession.LiveRequest) {
	if filePorts == nil || filePorts.Input == nil || request.ReplayPath == "" || liveRequest.ReplayPlan == nil {
		return
	}
	if liveRequest.ReplayPlan.InputAudioSampleRate <= 0 {
		UseLegacyFrameSource(filePorts.Input)
	}
}

func liveRunOptions(out io.Writer, request serviceSession.Request, liveRequest runtimeSession.LiveRequest, recorder runtimeSession.LiveRecorder, filePorts *FilePorts, deps Dependencies) runtimeSession.LiveRunOptions {
	deviceService := deps.DeviceService
	deviceRequest := devicesRequest(request, liveRequest)
	if filePorts != nil {
		applyFileSchedulers(filePorts, deps.FileDeviceService.Scheduler)
		deviceRequest.FileInput = filePorts.Input
		deviceRequest.FileOutput = filePorts.Output
		deviceService, deviceRequest = selectFileDevices(deviceService, deps.FileDeviceService.Service, deviceRequest, filePorts)
	}
	if !deviceRequest.CaptureEnabled && !deviceRequest.PlaybackEnabled && (filePorts == nil || len(filePorts.InputTurns) == 0) {
		deviceService = nil
	}
	renderer := cliOutput.NewLiveEventRenderer(request.ReplayPath != "")
	return runtimeSession.LiveRunOptions{
		Request:                 liveRequest,
		Devices:                 deviceService,
		DeviceRequest:           deviceRequest,
		AudioTurnAdmission:      audioTurnAdmission(request),
		Recorder:                recorder,
		CaptureTurns:            captureTurns(filePorts),
		CaptureCompleteControls: captureCompleteControls(request, deps.CaptureComplete),
		Events: runtimeSession.LiveEventSinkFunc(func(eventContext context.Context, event runtimeSession.LiveEvent) error {
			eventOut := outputWriter(request, out)
			if err := renderer.Render(eventContext, eventOut, event); err != nil {
				return err
			}
			if request.StreamObserver != nil && event.Message != nil {
				request.StreamObserver(*event.Message)
			}
			return nil
		}),
	}
}

func selectFileDevices(physical, finite runtimeDevices.Service, deviceRequest runtimeDevices.Request, filePorts *FilePorts) (runtimeDevices.Service, runtimeDevices.Request) {
	if filePorts == nil {
		return physical, deviceRequest
	}
	if filePorts.Input != nil {
		// The public device handle exposes one capture port. A finite source
		// therefore owns capture whenever it is present; an explicit physical
		// output can still be admitted alongside it.
		deviceRequest.CaptureEnabled = false
	}
	if deviceRequest.CaptureEnabled || deviceRequest.PlaybackEnabled {
		return physical, deviceRequest
	}
	if filePorts.Input != nil || filePorts.Output != nil {
		deviceRequest.CaptureEnabled = filePorts.Input != nil
		deviceRequest.PlaybackEnabled = filePorts.Output != nil
		return finite, deviceRequest
	}
	if len(filePorts.InputTurns) > 0 {
		return finite, deviceRequest
	}
	return nil, deviceRequest
}

func outputWriter(request serviceSession.Request, out io.Writer) io.Writer {
	if request.AudioOutputPath == "-" {
		return io.Discard
	}
	return out
}

func devicesRequest(request serviceSession.Request, liveRequest runtimeSession.LiveRequest) runtimeDevices.Request {
	sampleRate := liveRequest.InputAudioSampleRate
	if sampleRate <= 0 {
		sampleRate = liveRequest.OutputAudioSampleRate
	}
	if sampleRate <= 0 {
		sampleRate = 24000
	}
	return runtimeDevices.Request{
		InputDevice:     request.AudioInputDevice,
		OutputDevice:    request.AudioOutputDevice,
		RemoteEndpoint:  request.AudioDeviceServer,
		CaptureEnabled:  request.InteractiveDevices || request.AudioInputDevicePresent,
		PlaybackEnabled: request.InteractiveDevices || request.AudioOutputDevicePresent,
		SampleRate:      sampleRate,
		Channels:        audio.Channels,
		PlaybackProfile: "voice",
	}
}

func applyFileSchedulers(filePorts *FilePorts, scheduler clock.Scheduler) {
	if filePorts == nil {
		return
	}
	if filePorts.Input != nil {
		filePorts.Input.Scheduler = scheduler
	}
	for index := range filePorts.InputTurns {
		filePorts.InputTurns[index].Scheduler = scheduler
	}
}

func audioTurnAdmission(request serviceSession.Request) runtimeSession.AudioTurnAdmission {
	if request.AudioInTurnBarge {
		return runtimeSession.AudioTurnAdmissionBarge
	}
	return runtimeSession.AudioTurnAdmissionCompletionGated
}

func captureTurns(filePorts *FilePorts) []runtimeDevices.FileInput {
	if filePorts == nil {
		return nil
	}
	return append([]runtimeDevices.FileInput(nil), filePorts.InputTurns...)
}

func captureCompleteControls(request serviceSession.Request, custom func(serviceSession.Request) []runtimeSession.LiveControl) []runtimeSession.LiveControl {
	if custom != nil {
		return custom(request)
	}
	if !request.AudioInput.Present && len(request.AudioTurns) == 0 {
		return nil
	}
	return []runtimeSession.LiveControl{{Kind: runtimeSession.LiveControlAudioCommit}}
}
