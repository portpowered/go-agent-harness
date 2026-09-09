package subsystems_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/subsystems"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// TestPingPongSubsystem_EmitsPong directly tests the PingPong subsystem.
func TestPingPongSubsystem_EmitsPong(t *testing.T) {
	inbox := messages.NewTypedBuffer[messages.KernelDeltaRequest](8)
	pp := subsystems.NewPingPong(inbox, nil)

	ls := &state.LoopState{
		Mode: state.DuplexSession,
	}
	ls.Outputs.KernelDeltaInbox = inbox
	ls.Inputs.UserControlPlaneMessage = []messages.Message{
		{
			Role: messages.RoleUser,
			ContentParts: []messages.ContentPart{
				messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypePing},
			},
		},
	}

	if err := pp.Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Read PONG from inbox.
	req, ok := inbox.Read()
	if !ok {
		t.Fatal("expected PONG in kernel delta inbox")
	}
	if req.Delta.Type != messages.StreamTypePong {
		t.Errorf("delta type: got %q, want %q", req.Delta.Type, messages.StreamTypePong)
	}
	if pv, ok := req.Delta.Value.(*messages.PongValue); ok {
		now := time.Now().UnixMilli()
		diff := now - pv.Timestamp
		if diff < 0 || diff > 5000 {
			t.Errorf("PONG timestamp unreasonable: diff=%dms", diff)
		}
	} else {
		t.Error("PONG value is not *PongValue")
	}
}

func TestPingPongSubsystem_UsesInjectedClock(t *testing.T) {
	base := time.Date(2026, time.January, 2, 3, 4, 5, 123456000, time.UTC)
	logicalClock := clock.NewDeterministic(base, time.Millisecond)
	inbox := messages.NewTypedBuffer[messages.KernelDeltaRequest](8)
	pp := subsystems.NewPingPongWithClock(inbox, nil, logicalClock)
	ls := &state.LoopState{
		Mode: state.DuplexSession,
		Inputs: state.Buffers{UserControlPlaneMessage: []messages.Message{
			pingMessageForTest(),
		}},
	}

	if err := pp.Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	first := readPongForTest(t, inbox)
	if first != base.UnixMilli() {
		t.Fatalf("injected PONG timestamp = %d, want %d", first, base.UnixMilli())
	}

	logicalClock.AdvanceBy(37 * time.Millisecond)
	if err := pp.Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute after AdvanceBy: %v", err)
	}
	second := readPongForTest(t, inbox)
	if second != base.Add(37*time.Millisecond).UnixMilli() {
		t.Fatalf("advanced PONG timestamp = %d, want %d", second, base.Add(37*time.Millisecond).UnixMilli())
	}
}

func TestPingPongSubsystem_ExplicitNilClockUsesWallTime(t *testing.T) {
	inbox := messages.NewTypedBuffer[messages.KernelDeltaRequest](8)
	pp := subsystems.NewPingPongWithClock(inbox, nil, nil)
	ls := &state.LoopState{
		Mode: state.DuplexSession,
		Inputs: state.Buffers{UserControlPlaneMessage: []messages.Message{
			pingMessageForTest(),
		}},
	}
	before := time.Now().UnixMilli()
	if err := pp.Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := readPongForTest(t, inbox)
	after := time.Now().UnixMilli()
	if got < before || got > after {
		t.Fatalf("nil-clock PONG timestamp = %d, want wall-time range [%d,%d]", got, before, after)
	}
}

// TestPingPongSubsystem_NoOpInNonSession verifies ping/pong is a no-op outside DuplexSession.
func TestPingPongSubsystem_NoOpInNonSession(t *testing.T) {
	inbox := messages.NewTypedBuffer[messages.KernelDeltaRequest](8)
	pp := subsystems.NewPingPong(inbox, nil)

	ls := &state.LoopState{
		Mode: state.ModeAskOnce, // Not session mode.
	}
	ls.Outputs.KernelDeltaInbox = inbox
	ls.Inputs.UserControlPlaneMessage = []messages.Message{
		{
			Role: messages.RoleUser,
			ContentParts: []messages.ContentPart{
				messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypePing},
			},
		},
	}

	if err := pp.Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Should have NO pong.
	_, ok := inbox.Read()
	if ok {
		t.Error("PONG should not be emitted in non-session mode")
	}
}

