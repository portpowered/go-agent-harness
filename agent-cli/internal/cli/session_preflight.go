package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/spf13/cobra"
)

const sessionAudioTracePath = "audio-trace"

func prepareSessionAudioTrace(enabled bool, request *services.RTCDeviceBindingRequest, runtimeObserver services.SessionRuntimeObserver) (*services.SessionAudioTrace, services.SessionRuntimeObserver, error) {
	if !enabled {
		return nil, runtimeObserver, nil
	}
	trace, err := services.NewSessionAudioTrace(sessionAudioTracePath)
	if err != nil {
		return nil, nil, fmt.Errorf("--trace-audio %q: %w", sessionAudioTracePath, err)
	}
	request.PreGateSamplesObserver = trace.CaptureMicrophonePreGate
	request.UploadedSamplesObserver = trace.CaptureMicrophoneUploaded
	request.PlaybackSamplesObserver = trace.CaptureSpeakerEnqueued
	request.RenderedSamplesObserver = trace.CaptureSpeakerRendered
	return trace, services.CombineSessionRuntimeObservers(runtimeObserver, trace), nil
}

func closeSessionAudioTrace(trace *services.SessionAudioTrace, runErr *error) {
	if trace == nil || runErr == nil {
		return
	}
	if err := trace.Close(); err != nil {
		*runErr = errors.Join(*runErr, fmt.Errorf("--trace-audio %q: %w", sessionAudioTracePath, err))
	}
}

type sessionCommandPreflight struct {
	cmd              *cobra.Command
	browserTools     string
	transport        string
	signaling        string
	mediaSource      string
	audioInTurnBarge bool
	audioInTurns     int
	maxDuration      time.Duration
}

func validateSessionCommandPreflight(input sessionCommandPreflight) (string, error) {
	if err := services.ValidateSessionAudioInTurnBarge(input.audioInTurnBarge, input.audioInTurns); err != nil {
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
	if err := services.ValidateSessionAudioDeviceConflicts(
		input.cmd.Flags().Changed("audio-in"), input.cmd.Flags().Changed("audio-out"),
		input.cmd.Flags().Changed(services.SessionAudioInDeviceFlag), input.cmd.Flags().Changed(services.SessionAudioOutDeviceFlag),
	); err != nil {
		return "", err
	}
	if err := services.ValidateSessionMaxDuration(input.maxDuration); err != nil {
		return "", err
	}
	if selectedTransport == SessionTransportWebRTC {
		return "", &SessionWebRTCUnavailableError{}
	}
	return selectedTransport, nil
}
