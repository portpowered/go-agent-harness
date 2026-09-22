package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func timerChannel(timer *time.Timer) <-chan time.Time {
	if timer == nil {
		return nil
	}
	return timer.C
}

func applyRoomParticipantVoiceTransform(coordinator *roomCoordinator, runtime *roomParticipantRuntime, opts RoomRunOptions, plan *roomParticipantPlan, pcm []byte) ([]byte, bool) {
	if opts.AudioService == nil {
		coordinator.failParticipant(plan.manifest.ID, roomParticipantFailure(plan.manifest.ID, errors.New("audio service is required for room voice transformation"), secretsForPlan(plan)))
		return nil, false
	}
	transformed, err := opts.AudioService.ApplyVoicePCM16(runtime.ctx, audioio.VoicePCMRequest{Voice: plan.manifest.Voice, PCM: pcm})
	if err != nil {
		coordinator.failParticipant(plan.manifest.ID, roomParticipantFailure(plan.manifest.ID, fmt.Errorf("transform participant voice audio: %w", err), secretsForPlan(plan)))
		return nil, false
	}
	return transformed, true
}

func sortedRoomIDs(ids []string) []string {
	result := append([]string(nil), ids...)
	for index := 1; index < len(result); index++ {
		value := result[index]
		position := index
		for position > 0 && result[position-1] > value {
			result[position] = result[position-1]
			position--
		}
		result[position] = value
	}
	return result
}

func roomFormatForOptions(opts RoomRunOptions) room.PCM16Format {
	format := roomMixerConfigForOptions(opts).Format
	if format == (room.PCM16Format{}) {
		return room.DefaultPCM16Format()
	}
	return format
}

func roomParticipantEvidenceForRuntime(evidence *roomEvidence, runtime *roomParticipantRuntime) *roomParticipantEvidence {
	if evidence == nil {
		return nil
	}
	return evidence.participant(runtime.plan.manifest.ID)
}

func configureRoomParticipantBrowserOptions(ctx context.Context, opts RoomRunOptions, participant room.Participant, plan *roomParticipantPlan, sessionOptions SessionRunOptions, staticCapabilities RoomParticipantToolCapabilities, secret string) (SessionRunOptions, bool, error) {
	if participant.BrowserTools == nil {
		return sessionOptions, false, nil
	}
	if opts.BrowserCapabilitiesFactory == nil {
		return roomParticipantBrowserStartupFailure(sessionOptions, plan, participant, secret, ErrRoomParticipantBrowserToolsUnavailable)
	}
	browserCapabilities, err := opts.BrowserCapabilitiesFactory(participant)
	if err != nil {
		return roomParticipantBrowserStartupFailure(sessionOptions, plan, participant, secret, fmt.Errorf("configure browser tools: %w", err))
	}
	plan.capabilityCoordinator = NewSessionCapabilityCoordinator(browserCapabilities.Close)
	if err := validateRoomParticipantBrowserCapabilities(participant, browserCapabilities); err != nil {
		if errors.Is(err, ErrRoomParticipantBrowserToolMismatch) {
			return sessionOptions, false, fmt.Errorf("room participant %q browser capability contract: %w", participant.ID, err)
		}
		return roomParticipantBrowserStartupFailure(sessionOptions, plan, participant, secret, err)
	}
	composed, err := composeRoomParticipantBrowserCapabilities(ctx, participant, staticCapabilities, browserCapabilities)
	if err != nil {
		return sessionOptions, false, fmt.Errorf("room participant %q browser composition contract: %w", participant.ID, err)
	}
	composed, err = initializeRoomParticipantBrowserCapabilities(ctx, composed)
	if err != nil {
		return roomParticipantBrowserStartupFailure(sessionOptions, plan, participant, secret, err)
	}
	sessionOptions.ToolExecutor = composed.Executor
	sessionOptions.ToolDefinitions = cloneRoomToolDefinitions(composed.Definitions)
	sessionOptions.ToolDefinitionBase = cloneRoomToolDefinitions(composed.ToolDefinitionBase)
	sessionOptions.RefreshToolDefinitions = composed.RefreshToolDefinitions
	sessionOptions.BrowserWatch = composed.BrowserWatch
	sessionOptions.BrowserToolsEnabled = true
	sessionOptions.CapabilityClose = plan.capabilityCoordinator.Close
	return sessionOptions, false, nil
}

func roomParticipantBrowserStartupFailure(sessionOptions SessionRunOptions, plan *roomParticipantPlan, participant room.Participant, secret string, err error) (SessionRunOptions, bool, error) {
	plan.startupErr = roomParticipantFailure(participant.ID, err, []string{secret})
	return sessionOptions, true, nil
}

func initializeRoomParticipantBrowserCapabilities(ctx context.Context, capabilities RoomParticipantBrowserCapabilities) (RoomParticipantBrowserCapabilities, error) {
	if capabilities.Initialize != nil {
		if err := capabilities.Initialize(ctx); err != nil {
			return capabilities, fmt.Errorf("initialize browser tools: %w", err)
		}
	}
	if capabilities.RefreshToolDefinitions == nil {
		return capabilities, nil
	}
	definitions, err := capabilities.RefreshToolDefinitions(ctx)
	if err == nil {
		capabilities.Definitions = cloneRoomToolDefinitions(definitions)
		return capabilities, nil
	}
	if ctx.Err() != nil {
		return capabilities, fmt.Errorf("refresh browser tools: %w", err)
	}
	return capabilities, nil
}

