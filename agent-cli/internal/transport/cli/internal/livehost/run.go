package livehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	cliOutput "github.com/portpowered/go-agent-harness/agent-cli/internal/output"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	runtimeRecording "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	runtimeSessionTrace "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
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
	TraceService       runtimeSessionTrace.Service
}

// Run admits a single host invocation into the reusable live runtime. File
// sources and sinks are opened before admission and remain host-owned until
// this function joins the runtime. Provider, media, and terminal lifecycle
// policy remains in runtimeSession.LiveRunner.
func Run(ctx context.Context, out io.Writer, request serviceSession.Request, deps Dependencies) (runErr error) {
	traceRequested := request.TraceAudio || strings.TrimSpace(request.RecordDirectory) != ""
	if traceRequested && deps.TraceService == nil {
		return errors.New("live session trace service is unavailable")
	}
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
	credentials, credentialsReady, err := resolveTraceCredentials(request, &liveRequest, deps, traceRequested)
	if err != nil {
		return err
	}
	traceRun, err := prepareLiveTrace(request, liveRequest, deps, credentials)
	if err != nil {
		return err
	}
	if traceRun != nil {
		defer func() {
			bundle := strings.TrimSpace(request.RecordDirectory)
			runErr = errors.Join(runErr, traceRun.finish(traceContext(ctx), bundle, runErr == nil, runErr))
		}()
	}
	cleanupImages, err := stageLiveOpeningImages(request, &liveRequest)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, cleanupImages()) }()
	if deps.WriteAnnouncements != nil {
		announcementOut := deps.AnnouncementOutput
		if announcementOut == nil {
			announcementOut = out
		}
		if err := deps.WriteAnnouncements(announcementOut, request, liveRequest, deps.ReplayInspection); err != nil {
			return err
		}
	}
	recorder, err := openRecorder(request, &liveRequest, deps, credentials, credentialsReady)
	if err != nil {
		return err
	}
	if traceRun != nil {
		recorder = traceRun.wrapRecorder(recorder, liveRequest)
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
	options := liveRunOptions(out, request, liveRequest, recorder, filePorts, deps, traceRun)
	return suppressExpectedDuration(runner.RunLive(ctx, options))
}

func traceContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithoutCancel(ctx)
}

func resolveTraceCredentials(request serviceSession.Request, liveRequest *runtimeSession.LiveRequest, deps Dependencies, traceRequested bool) ([]string, bool, error) {
	if !traceRequested || deps.CredentialValues == nil {
		return nil, false, nil
	}
	values, err := deps.CredentialValues(liveCredentialRequest(request, liveRequest))
	if err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(request.APIKey) != "" {
		values = append(values, request.APIKey)
	}
	return append([]string(nil), values...), true, nil
}

type publicTraceRun struct {
	prepared sessiontracePrepared
	binding  runtimeSessionTrace.DeviceBinding
	observer runtimeSessionTrace.RuntimeObserver

	terminalOnce sync.Once
}

// sessiontracePrepared is kept as the narrow public contract used by the host.
// The alias avoids coupling the host lifecycle to the concrete sessiontrace
// implementation or its Wire package.
type sessiontracePrepared interface {
	DeviceBinding() runtimeSessionTrace.DeviceBinding
	RuntimeObserver() runtimeSessionTrace.RuntimeObserver
	StagedPath() string
	Finish(context.Context, string, bool) error
}

func prepareLiveTrace(request serviceSession.Request, liveRequest runtimeSession.LiveRequest, deps Dependencies, credentials []string) (*publicTraceRun, error) {
	if !request.TraceAudio && strings.TrimSpace(request.RecordDirectory) == "" {
		return nil, nil
	}
	prepared, err := deps.TraceService.Prepare(runtimeSessionTrace.Request{
		TraceAudio:      request.TraceAudio,
		RecordDirectory: request.RecordDirectory,
		Clock:           liveTraceSource(deps.FileDeviceService.Scheduler),
		Credentials:     append([]string(nil), credentials...),
	})
	if err != nil {
		return nil, err
	}
	if prepared == nil {
		return nil, errors.New("live session trace preparation returned nil")
	}
	return &publicTraceRun{
		prepared: prepared,
		binding:  prepared.DeviceBinding(),
		observer: prepared.RuntimeObserver(),
	}, nil
}

func (r *publicTraceRun) finish(ctx context.Context, bundle string, published bool, runErr error) error {
	if r == nil || r.prepared == nil {
		return nil
	}
	r.observeTerminal(runErr)
	return r.prepared.Finish(ctx, bundle, published)
}

