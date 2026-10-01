// Package admission_test proves, through the public room runner, that a
// participant whose provider fails after admission releases every resource
// the runner handed it.
package admission_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/lifecycle"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// failure is an immutable sentinel for the fake provider's failures.
type failure string

func (f failure) Error() string { return string(f) }

const (
	errStart failure = "provider handshake failed"
	errClose failure = "provider close failed"
)

type failingHandle struct {
	events    chan session.LiveEvent
	closeOnce sync.Once
	closes    atomic.Int32
}

func (*failingHandle) Media() sharedaudio.MediaEndpoints               { return sharedaudio.MediaEndpoints{} }
func (h *failingHandle) Events() <-chan session.LiveEvent              { return h.events }
func (*failingHandle) Start(context.Context) error                     { return errStart }
func (*failingHandle) Send(context.Context, session.LiveControl) error { return nil }
func (*failingHandle) Cancel(error)                                    {}
func (*failingHandle) Wait() error                                     { return nil }
func (h *failingHandle) Close() error {
	h.closes.Add(1)
	h.closeOnce.Do(func() { close(h.events) })
	return errClose
}

type failingLive struct {
	mu      sync.Mutex
	handles []*failingHandle
}

func (s *failingLive) OpenLive(context.Context, session.LiveRequest) (session.LiveHandle, error) {
	handle := &failingHandle{events: make(chan session.LiveEvent, 1)}
	s.mu.Lock()
	s.handles = append(s.handles, handle)
	s.mu.Unlock()
	return handle, nil
}

type countingMedia struct{ closes atomic.Int32 }

func (m *countingMedia) OpenMedia(context.Context, rooms.Participant, rooms.AudioFormat) (rooms.MediaPorts, error) {
	return rooms.MediaPorts{CloseFunc: func() error { m.closes.Add(1); return nil }}, nil
}

func TestStartFailureClosesHandleMediaAndCapabilities(t *testing.T) {
	live, media := &failingLive{}, &countingMedia{}
	var released atomic.Int32
	runner := lifecycle.New(lifecycle.Dependencies{Live: live, Media: media, Clock: platformclock.Real{}})
	_, err := runner.Run(t.Context(), nil, rooms.RoomRunOptions{
		Manifest: rooms.Manifest{
			SchemaVersion: rooms.SchemaVersion, Room: rooms.Room{MaxTurns: 1},
			Participants: []rooms.Participant{
				{ID: "alice", Kind: rooms.ParticipantKindAgent, SystemPrompt: "a", OpeningPrompt: "start", Provider: "p", Model: "m", APIKeyEnv: "A_KEY", Tools: []string{}},
				{ID: "bob", Kind: rooms.ParticipantKindAgent, SystemPrompt: "b", Provider: "p", Model: "m", APIKeyEnv: "B_KEY", Tools: []string{}},
			},
		},
		LiveCapabilitiesFactory: func(context.Context, session.LiveRequest) (session.LiveCapabilities, error) {
			return session.LiveCapabilities{Close: func() error { released.Add(1); return nil }}, nil
		},
	})
	if !errors.Is(err, errStart) || !errors.Is(err, errClose) || !strings.Contains(err.Error(), "start live participant") {
		t.Fatalf("Run = %v, want start failure joined with handle close failure", err)
	}
	live.mu.Lock()
	defer live.mu.Unlock()
	if len(live.handles) == 0 {
		t.Fatal("no participant was admitted")
	}
	for index, handle := range live.handles {
		if handle.closes.Load() != 1 {
			t.Fatalf("handle %d closed %d times, want 1", index, handle.closes.Load())
		}
	}
	if int(media.closes.Load()) < len(live.handles) || int(released.Load()) < len(live.handles) {
		t.Fatalf("media closed %d, capabilities released %d, for %d admitted participants", media.closes.Load(), released.Load(), len(live.handles))
	}
}