func openRoomHumanDevices(runtime *roomParticipantRuntime, service runtimeDevices.Service) error {
	if runtime == nil || runtime.plan == nil {
		return errors.New("human participant runtime is nil")
	}
	if service == nil {
		return runtimeDevices.ErrUnavailable
	}
	participant := runtime.plan.manifest
	handle, err := service.Open(runtime.ctx, runtimeDevices.Request{
		InputDevice: participant.InputDevice, OutputDevice: participant.OutputDevice,
		CaptureEnabled: true, PlaybackEnabled: true, SampleRate: runtime.mixer.Format().SampleRate, Channels: 1,
	})
	if err != nil {
		return fmt.Errorf("open human participant devices: %w", err)
	}
	if handle == nil {
		return fmt.Errorf("%w: device service returned a nil handle", runtimeDevices.ErrUnavailable)
	}
	ports := handle.Media()
	if ports.Capture == nil || ports.Playback == nil {
		return errors.Join(fmt.Errorf("%w: device service omitted room capture or playback", runtimeDevices.ErrUnavailable), handle.Close())
	}
	runtime.deviceHandle = handle
	runtime.input, runtime.output = ports.Capture, ports.Playback
	if selection, ok := handle.(runtimeDevices.DeviceSelectionProvider); ok {
		runtime.inputDeviceID, runtime.outputDeviceID = selection.SelectedDeviceIDs()
	}
	runtime.lifecycle.markDeviceReady()
	return nil
}

func closeRoomParticipantDevices(runtime *roomParticipantRuntime, cleanup *roomCleanupWaiter) error {
	if runtime == nil || runtime.plan == nil || runtime.deviceHandle == nil {
		return nil
	}
	id := runtime.plan.manifest.ID
	return boundedRoomCleanupOperation(cleanup, roomLifecycleWorkLabel(id, "devices"), runtime.deviceHandle.Close)
}

func runRoomHumanCapture(ctx, roomCtx context.Context, coordinator *roomCoordinator, runtime *roomParticipantRuntime, startGate <-chan struct{}, participantEvidence *roomParticipantEvidence, opts RoomRunOptions, secrets []string) error {
	if runtime == nil || runtime.plan == nil || runtime.input == nil || runtime.mixer == nil {
		return errors.New("human participant input device is not ready")
	}
	select {
	case <-startGate:
	case <-ctx.Done():
		return nil
	case <-roomCtx.Done():
		return nil
	}
	participantID := runtime.plan.manifest.ID
	media := &roomHumanCaptureMedia{coordinator: coordinator, runtime: runtime, opts: opts, evidence: participantEvidence, secrets: secrets}
	if err := runtime.input.Pump(ctx, media); err != nil {
		if ctx.Err() != nil || coordinator.isStopping() || errors.Is(err, context.Canceled) {
			return nil //nolint:nilerr // participant and room shutdown are expected cancellation paths
		}
		failure := roomParticipantFailure(participantID, fmt.Errorf("capture human input device: %w", err), secrets)
		coordinator.failParticipant(participantID, failure)
		return failure
	}
	return nil
}

func pumpRoomHumanOutput(ctx, roomCtx context.Context, coordinator *roomCoordinator, runtime *roomParticipantRuntime, startGate <-chan struct{}, participantEvidence *roomParticipantEvidence, secrets []string) {
	if runtime == nil || runtime.mixer == nil || runtime.output == nil {
		return
	}
	select {
	case <-startGate:
	case <-ctx.Done():
		return
	case <-roomCtx.Done():
		return
	}
	media := newRoomHumanPlaybackMedia(runtime, participantEvidence)
	err := runtime.output.Pump(ctx, media)
	if finishErr := media.finish(); finishErr != nil {
		err = errors.Join(err, finishErr)
	}
	if err != nil && ctx.Err() == nil && roomCtx.Err() == nil && !coordinator.isStopping() &&
		!errors.Is(err, context.Canceled) && !errors.Is(err, room.ErrMixerClosed) {
		coordinator.failParticipant(runtime.plan.manifest.ID, roomParticipantFailure(runtime.plan.manifest.ID, fmt.Errorf("write human output device: %w", err), secrets))
	}
}

type roomHumanCaptureMedia struct {
	coordinator *roomCoordinator
	runtime     *roomParticipantRuntime
	opts        RoomRunOptions
	evidence    *roomParticipantEvidence
	secrets     []string
}

func (m *roomHumanCaptureMedia) Close() error { return nil }

