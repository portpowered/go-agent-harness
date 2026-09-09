package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

const watchdogDuration = 3 * time.Second

type fixtureInferencer struct {
	session *fixtureSession
}

func (f *fixtureInferencer) ConnectSession(context.Context) (messages.Session, error) {
	if f.session == nil {
		f.session = newFixtureSession()
	}
	if !f.session.recv.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("c17-fixture", "session"),
	}) {
		return nil, errors.New("fixture session open was not admitted")
	}
	return f.session, nil
}

type fixtureSession struct {
	recv      *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	closeOnce sync.Once
}

func newFixtureSession() *fixtureSession {
	return &fixtureSession{
		recv: messages.NewTypedBuffer[messages.StreamMessage](32),
		done: make(chan struct{}),
	}
}

func (s *fixtureSession) Send(ctx context.Context, _ messages.StreamMessage) bool {
	return ctx == nil || ctx.Err() == nil
}

func (s *fixtureSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recv
}

func (s *fixtureSession) Done() <-chan struct{} { return s.done }

func (s *fixtureSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

func pingMessage() messages.Message {
	return messages.Message{
		Role: messages.RoleUser,
		ContentParts: []messages.ContentPart{
			messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypePing},
		},
	}
}

func readPong(ctx context.Context, deltas *messages.TypedBuffer[messages.StreamMessage]) (int64, error) {
	for {
		delta, ok := deltas.ReadBlockingContext(ctx)
		if !ok {
			return 0, ctx.Err()
		}
		if delta.Type != messages.StreamTypePong {
			continue
		}
		value, ok := delta.Value.(*messages.PongValue)
		if !ok || value == nil {
			return 0, fmt.Errorf("PONG value has type %T", delta.Value)
		}
		return value.Timestamp, nil
	}
}

func waitForSessionOpen(ctx context.Context, deltas *messages.TypedBuffer[messages.StreamMessage]) error {
	for {
		delta, ok := deltas.ReadBlockingContext(ctx)
		if !ok {
			return ctx.Err()
		}
		if delta.Type == messages.StreamTypeSessionOpen {
			return nil
		}
	}
}

func run() (err error) {
	base := time.Date(2026, time.January, 2, 3, 4, 5, 123456000, time.UTC)
	expected := base.UnixMilli()
	logicalClock := clock.NewDeterministic(base, time.Millisecond)
	fixture := &fixtureInferencer{}
	loop, err := agentloop.New(
		agentloop.WithMode(engine.DuplexSession),
		agentloop.WithSessionInferencer(fixture),
		agentloop.WithClock(logicalClock),
		agentloop.WithBufferCapacity(32),
	)
	if err != nil {
		return fmt.Errorf("construct public loop: %w", err)
	}

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	runDone := make(chan error, 1)
	go func() { runDone <- loop.Run(runCtx) }()

	shutdown := func() error {
		cancelRun()
		select {
		case runErr := <-runDone:
			if runErr != nil && !errors.Is(runErr, context.Canceled) {
				return fmt.Errorf("join public loop: %w", runErr)
			}
		case <-time.After(watchdogDuration):
			return errors.New("public loop did not join before watchdog")
		}
		if fixture.session != nil {
			return fixture.session.Close()
		}
		return nil
	}
	defer func() {
		if shutdownErr := shutdown(); err == nil && shutdownErr != nil {
			err = shutdownErr
		}
	}()

	openCtx, cancelOpen := context.WithTimeout(context.Background(), watchdogDuration)
	defer cancelOpen()
	if err := waitForSessionOpen(openCtx, loop.Deltas()); err != nil {
		return fmt.Errorf("wait for session setup: %w", err)
	}

	send := func() (int64, error) {
		if err := loop.Send(context.Background(), []messages.Message{pingMessage()}); err != nil {
			return 0, fmt.Errorf("send ping: %w", err)
		}
		pongCtx, cancelPong := context.WithTimeout(context.Background(), watchdogDuration)
		defer cancelPong()
		return readPong(pongCtx, loop.Deltas())
	}

	first, err := send()
	if err != nil {
		return err
	}
	fmt.Printf("pong[1] observed=%d expected=%d\n", first, expected)
	if first != expected {
		return fmt.Errorf("PONG timestamp mismatch: observed=%d expected=%d", first, expected)
	}

	second, err := send()
	if err != nil {
		return err
	}
	fmt.Printf("pong[2] observed=%d expected=%d\n", second, expected)
	if second != expected {
		return fmt.Errorf("frozen PONG timestamp changed: observed=%d expected=%d", second, expected)
	}

	advance := 37 * time.Millisecond
	logicalClock.AdvanceBy(advance)
	third, err := send()
	if err != nil {
		return err
	}
	expectedAdvanced := expected + advance.Milliseconds()
	fmt.Printf("pong[3] observed=%d expected=%d\n", third, expectedAdvanced)
	if third != expectedAdvanced {
		return fmt.Errorf("advanced PONG timestamp mismatch: observed=%d expected=%d", third, expectedAdvanced)
	}
	fmt.Println("c17 public-loop clock reproduction passed")
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "c17 public-loop clock reproduction failed: %v\n", err)
		os.Exit(1)
	}
}
