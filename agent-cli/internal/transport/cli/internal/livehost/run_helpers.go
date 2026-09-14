package livehost

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	runtimeSessionTrace "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

const liveCapabilityEventBuffer = 32

type liveRunAdmission struct {
	runner           runtimeSession.LiveRunner
	liveRequest      runtimeSession.LiveRequest
	credentials      []string
	credentialsReady bool
	traceRun         *publicTraceRun
}

func prepareLiveRun(ctx context.Context, request serviceSession.Request, deps Dependencies) (liveRunAdmission, error) {
	traceRequested := request.TraceAudio || strings.TrimSpace(request.RecordDirectory) != ""
	if traceRequested && deps.TraceService == nil {
		return liveRunAdmission{}, errors.New("live session trace service is unavailable")
	}
	runner, err := liveRunner(deps.LiveService)
	if err != nil {
		return liveRunAdmission{}, err
	}
	if deps.BuildRequest == nil {
		return liveRunAdmission{}, errors.New("live request builder is unavailable")
	}
	liveRequest, err := deps.BuildRequest(ctx, request, deps.ReplayInspection)
	if err != nil {
		return liveRunAdmission{}, err
	}
	credentials, credentialsReady, err := resolveTraceCredentials(request, &liveRequest, deps, traceRequested)
	if err != nil {
		return liveRunAdmission{}, err
	}
	traceRun, err := prepareLiveTrace(request, liveRequest, deps, credentials)
	if err != nil {
		return liveRunAdmission{}, err
	}
	return liveRunAdmission{runner, liveRequest, credentials, credentialsReady, traceRun}, nil
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

func (r *publicTraceRun) wrapFilePorts(filePorts *FilePorts) {
	if r == nil || filePorts == nil {
		return
	}
	if filePorts.Input != nil {
		filePorts.Input.Source = r.wrapFileSource(filePorts.Input.Source, filePorts.Input.SampleRate)
	}
	for index := range filePorts.InputTurns {
		filePorts.InputTurns[index].Source = r.wrapFileSource(filePorts.InputTurns[index].Source, filePorts.InputTurns[index].SampleRate)
	}
}

func (r *publicTraceRun) wrapFileSource(source audio.AudioSource, rate int) audio.AudioSource {
	if r == nil || source == nil || r.binding.PreGateSamplesObserver == nil {
		return source
	}
	if rate <= 0 {
		rate = audio.SampleRate
	}
	if sampleSource, ok := source.(audio.SampleSource); ok {
		return &traceSampleSource{source: sampleSource, rate: rate, observer: r.binding.PreGateSamplesObserver}
	}
	return &traceAudioSource{source: source, rate: rate, observer: r.binding.PreGateSamplesObserver}
}

type traceAudioSource struct {
	source   audio.AudioSource
	rate     int
	observer runtimeSessionTrace.CaptureSamplesObserver
}

func (s *traceAudioSource) ReadFrame(ctx context.Context, buf []int16) error {
	if s == nil || s.source == nil {
		return io.EOF
	}
	if err := s.source.ReadFrame(ctx, buf); err != nil {
		return err
	}
	if len(buf) > 0 && s.observer != nil {
		s.observer(s.rate, append([]int16(nil), buf...))
	}
	return nil
}

func (s *traceAudioSource) Close() error {
	if s == nil || s.source == nil {
		return nil
	}
	return s.source.Close()
}

type traceSampleSource struct {
	source   audio.SampleSource
	rate     int
	observer runtimeSessionTrace.CaptureSamplesObserver
}

func (s *traceSampleSource) ReadFrame(ctx context.Context, buf []int16) error {
	if s == nil || s.source == nil {
		return io.EOF
	}
	if err := s.source.ReadFrame(ctx, buf); err != nil {
		return err
	}
	if len(buf) > 0 && s.observer != nil {
		s.observer(s.rate, append([]int16(nil), buf...))
	}
	return nil
}

func (s *traceSampleSource) ReadSamples(ctx context.Context, buf []int16) (int, error) {
	if s == nil || s.source == nil {
		return 0, io.EOF
	}
	count, err := s.source.ReadSamples(ctx, buf)
	if count > 0 && count <= len(buf) && s.observer != nil {
		s.observer(s.rate, append([]int16(nil), buf[:count]...))
	}
	return count, err
}

func (s *traceSampleSource) Close() error {
	if s == nil || s.source == nil {
		return nil
	}
	return s.source.Close()
}

var _ audio.AudioSource = (*traceAudioSource)(nil)
var _ audio.SampleSource = (*traceSampleSource)(nil)

// MapBrowserEvents adapts the CLI browser observer to the provider-neutral
// live capability stream. The adapter owns a bounded queue and exits with the
// invocation context so browser resources cannot outlive the session.
func MapBrowserEvents(source func(context.Context) <-chan webmcp.BrowserEvent) func(context.Context) <-chan runtimeSession.LiveCapabilityEvent {
	return mapCapabilityEvents(source, browserCapabilityEvent)
}

// MapBrokerEvents adapts the legacy broker observer to the same neutral live
// capability stream while retaining its typed lifecycle state.
func MapBrokerEvents(source func(context.Context) <-chan webmcp.BrokerEvent) func(context.Context) <-chan runtimeSession.LiveCapabilityEvent {
	return mapCapabilityEvents(source, brokerCapabilityEvent)
}

func mapCapabilityEvents[Event any](source func(context.Context) <-chan Event, mapEvent func(Event) runtimeSession.LiveCapabilityEvent) func(context.Context) <-chan runtimeSession.LiveCapabilityEvent {
	return func(ctx context.Context) <-chan runtimeSession.LiveCapabilityEvent {
		if source == nil || mapEvent == nil || ctx == nil {
			return nil
		}
		input := source(ctx)
		if input == nil {
			return nil
		}
		output := make(chan runtimeSession.LiveCapabilityEvent, liveCapabilityEventBuffer)
		go forwardCapabilityEvents(ctx, input, output, mapEvent)
		return output
	}
}

func forwardCapabilityEvents[Event any](ctx context.Context, input <-chan Event, output chan<- runtimeSession.LiveCapabilityEvent, mapEvent func(Event) runtimeSession.LiveCapabilityEvent) {
	defer close(output)
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-input:
			if !ok {
				return
			}
			mapped := mapEvent(event)
			select {
			case output <- mapped:
			case <-ctx.Done():
				return
			}
		}
	}
}

