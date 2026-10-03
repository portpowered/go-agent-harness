package codexlive

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/internal/livesession"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/quicksilver"
)

// The fakes below stand in for the pion peer and the sideband so the
// transport runs inside a testing/synctest bubble on virtual time.

// sentFrame is one frame the transport wrote to the peer, and when.
type sentFrame struct {
	samples []int16
	at      time.Time
}

type fakePeer struct {
	mu      sync.Mutex
	sent    []sentFrame
	inbound chan []int16
	failed  chan struct{}
	done    chan struct{}
	once    sync.Once
	fail    error
}

func newFakePeer() *fakePeer {
	return &fakePeer{inbound: make(chan []int16, 8), failed: make(chan struct{}), done: make(chan struct{})}
}

func (p *fakePeer) WriteFrame(_ context.Context, samples []int16) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail != nil {
		return p.fail
	}
	p.sent = append(p.sent, sentFrame{samples: samples, at: time.Now()})
	return nil
}

func (p *fakePeer) ReadFrame(ctx context.Context) ([]int16, error) {
	select {
	case frame := <-p.inbound:
		return frame, nil
	case <-p.done:
		return nil, codexrtc.ErrPeerClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *fakePeer) Failed() <-chan struct{} { return p.failed }

func (p *fakePeer) Close() error {
	p.once.Do(func() { close(p.done) })
	return nil
}

func (p *fakePeer) frames() []sentFrame {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]sentFrame(nil), p.sent...)
}

// fakeControl is one sideband connection. The test feeds server events or a
// read error through events; Send records client events.
type fakeControl struct {
	events chan any
	// gate, when set, holds every Send until it is closed.
	gate    chan struct{}
	mu      sync.Mutex
	sent    []quicksilver.Event
	sendErr error
	done    chan struct{}
	once    sync.Once
}

func newFakeControl() *fakeControl {
	return &fakeControl{events: make(chan any, 16), done: make(chan struct{})}
}

func (c *fakeControl) Send(event quicksilver.Event) error {
	if c.gate != nil {
		<-c.gate
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sendErr != nil {
		return c.sendErr
	}
	c.sent = append(c.sent, event)
	return nil
}

func (c *fakeControl) Receive() (quicksilver.Event, error) {
	select {
	case next := <-c.events:
		if err, ok := next.(error); ok {
			return nil, err
		}
		event, ok := next.(quicksilver.Event)
		if !ok {
			return nil, fmt.Errorf("fake sideband: %T is not an event", next)
		}
		return event, nil
	case <-c.done:
		return nil, codexrtc.ErrSidebandClosed
	}
}

func (c *fakeControl) Close() error {
	c.once.Do(func() { close(c.done) })
	return nil
}

func (c *fakeControl) sentEvents() []quicksilver.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]quicksilver.Event(nil), c.sent...)
}

// dialer hands out scripted dial results in order and records dial times.
type dialer struct {
	mu      sync.Mutex
	results []any // *fakeControl or error
	dials   []time.Time
}

func (d *dialer) dial(context.Context) (control, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dials = append(d.dials, time.Now())
	if len(d.results) == 0 {
		return nil, errors.New("no more scripted dials")
	}
	next := d.results[0]
	d.results = d.results[1:]
	if err, ok := next.(error); ok {
		return nil, err
	}
	side, ok := next.(*fakeControl)
	if !ok {
		return nil, fmt.Errorf("scripted dial: %T is not a sideband", next)
	}
	return side, nil
}

func (d *dialer) dialTimes() []time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]time.Time(nil), d.dials...)
}

// harness is a session over the fake transport.
type harness struct {
	peer    *fakePeer
	side    *fakeControl
	dialer  *dialer
	conn    *conn
	session *livesession.Session
}

func newHarness(t *testing.T, dial *dialer) *harness {
	t.Helper()
	peer, side := newFakePeer(), newFakeControl()
	if dial == nil {
		dial = &dialer{}
	}
	transport, err := newConn(t.Context(), peer, side, dial.dial, connConfig{rate: 24000, clock: clock.Real{}, logger: logging.DummyLogger(), policy: defaultReconnectPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	session := livesession.New(transport, nil, livesession.Settings{
		Name: "codex test", Format: livesession.Format{Type: livesession.AudioTypePCM, Rate: 24000},
		Clock: clock.Real{}, SegmentGap: DefaultSegmentGap, DelegationSettle: DefaultDelegationSettle, CloseTimeout: time.Second,
	}, dialect{})
	session.Open(t.Context(), "rtc_test", quicksilver.ModelCodex)
	h := &harness{peer: peer, side: side, dialer: dial, conn: transport, session: session}
	h.expect(t, messages.StreamTypeSessionOpen)
	h.expect(t, messages.StreamTypeSessionCreated)
	return h
}

// next reads one message; inside a bubble a missing one is a deadlock.
func (h *harness) next(t *testing.T) messages.StreamMessage {
	t.Helper()
	msg, ok := <-h.session.Receive().Chan()
	if !ok {
		t.Fatal("receive buffer closed")
	}
	return msg
}

func (h *harness) expect(t *testing.T, want messages.StreamMessageType) messages.StreamMessage {
	t.Helper()
	msg := h.next(t)
	if msg.Type != want {
		t.Fatalf("message = %s %+v, want %s", msg.Type, msg.Value, want)
	}
	return msg
}

// pending reports whether a message is ready without waiting.
func (h *harness) pending() bool {
	select {
	case msg := <-h.session.Receive().Chan():
		_ = msg
		return true
	default:
		return false
	}
}

// valueOf returns msg's value as T.
func valueOf[T any](t *testing.T, msg messages.StreamMessage) T {
	t.Helper()
	value, ok := msg.Value.(T)
	if !ok {
		t.Fatalf("%s value = %T, want %T", msg.Type, msg.Value, value)
	}
	return value
}

// wait lets d pass on the bubble's virtual clock.
func wait(d time.Duration) {
	timer := time.NewTimer(d)
	<-timer.C
}