// TestPingPongSubsystem_MultiplePings verifies multiple pings produce multiple pongs.
func TestPingPongSubsystem_MultiplePings(t *testing.T) {
	inbox := messages.NewTypedBuffer[messages.KernelDeltaRequest](8)
	pp := subsystems.NewPingPong(inbox, nil)

	pingMsg := messages.Message{
		Role: messages.RoleUser,
		ContentParts: []messages.ContentPart{
			messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypePing},
		},
	}

	ls := &state.LoopState{
		Mode: state.DuplexSession,
	}
	ls.Outputs.KernelDeltaInbox = inbox
	ls.Inputs.UserControlPlaneMessage = []messages.Message{pingMsg, pingMsg, pingMsg}

	if err := pp.Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	pongCount := 0
	for {
		_, ok := inbox.Read()
		if !ok {
			break
		}
		pongCount++
	}
	if pongCount != 3 {
		t.Errorf("pong count: got %d, want 3", pongCount)
	}
}

func TestPingPongSubsystem_MultiplePingPartsRemainOnePongPerMessage(t *testing.T) {
	inbox := messages.NewTypedBuffer[messages.KernelDeltaRequest](8)
	pp := subsystems.NewPingPongWithClock(inbox, nil, clock.NewDeterministic(time.Unix(1700000000, 0).UTC(), time.Millisecond))
	ls := &state.LoopState{
		Mode: state.DuplexSession,
		Inputs: state.Buffers{UserControlPlaneMessage: []messages.Message{{
			Role: messages.RoleUser,
			ContentParts: []messages.ContentPart{
				messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypePing},
				messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypePing},
			},
		}}},
	}
	if err := pp.Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	_ = readPongForTest(t, inbox)
	if _, ok := inbox.Read(); ok {
		t.Fatal("multiple ping parts in one message emitted more than one PONG")
	}
}

func TestPingPongSubsystem_IgnoresNonPingMessage(t *testing.T) {
	inbox := messages.NewTypedBuffer[messages.KernelDeltaRequest](8)
	pp := subsystems.NewPingPong(inbox, nil)
	ls := &state.LoopState{
		Mode: state.DuplexSession,
		Inputs: state.Buffers{UserControlPlaneMessage: []messages.Message{{
			Role:         messages.RoleUser,
			ContentParts: []messages.ContentPart{messages.TextPart{Text: "not a ping"}},
		}}},
	}
	if err := pp.Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, ok := inbox.Read(); ok {
		t.Fatal("non-ping message emitted a PONG")
	}
}

func pingMessageForTest() messages.Message {
	return messages.Message{
		Role: messages.RoleUser,
		ContentParts: []messages.ContentPart{
			messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypePing},
		},
	}
}

func readPongForTest(t *testing.T, inbox *messages.TypedBuffer[messages.KernelDeltaRequest]) int64 {
	t.Helper()
	req, ok := inbox.Read()
	if !ok {
		t.Fatal("expected PONG in kernel delta inbox")
	}
	if req.Delta.Type != messages.StreamTypePong {
		t.Fatalf("delta type = %q, want %q", req.Delta.Type, messages.StreamTypePong)
	}
	value, ok := req.Delta.Value.(*messages.PongValue)
	if !ok || value == nil {
		t.Fatalf("PONG value = %T, want *messages.PongValue", req.Delta.Value)
	}
	return value.Timestamp
}

type pingClockInferencer struct {
	connected chan *pingClockSession
}

func (i *pingClockInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	session := newPingClockSession()
	if !session.recv.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("ping-clock-test", "session"),
	}) {
		return nil, errors.New("session-open fixture write failed")
	}
	i.connected <- session
	return session, nil
}

type pingClockSession struct {
	recv      *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	closeOnce sync.Once
}

func newPingClockSession() *pingClockSession {
	return &pingClockSession{
		recv: messages.NewTypedBuffer[messages.StreamMessage](32),
		done: make(chan struct{}),
	}
}

func (s *pingClockSession) Send(ctx context.Context, _ messages.StreamMessage) bool {
	if ctx == nil {
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case <-s.done:
		return false
	default:
		return true
	}
}

func (s *pingClockSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.recv }

func (s *pingClockSession) Done() <-chan struct{} { return s.done }