func (r *publicTraceRun) observeTerminal(runErr error) {
	if r == nil || r.observer == nil {
		return
	}
	r.terminalOnce.Do(func() {
		r.observer.ObserveSessionRuntime(runtimeSessionTrace.SessionRuntimeObservation{
			Kind:  runtimeSessionTrace.SessionRuntimeObservationTerminal,
			Clean: runErr == nil,
			Error: traceErrorText(runErr),
		})
	})
}

func (r *publicTraceRun) wrapRecorder(inner runtimeSession.LiveRecorder, liveRequest runtimeSession.LiveRequest) runtimeSession.LiveRecorder {
	return &publicTraceRecorder{
		inner:      inner,
		run:        r,
		inputRate:  liveRequest.InputAudioSampleRate,
		outputRate: liveRequest.OutputAudioSampleRate,
		binding:    r.binding,
		observer:   r.observer,
		sequence:   new(atomic.Uint64),
	}
}

type publicTraceRecorder struct {
	inner                 runtimeSession.LiveRecorder
	run                   *publicTraceRun
	binding               runtimeSessionTrace.DeviceBinding
	observer              runtimeSessionTrace.RuntimeObserver
	inputRate, outputRate int
	sequence              *atomic.Uint64
}

func (r *publicTraceRecorder) RecordMessage(ctx context.Context, record runtimeSession.LiveRecord) error {
	innerErr := error(nil)
	if r != nil && r.inner != nil {
		innerErr = r.inner.RecordMessage(ctx, record)
	}
	if r == nil {
		return innerErr
	}
	r.observeMessage(record)
	return innerErr
}

func (r *publicTraceRecorder) RecordAudio(ctx context.Context, record runtimeSession.LiveAudioRecord) error {
	innerErr := error(nil)
	if r != nil && r.inner != nil {
		innerErr = r.inner.RecordAudio(ctx, record)
	}
	if r == nil {
		return innerErr
	}
	frame := record.Frame
	frame.Samples = append([]int16(nil), record.Frame.Samples...)
	record.Frame = frame
	traceErr := r.observeAudio(ctx, record)
	return errors.Join(innerErr, traceErr)
}

func (r *publicTraceRecorder) RecordEvent(ctx context.Context, event runtimeSession.LiveEvent) error {
	innerErr := error(nil)
	if r != nil && r.inner != nil {
		innerErr = r.inner.RecordEvent(ctx, event)
	}
	if r == nil {
		return innerErr
	}
	r.observeEvent(event)
	return innerErr
}

func (r *publicTraceRecorder) Finalize(ctx context.Context, runErr error) error {
	if r == nil {
		return nil
	}
	if r.run != nil {
		r.run.observeTerminal(runErr)
	}
	if r.inner == nil {
		return nil
	}
	return r.inner.Finalize(ctx, runErr)
}

func (r *publicTraceRecorder) observeMessage(record runtimeSession.LiveRecord) {
	payload, marshalErr := json.Marshal(record.Message)
	clean := marshalErr == nil
	if marshalErr != nil {
		payload = nil
	}
	direction := "receive"
	if record.Direction == runtimeSession.LiveRecordClient {
		direction = "send"
	}
	r.observe(runtimeSessionTrace.SessionRuntimeObservation{
		Kind:            runtimeSessionTrace.SessionRuntimeObservationKind("provider_wire_" + direction),
		Timestamp:       record.Timestamp,
		Payload:         payload,
		ResponseID:      record.Message.ResponseID,
		ResponsePurpose: record.Message.ResponsePurpose,
		StreamID:        record.Message.ActorStreamID,
		LoopPassID:      record.Message.LoopPassID,
		Clean:           clean,
		Error:           traceErrorText(marshalErr),
	})
	if special := traceMessageKind(record.Message); special != "" {
		r.observe(runtimeSessionTrace.SessionRuntimeObservation{
			Kind:            runtimeSessionTrace.SessionRuntimeObservationKind(special),
			Timestamp:       record.Timestamp,
			Payload:         payload,
			ResponseID:      record.Message.ResponseID,
			ResponsePurpose: record.Message.ResponsePurpose,
			StreamID:        record.Message.ActorStreamID,
			LoopPassID:      record.Message.LoopPassID,
			Clean:           clean,
			Error:           traceErrorText(marshalErr),
		})
	}
}