func browserCapabilityEvent(event webmcp.BrowserEvent) runtimeSession.LiveCapabilityEvent {
	return runtimeSession.LiveCapabilityEvent{
		Type: string(event.Type), Sequence: event.Sequence, Timestamp: event.At,
		BrowserID: string(event.BrowserID), TargetID: string(event.TargetID),
		Generation: event.Generation, PreviousGeneration: event.PreviousGeneration,
		InvocationID: string(event.InvocationID), ToolName: event.ToolName,
		Status: event.Status, ErrorCode: event.ErrorCode, Reason: event.Reason,
		CatalogReady: event.CatalogReady, ToolCount: event.ToolCount, ToolCountKnown: event.ToolCountKnown,
	}
}

func brokerCapabilityEvent(event webmcp.BrokerEvent) runtimeSession.LiveCapabilityEvent {
	return runtimeSession.LiveCapabilityEvent{
		Type: string(event.Type), Sequence: event.Sequence, Timestamp: event.At,
		BrowserID: string(event.BrowserID), TargetID: string(event.TargetID),
		Generation: event.Generation, InvocationID: string(event.InvocationID),
		ToolName: event.ToolName, State: string(event.State), Reason: event.Reason,
	}
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
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
		HoldToneConfig:  request.HoldToneConfig,
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
