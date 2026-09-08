// Package media adapts the device service's invocation handle to the room
// media contract. The adapter owns only room policy and type translation;
// device selection and worker lifetime remain with services/devices.
package media

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	roommanifest "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/manifest"
)

// Factory binds a public device service to room participants. It does not
// retain a registry or open anything during construction.
type Factory struct {
	service devices.Service
}

func NewFactory(service devices.Service) *Factory {
	return &Factory{service: service}
}

// OpenMedia admits both local directions for a human participant. Room
// manifests require both selectors; the explicit device request still keeps
// the generic device service capable of one-way media for other hosts.
func (f *Factory) OpenMedia(ctx context.Context, participant rooms.Participant, format rooms.AudioFormat) (rooms.MediaPorts, error) {
	if f == nil || f.service == nil {
		return rooms.MediaPorts{}, rooms.ErrRoomServiceUnavailable
	}
	if roommanifest.NormalizeParticipantKind(participant.Kind) != rooms.ParticipantKindHuman {
		// Agent media is normally supplied by the live session's own bounded
		// endpoints. Returning an empty local port set keeps the factory usable
		// for mixed rooms while allowing custom hosts to provide agent-local
		// media through another MediaFactory implementation.
		return rooms.MediaPorts{}, nil
	}
	handle, err := f.service.Open(ctx, devices.Request{
		InputDevice:     participant.InputDevice,
		OutputDevice:    participant.OutputDevice,
		CaptureEnabled:  true,
		PlaybackEnabled: true,
		SampleRate:      format.SampleRate,
		Channels:        format.Channels,
		PlaybackProfile: participant.Voice,
	})
	if err != nil {
		return rooms.MediaPorts{}, err
	}
	if handle == nil {
		return rooms.MediaPorts{}, fmt.Errorf("%w: device service returned nil handle", rooms.ErrRoomServiceUnavailable)
	}
	ports := handle.Media()
	return rooms.MediaPorts{
		Capture:   ports.Capture,
		Playback:  ports.Playback,
		CloseFunc: handle.Close,
	}, nil
}

var _ rooms.MediaFactory = (*Factory)(nil)
