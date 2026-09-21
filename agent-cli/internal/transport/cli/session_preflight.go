package cli

import (
	"errors"
	"strings"
	"time"

	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	serviceDevices "github.com/portpowered/go-agent-harness/agent-cli/internal/services/devices"
	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/spf13/cobra"
)

type sessionCommandPreflight struct {
	cmd               *cobra.Command
	browserTools      string
	transport         string
	signaling         string
	mediaSource       string
	audioInTurnBarge  bool
	audioInTurns      int
	audioDeviceServer string
	maxDuration       time.Duration
	deviceService     runtimeDevices.RemoteEndpointValidator
}

func validateSessionCommandPreflight(input sessionCommandPreflight) (string, error) {
	if err := serviceSession.ValidateSessionAudioInTurnBarge(input.audioInTurnBarge, input.audioInTurns); err != nil {
		return "", err
	}
	if err := validateBrowserToolsBackend(input.browserTools, browserToolsAdmission(input.cmd)); err != nil {
		return "", err
	}
	selectedTransport, err := validateSessionTransport(input.transport)
	if err != nil {
		return "", err
	}
	if err := validateSessionSignaling(selectedTransport, input.signaling, input.cmd.Flags().Changed("signaling")); err != nil {
		return "", err
	}
	if err := validateSessionMediaSource(selectedTransport, input.mediaSource, input.cmd.Flags().Changed("media-source"), input.cmd.Flags().Changed("audio-in")); err != nil {
		return "", err
	}
	if err := serviceDevices.ValidateSessionAudioDeviceConflicts(
		input.cmd.Flags().Changed("audio-in"), input.cmd.Flags().Changed("audio-out"),
		input.cmd.Flags().Changed(serviceDevices.SessionAudioInDeviceFlag), input.cmd.Flags().Changed(serviceDevices.SessionAudioOutDeviceFlag),
	); err != nil {
		return "", err
	}
	if strings.TrimSpace(input.audioDeviceServer) != "" {
		if input.deviceService == nil {
			return "", errors.New("device service is required to validate the audio device server endpoint")
		}
		if err := input.deviceService.ValidateRemoteEndpoint(input.audioDeviceServer); err != nil {
			return "", err
		}
	}
	if err := serviceSession.ValidateSessionMaxDuration(input.maxDuration); err != nil {
		return "", err
	}
	if selectedTransport == SessionTransportWebRTC {
		return "", &SessionWebRTCUnavailableError{}
	}
	return selectedTransport, nil
}

func replayRequestsAudio(request serviceSession.Request) bool {
	return request.AudioInput.Present || request.AudioInput.DevicePresent ||
		len(request.AudioTurns) > 0 || len(request.AudioInterrupts) > 0 ||
		request.AudioOutputPath != "" || request.AudioOutputRequested ||
		request.AudioInputDevicePresent || request.AudioOutputDevicePresent
}