func (r *publicTraceRecorder) observeAudio(ctx context.Context, record runtimeSession.LiveAudioRecord) error {
	rate := record.Frame.Format.SampleRate
	if rate <= 0 {
		if record.Direction == runtimeSession.LiveRecordClient {
			rate = r.inputRate
		} else {
			rate = r.outputRate
		}
	}
	samples := append([]int16(nil), record.Frame.Samples...)
	if len(samples) > 0 {
		switch {
		case record.Direction == runtimeSession.LiveRecordClient && record.Admission == runtimeSession.LiveAudioQueueAdmitted:
			if r.binding.PreGateSamplesObserver != nil {
				r.binding.PreGateSamplesObserver(rate, samples)
			}
		case record.Direction == runtimeSession.LiveRecordClient:
			if r.binding.UploadedSamplesObserver != nil {
				r.binding.UploadedSamplesObserver(rate, samples)
			}
		case record.Direction == runtimeSession.LiveRecordAgent:
			if r.binding.PlaybackSamplesObserver != nil {
				if err := r.binding.PlaybackSamplesObserver(ctx, rate, samples); err != nil {
					return err
				}
			}
		}
	}
	payload, marshalErr := json.Marshal(struct {
		Direction string `json:"direction"`
		Admission string `json:"admission"`
		Rate      int    `json:"sample_rate"`
		Samples   int    `json:"sample_count"`
		StreamID  string `json:"stream_id,omitempty"`
		Epoch     uint64 `json:"epoch,omitempty"`
	}{string(record.Direction), string(record.Admission), rate, len(samples), record.Frame.StreamID, record.Frame.Epoch})
	kind := runtimeSessionTrace.SessionRuntimeObservationAudioOutput
	if record.Direction == runtimeSession.LiveRecordClient {
		kind = runtimeSessionTrace.SessionRuntimeObservationAudioInput
	}
	r.observe(runtimeSessionTrace.SessionRuntimeObservation{
		Kind:    kind,
		Payload: payload,
		Epoch:   record.Frame.Epoch,
		Clean:   marshalErr == nil,
		Error:   traceErrorText(marshalErr),
	})
	return marshalErr
}

func (r *publicTraceRecorder) observeEvent(event runtimeSession.LiveEvent) {
	payload, marshalErr := json.Marshal(struct {
		Sequence      uint64 `json:"sequence"`
		Kind          string `json:"kind"`
		SessionID     string `json:"session_id,omitempty"`
		ParticipantID string `json:"participant_id,omitempty"`
		ResponseID    string `json:"response_id,omitempty"`
		ItemID        string `json:"item_id,omitempty"`
		ToolCallID    string `json:"tool_call_id,omitempty"`
		State         string `json:"state,omitempty"`
		Reason        string `json:"reason,omitempty"`
		Dropped       uint64 `json:"dropped,omitempty"`
	}{event.Sequence, event.Kind, event.SessionID, event.ParticipantID, event.ResponseID, event.ItemID, event.ToolCallID, event.State, event.Reason, event.Dropped})
	kind := event.Kind
	if event.Kind == string(runtimeSession.LiveEventTerminal) {
		if r.run != nil {
			r.run.observeTerminal(event.Error)
		}
		return
	}
	r.observe(runtimeSessionTrace.SessionRuntimeObservation{
		Kind:      runtimeSessionTrace.SessionRuntimeObservationKind(kind),
		Timestamp: event.Timestamp,
		Payload:   payload,
		Clean:     event.Error == nil && marshalErr == nil,
		Error:     traceErrorText(event.Error),
	})
}

func (r *publicTraceRecorder) observe(observation runtimeSessionTrace.SessionRuntimeObservation) {
	if r == nil || r.observer == nil {
		return
	}
	if r.sequence != nil {
		observation.Tick = r.sequence.Add(1)
	}
	r.observer.ObserveSessionRuntime(observation)
}

func traceMessageKind(message messages.StreamMessage) string {
	switch {
	case message.Type == messages.StreamTypeAudioStart || message.Type == messages.StreamTypeAudioDelta || message.Type == messages.StreamTypeAudioEnd:
		if message.Role == messages.RoleTool {
			return "tool_result"
		}
		return string(runtimeSessionTrace.SessionRuntimeObservationAudioOutput)
	case message.Type == messages.StreamTypeResponseCreate:
		return string(runtimeSessionTrace.SessionRuntimeObservationResponseCreate)
	case message.Type == messages.StreamTypeInputItemAdded || message.Type == messages.StreamTypeMessageEnd && message.Role == messages.RoleUser:
		return string(runtimeSessionTrace.SessionRuntimeObservationInputCommit)
	case message.Type == messages.StreamTypeMessageEnd:
		return string(runtimeSessionTrace.SessionRuntimeObservationTurnCompleted)
	case message.Type == messages.StreamTypeSessionClose:
		return string(runtimeSessionTrace.SessionRuntimeObservationTerminal)
	case message.Type == messages.StreamTypeToolCallStart || message.Type == messages.StreamTypeToolCallDelta || message.Type == messages.StreamTypeToolCallEnd:
		return "tool_call"
	case message.Role == messages.RoleTool:
		return "tool_result"
	default:
		return ""
	}
}

func traceErrorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type publicTraceDeviceService struct {
	inner runtimeDevices.Service
	trace *publicTraceRun
}

func (s publicTraceDeviceService) Open(ctx context.Context, request runtimeDevices.Request) (runtimeDevices.Handle, error) {
	if s.inner == nil {
		return nil, runtimeDevices.ErrUnavailable
	}
	handle, err := s.inner.Open(ctx, request)
	if err != nil || handle == nil || s.trace == nil || !request.PlaybackEnabled {
		return handle, err
	}
	playback := handle.Media().Playback
	setter, ok := playback.(interface{ SetRenderedSamplesObserver(func(int, []int16)) bool })
	if !ok || !setter.SetRenderedSamplesObserver(s.trace.binding.RenderedSamplesObserver) {
		if s.trace.binding.RenderedSamplesUnavailable != nil {
			s.trace.binding.RenderedSamplesUnavailable()
		}
	}
	return handle, nil
}

func wrapTraceDeviceService(service runtimeDevices.Service, trace *publicTraceRun) runtimeDevices.Service {
	if service == nil || trace == nil {
		return service
	}
	return publicTraceDeviceService{inner: service, trace: trace}
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

func openRecorder(request serviceSession.Request, liveRequest *runtimeSession.LiveRequest, deps Dependencies, credentials []string, credentialsReady bool) (runtimeSession.LiveRecorder, error) {
	if request.RecordDirectory == "" {
		return openSemanticRecorder(request.RecordPath, deps.RecordingService)
	}
	if err := validateLiveRecorderDependencies(deps); err != nil {
		return nil, err
	}
	replayInputPath := liveReplayInputPath(liveRequest)
	if !credentialsReady {
		var err error
		credentials, err = deps.CredentialValues(liveCredentialRequest(request, liveRequest))
		if err != nil {
			return nil, err
		}
	}
	recorder, err := deps.RecordingService.OpenLiveEvidence(runtimeRecording.LiveEvidenceOptions{
		Destination:                   request.RecordDirectory,
		SessionID:                     liveRequest.SessionID,
		ParticipantID:                 liveRequest.ParticipantID,
		Provider:                      liveRequest.Provider,
		Model:                         liveRequest.Model,
		Credentials:                   credentials,
		ProviderCapturePath:           liveProviderCapturePath(request.RecordPath, replayInputPath),
		DisableProviderCaptureSidecar: replayInputPath != "" && request.RecordPath == "",
	})
	if err != nil {
		return nil, fmt.Errorf("open live recording: %w", err)
	}
	configureLiveCapturePath(request, replayInputPath, recorder, liveRequest)
	return recorder, nil
}

func validateLiveRecorderDependencies(deps Dependencies) error {
	if deps.RecordingService == nil {
		return errors.New("live recording service is unavailable")
	}
	if deps.CredentialValues == nil {
		return errors.New("live credential resolver is unavailable")
	}
	return nil
}

func liveReplayInputPath(liveRequest *runtimeSession.LiveRequest) string {
	if liveRequest == nil {
		return ""
	}
	return strings.TrimSpace(liveRequest.Replay.InputCapturePath)
}

func liveProviderCapturePath(recordPath, replayInputPath string) string {
	if recordPath != "" {
		return recordPath
	}
	return replayInputPath
}

func liveCredentialRequest(request serviceSession.Request, liveRequest *runtimeSession.LiveRequest) serviceSession.Request {
	credentialRequest := request
	credentialRequest.ReplayPath = liveRequest.Replay.InputCapturePath
	credentialRequest.Provider = liveRequest.Provider
	credentialRequest.Model = liveRequest.Model
	credentialRequest.BaseURL = liveRequest.BaseURL
	return credentialRequest
}

func configureLiveCapturePath(request serviceSession.Request, replayInputPath string, recorder runtimeSession.LiveRecorder, liveRequest *runtimeSession.LiveRequest) {
	if request.RecordPath != "" || replayInputPath != "" {
		return
	}
	providerCapture, ok := recorder.(runtimeRecording.ProviderCapture)
	if !ok {
		return
	}
	path := strings.TrimSpace(providerCapture.ProviderCapturePath())
	if path != "" {
		configureCapturePath(liveRequest, path)
	}
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

func liveRunOptions(out io.Writer, request serviceSession.Request, liveRequest runtimeSession.LiveRequest, recorder runtimeSession.LiveRecorder, filePorts *FilePorts, deps Dependencies, traceRun *publicTraceRun) runtimeSession.LiveRunOptions {
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
	deviceService = wrapTraceDeviceService(deviceService, traceRun)
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