func (s *pingClockSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

func startPingClockLoop(t *testing.T, source clock.TimerSource) (*agentloop.AgentLoop, *pingClockSession, *clock.Deterministic) {
	t.Helper()
	inferencer := &pingClockInferencer{connected: make(chan *pingClockSession, 1)}
	options := []agentloop.Option{
		agentloop.WithMode(engine.DuplexSession),
		agentloop.WithSessionInferencer(inferencer),
		agentloop.WithBufferCapacity(32),
	}
	if source != nil {
		options = append(options, agentloop.WithClock(source))
	}
	loop, err := agentloop.New(options...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- loop.Run(runCtx) }()

	watchdog, stopWatchdog := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopWatchdog()
	var session *pingClockSession
	select {
	case session = <-inferencer.connected:
	case <-watchdog.Done():
		t.Fatalf("session did not connect: %v", watchdog.Err())
	}
	if err := waitForPingClockDelta(watchdog, loop.Deltas(), messages.StreamTypeSessionOpen); err != nil {
		t.Fatalf("SESSION.OPEN: %v", err)
	}

	t.Cleanup(func() {
		cancel()
		select {
		case err := <-runDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("Run after cancellation: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Run did not join before watchdog")
		}
		if err := session.Close(); err != nil {
			t.Errorf("session close: %v", err)
		}
	})

	var deterministic *clock.Deterministic
	if source != nil {
		deterministic, _ = source.(*clock.Deterministic)
	}
	return loop, session, deterministic
}

func waitForPingClockDelta(ctx context.Context, deltas *messages.TypedBuffer[messages.StreamMessage], want messages.StreamMessageType) error {
	for {
		delta, ok := deltas.ReadBlockingContext(ctx)
		if !ok {
			return ctx.Err()
		}
		if delta.Type == want {
			return nil
		}
	}
}

func sendPingForClockTest(t *testing.T, loop *agentloop.AgentLoop) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := loop.Send(ctx, []messages.Message{{
		Role: messages.RoleUser,
		ContentParts: []messages.ContentPart{
			messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypePing},
		},
	}}); err != nil {
		t.Fatalf("Send ping: %v", err)
	}
}

func readPingClockPong(t *testing.T, loop *agentloop.AgentLoop) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		delta, ok := loop.Deltas().ReadBlockingContext(ctx)
		if !ok {
			t.Fatalf("PONG: %v", ctx.Err())
		}
		if delta.Type != messages.StreamTypePong {
			continue
		}
		value, ok := delta.Value.(*messages.PongValue)
		if !ok || value == nil {
			t.Fatalf("PONG value = %T, want *messages.PongValue", delta.Value)
		}
		return value.Timestamp
	}
}

func TestPingClock_PublicLoopUsesConfiguredClock(t *testing.T) {
	base := time.Date(2026, time.January, 2, 3, 4, 5, 123456000, time.UTC)
	logicalClock := clock.NewDeterministic(base, time.Millisecond)
	loop, _, deterministic := startPingClockLoop(t, logicalClock)
	if deterministic != logicalClock {
		t.Fatal("test helper did not retain configured deterministic clock")
	}

	sendPingForClockTest(t, loop)
	if got := readPingClockPong(t, loop); got != base.UnixMilli() {
		t.Fatalf("first PONG timestamp = %d, want %d", got, base.UnixMilli())
	}

	sendPingForClockTest(t, loop)
	if got := readPingClockPong(t, loop); got != base.UnixMilli() {
		t.Fatalf("frozen PONG timestamp = %d, want %d", got, base.UnixMilli())
	}

	deterministic.AdvanceBy(37 * time.Millisecond)
	sendPingForClockTest(t, loop)
	want := base.Add(37 * time.Millisecond).UnixMilli()
	if got := readPingClockPong(t, loop); got != want {
		t.Fatalf("advanced PONG timestamp = %d, want %d", got, want)
	}
}

func TestPingClock_PublicLoopEmitsOnePongPerPingMessage(t *testing.T) {
	base := time.Unix(1700000000, 123000000).UTC()
	logicalClock := clock.NewDeterministic(base, time.Millisecond)
	loop, _, _ := startPingClockLoop(t, logicalClock)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ping := pingMessageForTest()
	if err := loop.Send(ctx, []messages.Message{ping, ping, ping}); err != nil {
		t.Fatalf("Send pings: %v", err)
	}
	for i := 0; i < 3; i++ {
		if got := readPingClockPong(t, loop); got != base.UnixMilli() {
			t.Fatalf("PONG[%d] timestamp = %d, want %d", i, got, base.UnixMilli())
		}
	}
}

func TestPingClock_PublicLoopNilClockUsesWallTime(t *testing.T) {
	loop, _, _ := startPingClockLoop(t, nil)
	before := time.Now().UnixMilli()
	sendPingForClockTest(t, loop)
	got := readPingClockPong(t, loop)
	after := time.Now().UnixMilli()
	if got < before || got > after {
		t.Fatalf("nil-clock PONG timestamp = %d, want wall-time range [%d,%d]", got, before, after)
	}
}
