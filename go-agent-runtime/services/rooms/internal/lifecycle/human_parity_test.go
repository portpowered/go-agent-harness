package lifecycle

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/mixer"
)

// TestRunnerKeepsHumanMediaUntilRoomStop exercises the production room
// boundary rather than newRoomGraph in isolation. Human capture must enter
// every peer mixer, provider output must reach the human playback port, and
// both workers must remain alive until the provider turn supplies the room
// bound that ends the invocation.
func TestRunnerKeepsHumanMediaUntilRoomStop(t *testing.T) {
	frameSamples, err := mixer.DefaultFormat().FrameSamples()
	if err != nil {
		t.Fatalf("default audio format frame size: %v", err)
	}
	agent := newParityLiveHandle()
	media := &parityMediaFactory{ports: rooms.MediaPorts{
		Capture:  &parityCapture{frame: paritySignalFrame(frameSamples), started: make(chan struct{})},
		Playback: &parityPlayback{received: make(chan []int16, 4), started: make(chan struct{})},
	}}
	service := &parityLiveService{handle: agent}
	runner := New(Dependencies{Live: service, Media: media, Clock: platformclock.Real{}})
	resultCh := make(chan struct {
		result rooms.RoomResult
		err    error
	}, 1)
	go func() {
		result, err := runner.Run(context.Background(), nil, rooms.RoomRunOptions{
			Manifest: rooms.Manifest{
				SchemaVersion: rooms.SchemaVersion,
				Room:          rooms.Room{MaxTurns: 1},
				Participants: []rooms.Participant{
					{ID: "customer", Kind: rooms.ParticipantKindHuman, SystemPrompt: "customer", InputDevice: "mic", OutputDevice: "speaker", Tools: []string{}},
					{ID: "agent", Kind: rooms.ParticipantKindAgent, SystemPrompt: "agent", OpeningPrompt: "hello", Provider: "fixture", Model: "room", APIKeyEnv: "ROOM_KEY", Tools: []string{}},
				},
			},
		})
		resultCh <- struct {
			result rooms.RoomResult
			err    error
		}{result, err}
	}()

	capture := parityCaptureForTest(t, media.ports.Capture)
	playback := parityPlaybackForTest(t, media.ports.Playback)
	waitSignal(t, capture.started, "human capture startup")
	waitForNonSilent(t, agent.outbound, "human capture was not routed to agent")

	agent.inbound.frames <- audio.PCMFrame{Samples: paritySignalFrame(frameSamples)}
	waitForNonSilentSamples(t, playback.received, "agent audio was not routed to human playback")
	agent.emit(session.LiveEvent{Kind: "turn_completed", SessionID: "agent", ParticipantID: "agent"})

	select {
	case outcome := <-resultCh:
		if outcome.err != nil {
			t.Fatalf("room Run error = %v", outcome.err)
		}
		if outcome.result.TerminationReason != rooms.RoomTerminationMaxTurnsReached {
			t.Fatalf("room termination = %q, want max turns", outcome.result.TerminationReason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("room did not stop after agent turn")
	}
	if !capture.wasClosed() {
		t.Fatal("human capture closed state was not joined")
	}
	if !playback.wasClosed() {
		t.Fatal("human playback closed state was not joined")
	}
	media.mu.Lock()
	closeCount := media.closeCount
	media.mu.Unlock()
	if closeCount != 1 {
		t.Fatalf("human media close count = %d, want one", closeCount)
	}
}

func TestRunnerJoinsHumanMediaAfterNaturalAgentCompletion(t *testing.T) {
	frameSamples, err := mixer.DefaultFormat().FrameSamples()
	if err != nil {
		t.Fatalf("default audio format frame size: %v", err)
	}
	agent := newParityLiveHandle()
	media := &parityMediaFactory{ports: rooms.MediaPorts{
		Capture:  &parityCapture{frame: paritySignalFrame(frameSamples), started: make(chan struct{})},
		Playback: &parityPlayback{received: make(chan []int16, 4), started: make(chan struct{})},
	}}
	runner := New(Dependencies{Live: &parityLiveService{handle: agent}, Media: media, Clock: platformclock.Real{}})
	resultCh := make(chan struct {
		result rooms.RoomResult
		err    error
	}, 1)
	go func() {
		result, err := runner.Run(context.Background(), nil, rooms.RoomRunOptions{
			Manifest: rooms.Manifest{
				SchemaVersion: rooms.SchemaVersion,
				Room:          rooms.Room{Interactive: true},
				Participants: []rooms.Participant{
					{ID: "customer", Kind: rooms.ParticipantKindHuman, SystemPrompt: "customer", InputDevice: "mic", OutputDevice: "speaker", Tools: []string{}},
					{ID: "agent", Kind: rooms.ParticipantKindAgent, SystemPrompt: "agent", OpeningPrompt: "hello", Provider: "fixture", Model: "room", APIKeyEnv: "ROOM_KEY", Tools: []string{}},
				},
			},
		})
		resultCh <- struct {
			result rooms.RoomResult
			err    error
		}{result, err}
	}()
	waitSignal(t, parityCaptureForTest(t, media.ports.Capture).started, "natural-completion human capture startup")
	waitSignal(t, parityPlaybackForTest(t, media.ports.Playback).started, "natural-completion human playback startup")
	agent.closeDone()
	select {
	case outcome := <-resultCh:
		if outcome.err != nil {
			t.Fatalf("room Run error = %v", outcome.err)
		}
		if outcome.result.TerminationReason != rooms.RoomTerminationStopped {
			t.Fatalf("room termination = %q, want stopped", outcome.result.TerminationReason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("room did not stop after natural agent completion")
	}
	if !parityCaptureForTest(t, media.ports.Capture).wasClosed() || !parityPlaybackForTest(t, media.ports.Playback).wasClosed() {
		t.Fatal("human media workers were not joined after natural agent completion")
	}
}

func paritySignalFrame(size int) []int16 {
	frame := make([]int16, size)
	if len(frame) > 0 {
		frame[0] = 1200
	}
	return frame
}

func waitSignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func waitForNonSilent(t *testing.T, frames <-chan audio.PCMFrame, label string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case frame := <-frames:
			for _, sample := range frame.Samples {
				if sample != 0 {
					return
				}
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", label)
		}
	}
}

func waitForNonSilentSamples(t *testing.T, frames <-chan []int16, label string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case samples := <-frames:
			for _, sample := range samples {
				if sample != 0 {
					return
				}
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", label)
		}
	}
}

type parityLiveService struct{ handle *parityLiveHandle }

func (s *parityLiveService) OpenLive(context.Context, session.LiveRequest) (session.LiveHandle, error) {
	if s == nil || s.handle == nil {
		return nil, errors.New("parity live handle is unavailable")
	}
	return s.handle, nil
}

type parityLiveHandle struct {
	inbound    *parityInbound
	outbound   chan audio.PCMFrame
	events     chan session.LiveEvent
	done       chan struct{}
	doneOnce   sync.Once
	eventsOnce sync.Once
	mu         sync.Mutex
	started    bool
	closed     bool
}

func newParityLiveHandle() *parityLiveHandle {
	return &parityLiveHandle{
		inbound:  &parityInbound{frames: make(chan audio.PCMFrame, 4)},
		outbound: make(chan audio.PCMFrame, 32), events: make(chan session.LiveEvent, 8), done: make(chan struct{}),
	}
}

func (h *parityLiveHandle) Media() audio.MediaEndpoints {
	return audio.MediaEndpoints{Inbound: h.inbound, Outbound: parityOutbound{frames: h.outbound}}
}

func (h *parityLiveHandle) Events() <-chan session.LiveEvent { return h.events }

func (h *parityLiveHandle) Start(context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return session.ErrLiveClosed
	}
	h.started = true
	return nil
}

func (h *parityLiveHandle) Send(context.Context, session.LiveControl) error { return nil }

func (h *parityLiveHandle) emit(event session.LiveEvent) {
	select {
	case h.events <- event:
	case <-h.done:
	}
}

func (h *parityLiveHandle) Cancel(error) { h.closeDone() }

func (h *parityLiveHandle) Wait() error {
	<-h.done
	return nil
}

func (h *parityLiveHandle) Close() error {
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	h.closeDone()
	h.eventsOnce.Do(func() { close(h.events) })
	return nil
}

func (h *parityLiveHandle) closeDone() { h.doneOnce.Do(func() { close(h.done) }) }

type parityInbound struct{ frames chan audio.PCMFrame }

func (i *parityInbound) ReadFrame(ctx context.Context) (audio.PCMFrame, error) {
	select {
	case frame := <-i.frames:
		return frame, nil
	case <-ctx.Done():
		return audio.PCMFrame{}, ctx.Err()
	}
}

func (*parityInbound) Close() error { return nil }

type parityOutbound struct{ frames chan<- audio.PCMFrame }

func (o parityOutbound) WriteFrame(ctx context.Context, frame audio.PCMFrame) error {
	select {
	case o.frames <- audio.PCMFrame{Samples: append([]int16(nil), frame.Samples...)}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (parityOutbound) Close() error { return nil }

type parityMediaFactory struct {
	ports      rooms.MediaPorts
	mu         sync.Mutex
	closeCount int
}

func (f *parityMediaFactory) OpenMedia(_ context.Context, participant rooms.Participant, _ rooms.AudioFormat) (rooms.MediaPorts, error) {
	if participant.Kind != rooms.ParticipantKindHuman {
		return rooms.MediaPorts{}, nil
	}
	ports := f.ports
	ports.CloseFunc = func() error {
		f.mu.Lock()
		f.closeCount++
		f.mu.Unlock()
		var closeErr error
		if capture, ok := ports.Capture.(*parityCapture); ok {
			closeErr = errors.Join(closeErr, capture.Close())
		}
		if playback, ok := ports.Playback.(*parityPlayback); ok {
			closeErr = errors.Join(closeErr, playback.Close())
		}
		return closeErr
	}
	return ports, nil
}

type parityCapture struct {
	frame   []int16
	started chan struct{}
	closeCh chan struct{}
	once    sync.Once
}

func (c *parityCapture) Pump(ctx context.Context, outbound audio.OutboundMedia) error {
	c.once.Do(func() {
		c.closeCh = make(chan struct{})
		close(c.started)
	})
	if err := outbound.WriteFrame(ctx, audio.PCMFrame{Samples: append([]int16(nil), c.frame...)}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closeCh:
		return context.Canceled
	}
}

func (c *parityCapture) Close() error {
	if c.closeCh != nil {
		select {
		case <-c.closeCh:
		default:
			close(c.closeCh)
		}
	}
	return nil
}

func (c *parityCapture) wasClosed() bool {
	if c.closeCh == nil {
		return false
	}
	select {
	case <-c.closeCh:
		return true
	default:
		return false
	}
}

type parityPlayback struct {
	received chan []int16
	started  chan struct{}
	closeCh  chan struct{}
	once     sync.Once
}

func (p *parityPlayback) Pump(ctx context.Context, inbound audio.InboundMedia) error {
	p.once.Do(func() {
		p.closeCh = make(chan struct{})
		close(p.started)
	})
	for {
		frame, err := inbound.ReadFrame(ctx)
		if err != nil {
			return err
		}
		select {
		case p.received <- append([]int16(nil), frame.Samples...):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (p *parityPlayback) Close() error {
	if p.closeCh != nil {
		select {
		case <-p.closeCh:
		default:
			close(p.closeCh)
		}
	}
	return nil
}

func (p *parityPlayback) wasClosed() bool {
	if p.closeCh == nil {
		return false
	}
	select {
	case <-p.closeCh:
		return true
	default:
		return false
	}
}

func parityCaptureForTest(t *testing.T, value rooms.MediaCapture) *parityCapture {
	t.Helper()
	capture, ok := value.(*parityCapture)
	if !ok {
		t.Fatalf("capture type = %T, want *parityCapture", value)
	}
	return capture
}

func parityPlaybackForTest(t *testing.T, value rooms.MediaPlayback) *parityPlayback {
	t.Helper()
	playback, ok := value.(*parityPlayback)
	if !ok {
		t.Fatalf("playback type = %T, want *parityPlayback", value)
	}
	return playback
}

var _ session.LiveService = (*parityLiveService)(nil)
var _ session.LiveHandle = (*parityLiveHandle)(nil)
var _ rooms.MediaFactory = (*parityMediaFactory)(nil)
