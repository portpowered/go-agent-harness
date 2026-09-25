package wire

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const (
	contractWait     = 5 * time.Second
	fakeProvider     = "fixture"
	fakeModel        = "fixture-model"
	humanCustomerID  = "customer"
	agentID          = "agent"
)

// contractLive is a public session.LiveService fake. Each participant gets
// one handle; the test decides when that handle's provider session ends.
type contractLive struct {
	mu       sync.Mutex
	requests []session.LiveRequest
	handles  map[string]*contractHandle
	media    bool
	openErr  map[string]error
}

func newContractLive() *contractLive {
	return &contractLive{handles: map[string]*contractHandle{}, openErr: map[string]error{}}
}

func (l *contractLive) OpenLive(_ context.Context, request session.LiveRequest) (session.LiveHandle, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requests = append(l.requests, request)
	if err := l.openErr[request.ParticipantID]; err != nil {
		return nil, err
	}
	handle := newContractHandle(l.media)
	l.handles[request.ParticipantID] = handle
	return handle, nil
}

func (l *contractLive) handle(t *testing.T, id string) *contractHandle {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	handle, ok := l.handles[id]
	if !ok {
		t.Fatalf("participant %q was never opened", id)
	}
	return handle
}

func (l *contractLive) request(t *testing.T, id string) session.LiveRequest {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, request := range l.requests {
		if request.ParticipantID == id {
			return request
		}
	}
	t.Fatalf("participant %q made no live request", id)
	return session.LiveRequest{}
}

// contractHandle is one provider session. Inbound carries provider speech
// into the room; Outbound records what the room delivered to the provider.
type contractHandle struct {
	inbound  *contractFrames
	outbound *contractFrames
	events   chan session.LiveEvent

	mu          sync.Mutex
	done        chan struct{}
	finished    bool
	waitErr     error
	cancelCount int
	closeCount  int
	closeEvents sync.Once
}

func newContractHandle(media bool) *contractHandle {
	handle := &contractHandle{events: make(chan session.LiveEvent, 8), done: make(chan struct{})}
	if media {
		handle.inbound, handle.outbound = newContractFrames(64), newContractFrames(256)
	}
	return handle
}

func (h *contractHandle) Media() audio.MediaEndpoints {
	if h.inbound == nil {
		return audio.MediaEndpoints{}
	}
	return audio.MediaEndpoints{Inbound: h.inbound, Outbound: h.outbound}
}

func (h *contractHandle) Events() <-chan session.LiveEvent                { return h.events }
func (h *contractHandle) Start(context.Context) error                     { return nil }
func (h *contractHandle) Send(context.Context, session.LiveControl) error { return nil }

func (h *contractHandle) Cancel(err error) {
	h.mu.Lock()
	h.cancelCount++
	h.mu.Unlock()
	h.finish(err)
}

// finish ends the provider session; the first cause wins.
func (h *contractHandle) finish(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.finished {
		return
	}
	h.finished, h.waitErr = true, err
	close(h.done)
}

func (h *contractHandle) Wait() error {
	<-h.done
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.waitErr
}

func (h *contractHandle) Close() error {
	h.mu.Lock()
	h.closeCount++
	h.mu.Unlock()
	h.closeEvents.Do(func() { close(h.events) })
	if h.inbound != nil {
		_ = h.inbound.Close()
	}
	return nil
}

func (h *contractHandle) counts() (cancels, closes int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cancelCount, h.closeCount
}

// contractFrames is a bounded PCM queue usable as either media direction.
type contractFrames struct {
	frames chan audio.PCMFrame
	closed chan struct{}
	once   sync.Once
}

func newContractFrames(capacity int) *contractFrames {
	return &contractFrames{frames: make(chan audio.PCMFrame, capacity), closed: make(chan struct{})}
}

func (q *contractFrames) ReadFrame(ctx context.Context) (audio.PCMFrame, error) {
	select {
	case frame := <-q.frames:
		return frame, nil
	case <-q.closed:
		return audio.PCMFrame{}, errors.New("contract media closed")
	case <-ctx.Done():
		return audio.PCMFrame{}, ctx.Err()
	}
}

func (q *contractFrames) WriteFrame(ctx context.Context, frame audio.PCMFrame) error {
	select {
	case q.frames <- frame:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func (q *contractFrames) Close() error {
	q.once.Do(func() { close(q.closed) })
	return nil
}

func agentParticipant(id string) rooms.Participant {
	return rooms.Participant{ID: id, Kind: rooms.ParticipantKindAgent, SystemPrompt: id, OpeningPrompt: "start", Provider: fakeProvider, Model: fakeModel, APIKeyEnv: "UNRESOLVED_" + id, Tools: []string{}}
}

func agentRoom(bounds rooms.Room, ids ...string) rooms.Manifest {
	manifest := rooms.Manifest{SchemaVersion: rooms.SchemaVersion, Room: bounds}
	for _, id := range ids {
		manifest.Participants = append(manifest.Participants, agentParticipant(id))
	}
	return manifest
}

type runOutcome struct {
	result rooms.RoomResult
	err    error
}

// startRun runs the room asynchronously and reports each ready participant.
func startRun(ctx context.Context, service rooms.Service, options rooms.RoomRunOptions) (<-chan runOutcome, <-chan string) {
	ready := make(chan string, len(options.Manifest.Participants))
	previous := options.OnParticipantReady
	options.OnParticipantReady = func(value rooms.RoomParticipantReady) {
		if previous != nil {
			previous(value)
		}
		ready <- value.ParticipantID
	}
	done := make(chan runOutcome, 1)
	go func() {
		result, err := service.Run(ctx, nil, options)
		done <- runOutcome{result: result, err: err}
	}()
	return done, ready
}

func awaitReady(t *testing.T, ready <-chan string, count int) {
	t.Helper()
	for range count {
		select {
		case <-ready:
		case <-time.After(contractWait):
			t.Fatal("room participants were not admitted")
		}
	}
}

func awaitOutcome(t *testing.T, done <-chan runOutcome) runOutcome {
	t.Helper()
	select {
	case outcome := <-done:
		return outcome
	case <-time.After(contractWait):
		t.Fatal("room did not return")
		return runOutcome{}
	}
}

func assertParticipant(t *testing.T, result rooms.RoomResult, id string, want rooms.ParticipantTerminationReason) rooms.RoomParticipantResult {
	t.Helper()
	value, ok := result.Participants[id]
	if !ok || value.TerminationReason != want || value.Reason != want {
		t.Fatalf("participant %q result = %+v (found=%t), want %q", id, value, ok, want)
	}
	return value
}
