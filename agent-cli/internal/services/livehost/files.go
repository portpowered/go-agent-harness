package livehost

import (
	"errors"
	"io"
	"strings"

	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	runtimeSessionTrace "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// cliFileMediaLabels keeps admission errors in the command's option spelling.
func cliFileMediaLabels() runtimeDevices.FileMediaLabels {
	return runtimeDevices.FileMediaLabels{Input: "--audio-in", InputTurn: "--audio-in-turn", Interruption: "--audio-interrupt", Output: "--audio-out"}
}

// hasFileMedia reports whether the invocation names any finite source or sink.
func hasFileMedia(request serviceSession.Request) bool {
	return request.AudioInput.Present || len(request.AudioTurns) > 0 || len(request.AudioInterrupts) > 0 || request.AudioOutputPath != ""
}

// openFileMedia maps the command's media flags onto the runtime finite media
// service. The runtime owns opening, framing, observation, and cleanup.
func openFileMedia(request serviceSession.Request, out io.Writer, liveRequest runtimeSession.LiveRequest, deps Dependencies, traceRun runtimeSessionTrace.Prepared) (runtimeDevices.FileMediaHandle, error) {
	if !hasFileMedia(request) {
		return nil, nil
	}
	if deps.FileMediaService == nil {
		return nil, errors.New("live file media service is unavailable")
	}
	mediaRequest := runtimeDevices.FileMediaRequest{
		InputTurns:          request.AudioTurns,
		Interruptions:       request.AudioInterrupts,
		OutputPath:          request.AudioOutputPath,
		Stdout:              out,
		OutputSampleRate:    liveRequest.OutputAudioSampleRate,
		NegotiateOutputRate: request.AudioOutputDevicePresent || request.InteractiveDevices,
		// Old raw replay captures did not record their input rate; they keep
		// fixed-frame reads rather than the count-aware finite source contract.
		FrameInput: request.ReplayPath != "" && liveRequest.ReplayPlan != nil && liveRequest.ReplayPlan.InputAudioSampleRate <= 0,
		Scheduler:  deps.FileDeviceService.Scheduler,
		Pacing:     request.AudioInputPacing,
		Labels:     cliFileMediaLabels(),
	}
	if request.AudioInput.Present {
		mediaRequest.Input = &runtimeDevices.FileMediaSource{
			Path: request.AudioInput.Path, Stdin: request.AudioInput.Stdin,
			SampleRate: request.AudioInput.SourceSampleRate, CloseStdinOnCancel: request.AudioInput.CloseStdinOnCancel,
		}
	}
	if traceRun != nil {
		mediaRequest.ObserveSource = func(source audio.AudioSource, rate int) audio.AudioSource {
			return traceRun.WrapAudioSource(source, rate)
		}
	}
	return deps.FileMediaService.OpenFileMedia(mediaRequest)
}

func fileMedia(handle runtimeDevices.FileMediaHandle) runtimeDevices.FileMedia {
	if handle == nil {
		return runtimeDevices.FileMedia{}
	}
	return handle.Media()
}

func selectFileDevices(physical, finite runtimeDevices.Service, deviceRequest runtimeDevices.Request, media runtimeDevices.FileMedia) (runtimeDevices.Service, runtimeDevices.Request) {
	if media.Input != nil {
		deviceRequest.CaptureEnabled = false
	}
	if deviceRequest.CaptureEnabled || deviceRequest.PlaybackEnabled {
		return physical, deviceRequest
	}
	if media.Input != nil || media.Output != nil {
		deviceRequest.CaptureEnabled = media.Input != nil
		deviceRequest.PlaybackEnabled = media.Output != nil
		return finite, deviceRequest
	}
	if len(media.InputTurns) > 0 || len(media.Interruptions) > 0 {
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
		sampleRate = cliLiveDefaultRate
	}
	return runtimeDevices.Request{
		InputDevice: normalizeDeviceSelector(request.AudioInputDevice), OutputDevice: normalizeDeviceSelector(request.AudioOutputDevice),
		RemoteEndpoint:  request.AudioDeviceServer,
		CaptureEnabled:  request.InteractiveDevices || request.AudioInputDevicePresent,
		PlaybackEnabled: request.InteractiveDevices || request.AudioOutputDevicePresent,
		SampleRate:      sampleRate, Channels: audio.Channels, PlaybackProfile: "voice",
		HoldToneConfig: request.HoldToneConfig,
	}
}

// normalizeDeviceSelector translates the CLI's historical "default" spelling
// to the service contract's empty-selector default. Device IDs remain opaque;
// only this stateless compatibility spelling is handled at the host edge.
func normalizeDeviceSelector(selector string) string {
	selector = strings.TrimSpace(selector)
	if strings.EqualFold(selector, "default") {
		return ""
	}
	return selector
}

func audioTurnAdmission(request serviceSession.Request) runtimeSession.AudioTurnAdmission {
	if request.AudioInTurnBarge {
		return runtimeSession.AudioTurnAdmissionBarge
	}
	return runtimeSession.AudioTurnAdmissionCompletionGated
}

func captureCompleteControls(request serviceSession.Request, custom func(serviceSession.Request) []runtimeSession.LiveControl) []runtimeSession.LiveControl {
	if custom != nil {
		return custom(request)
	}
	if !request.AudioInput.Present && len(request.AudioTurns) == 0 && len(request.AudioInterrupts) == 0 {
		return nil
	}
	return []runtimeSession.LiveControl{{Kind: runtimeSession.LiveControlAudioCommit}}
}
