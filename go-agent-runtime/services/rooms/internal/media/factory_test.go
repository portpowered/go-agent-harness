package media

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/mixer"
)

func TestFactoryForwardsNegotiatedFormatAndOwnsHandle(t *testing.T) {
	handle := &fakeHandle{ports: devices.MediaPorts{Capture: fakeCapture{}, Playback: fakePlayback{}}}
	service := &fakeService{handle: handle}
	factory := NewFactory(service)
	format := mixer.Format{SampleRate: 16000, Channels: 1}
	ports, err := factory.OpenMedia(context.Background(), rooms.Participant{
		Kind:         rooms.ParticipantKindHuman,
		ID:           "customer",
		InputDevice:  "mic",
		OutputDevice: "speaker",
	}, format)
	if err != nil {
		t.Fatalf("OpenMedia() error = %v", err)
	}
	if ports.Capture == nil || ports.Playback == nil {
		t.Fatalf("OpenMedia() ports = %#v, want both directions", ports)
	}
	if err := ports.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if handle.closeCount != 1 {
		t.Fatalf("device handle closes = %d, want one", handle.closeCount)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.requests) != 1 {
		t.Fatalf("device requests = %d, want one", len(service.requests))
	}
	request := service.requests[0]
	if request.InputDevice != "mic" || request.OutputDevice != "speaker" || !request.CaptureEnabled || !request.PlaybackEnabled || request.SampleRate != 16000 || request.Channels != 1 {
		t.Fatalf("device request = %+v, want selectors, directions, and negotiated format", request)
	}
}

func TestFactoryLeavesAgentMediaToTheLiveSession(t *testing.T) {
	service := &fakeService{handle: &fakeHandle{}}
	factory := NewFactory(service)
	ports, err := factory.OpenMedia(context.Background(), rooms.Participant{Kind: rooms.ParticipantKindAgent, ID: "agent"}, mixer.DefaultFormat())
	if err != nil {
		t.Fatalf("OpenMedia() error = %v", err)
	}
	if ports.Capture != nil || ports.Playback != nil || ports.CloseFunc != nil {
		t.Fatalf("agent ports = %#v, want empty local media", ports)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.requests) != 0 {
		t.Fatalf("device requests = %d, want no host-device admission for agents", len(service.requests))
	}
}

type fakeService struct {
	mu       sync.Mutex
	requests []devices.Request
	handle   devices.Handle
}

func (s *fakeService) Open(_ context.Context, request devices.Request) (devices.Handle, error) {
	s.mu.Lock()
	s.requests = append(s.requests, request)
	s.mu.Unlock()
	return s.handle, nil
}

type fakeHandle struct {
	ports      devices.MediaPorts
	closeErr   error
	closeCount int
}

func (h *fakeHandle) Media() devices.MediaPorts { return h.ports }
func (h *fakeHandle) Close() error {
	h.closeCount++
	return h.closeErr
}

type fakeCapture struct{}

func (fakeCapture) Close() error                                    { return nil }
func (fakeCapture) Pump(context.Context, audio.OutboundMedia) error { return errors.New("unused") }

type fakePlayback struct{}

func (fakePlayback) Close() error                                   { return nil }
func (fakePlayback) Pump(context.Context, audio.InboundMedia) error { return errors.New("unused") }

var _ devices.Service = (*fakeService)(nil)
var _ devices.Handle = (*fakeHandle)(nil)