func (m *roomHumanCaptureMedia) WriteFrame(ctx context.Context, frame audio.PCMFrame) error {
	if m == nil || m.runtime == nil || m.runtime.plan == nil || m.runtime.mixer == nil {
		return errors.New("human participant capture route is unavailable")
	}
	participantID := m.runtime.plan.manifest.ID
	if len(frame.Samples) == 0 {
		return nil
	}
	pcm := encodeRoomPCM16(frame.Samples)
	if m.evidence != nil {
		if err := m.evidence.observeSentAudio(ctx, pcm); err != nil {
			return fmt.Errorf("record human capture evidence: %w", err)
		}
	}
	sourceRate := m.runtime.mixer.Format().SampleRate
	return m.fanOutPCM(ctx, participantID, pcm, sourceRate)
}

func (m *roomHumanCaptureMedia) fanOutPCM(ctx context.Context, participantID string, pcm []byte, sourceRate int) error {
	for _, target := range m.coordinator.activeExcept(participantID) {
		if err := m.fanOutTargetPCM(ctx, participantID, sourceRate, pcm, target); err != nil {
			return err
		}
	}
	return nil
}

func (m *roomHumanCaptureMedia) fanOutTargetPCM(ctx context.Context, participantID string, sourceRate int, pcm []byte, target *roomParticipantRuntime) error {
	if target == nil || target.mixer == nil {
		return nil
	}
	if target.mixer.Format().SampleRate != sourceRate && m.opts.AudioService == nil {
		return errors.New("audio service is required to convert human room input")
	}
	targetPCM, err := m.convertPCMForTarget(ctx, sourceRate, pcm, target)
	if err != nil {
		failure := roomParticipantFailure(participantID, fmt.Errorf("convert human input audio for %s: %w", target.plan.manifest.ID, err), m.secrets)
		m.coordinator.failParticipant(participantID, failure)
		return failure
	}
	if err := routeRoomPeerPCM(ctx, participantID, target, targetPCM); err != nil {
		if m.coordinator.isActive(target.plan.manifest.ID) {
			failure := roomParticipantFailure(target.plan.manifest.ID, fmt.Errorf("receive fan out human PCM from %s: %w", participantID, err), m.secrets)
			m.coordinator.failParticipant(target.plan.manifest.ID, failure)
			return failure
		}
		return nil
	}
	if m.opts.onParticipantAudioFanned != nil {
		m.opts.onParticipantAudioFanned(participantID, target.plan.manifest.ID, append([]byte(nil), targetPCM...))
	}
	return nil
}

func (m *roomHumanCaptureMedia) convertPCMForTarget(ctx context.Context, sourceRate int, pcm []byte, target *roomParticipantRuntime) ([]byte, error) {
	targetRate := target.mixer.Format().SampleRate
	if targetRate == sourceRate {
		return pcm, nil
	}
	return m.opts.AudioService.ConvertPCM16(ctx, audioio.PCM16Request{
		PCM: pcm, SourceRate: sourceRate, TargetRate: targetRate,
		SourceChannels: 1, TargetChannels: 1,
	})
}

type roomHumanPlaybackMedia struct {
	runtime  *roomParticipantRuntime
	evidence *roomParticipantEvidence

	pending bool
	sources []string
	pcm     []byte
}

func newRoomHumanPlaybackMedia(runtime *roomParticipantRuntime, evidence *roomParticipantEvidence) *roomHumanPlaybackMedia {
	return &roomHumanPlaybackMedia{runtime: runtime, evidence: evidence}
}

func (m *roomHumanPlaybackMedia) ReadFrame(ctx context.Context) (audio.PCMFrame, error) {
	if m == nil || m.runtime == nil || m.runtime.mixer == nil {
		return audio.PCMFrame{}, errors.New("human participant output mixer is unavailable")
	}
	if err := m.resolvePending(true); err != nil {
		return audio.PCMFrame{}, err
	}
	mixed, err := m.runtime.mixer.ReadFrameWithSources(ctx)
	if err != nil {
		return audio.PCMFrame{}, err
	}
	pcm := mixed.PCM
	samples, err := codec.DecodePCM16WithLimit(pcm, len(pcm))
	if err != nil {
		return audio.PCMFrame{}, err
	}
	m.pending = true
	m.sources = append(m.sources[:0], mixed.Sources...)
	m.pcm = pcm
	return audio.PCMFrame{Samples: samples, Format: audio.PCM16DeviceFormat(m.runtime.mixer.Format().SampleRate)}, nil
}

func (m *roomHumanPlaybackMedia) Close() error {
	if m != nil {
		return m.resolvePending(false)
	}
	return nil
}

func (m *roomHumanPlaybackMedia) finish() error { return m.Close() }

func (m *roomHumanPlaybackMedia) resolvePending(accepted bool) error {
	if m == nil || !m.pending {
		return nil
	}
	if m.runtime.ingress != nil {
		reason := ""
		if !accepted {
			reason = roomAudioIngressReasonParticipantOutputRejected
		}
		m.runtime.ingress.resolveFrame(m.sources, len(m.pcm), reason)
	}
	var err error
	if accepted && m.evidence != nil {
		err = m.evidence.observeReceivedAudio(m.pcm)
	}
	m.pending = false
	m.sources = nil
	m.pcm = nil
	return err
}

func encodeRoomPCM16(samples []int16) []byte {
	return codec.EncodePCM16(samples)
}
