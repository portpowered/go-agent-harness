package livehost

import (
	"io"

	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

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
