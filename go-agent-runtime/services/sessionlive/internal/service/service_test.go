package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionlive"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type scriptedInferencer struct {
	session *scriptedSession
	events  []messages.StreamMessage
}

func (i *scriptedInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	for _, event := range i.events {
		if !i.session.recv.Write(ctx, event) {
			return nil, ctx.Err()
		}
	}
	return i.session, nil
}

type scriptedSession struct {
	recv *messages.TypedBuffer[messages.StreamMessage]
	done chan struct{}

	mu        sync.Mutex
	sent      []messages.StreamMessage
	closeErr  error
	closeOnce sync.Once
}

func newScriptedSession() *scriptedSession {
	return &scriptedSession{
		recv: messages.NewTypedBuffer[messages.StreamMessage](64),
		done: make(chan struct{}),
	}
}

func (s *scriptedSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	s.mu.Lock()
	s.sent = append(s.sent, msg)
	s.mu.Unlock()
	return true
}

func (s *scriptedSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.recv }
func (s *scriptedSession) Done() <-chan struct{}                                  { return s.done }

func (s *scriptedSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return s.closeErr
}

func (s *scriptedSession) sentMessages() []messages.StreamMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]messages.StreamMessage(nil), s.sent...)
}

func TestRunDrainsFinalOutputAndJoinsCleanup(t *testing.T) {
	session := newScriptedSession()
	providerErr := errors.New("provider cleanup")
	inferencer := &scriptedInferencer{
		session: session,
		events: []messages.StreamMessage{
			{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("s1", "test")},
			{Type: messages.StreamTypeSessionCreated, Value: messages.NewSessionCreatedValue("s1", "test")},
			{Type: messages.StreamTypeTextStart, Value: messages.NewTextStartValue()},
			{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("final")},
			{Type: messages.StreamTypeTextEnd, Value: messages.NewTextEndValue()},
			{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
		},
	}
	loop, err := New().NewLoop(sessionlive.LoopOptions{Inferencer: inferencer})
	if err != nil {
		t.Fatalf("NewLoop: %v", err)
	}
	var seen []messages.StreamMessageType
	err = New().Run(context.Background(), sessionlive.RunOptions{
		Loop: loop,
		Handler: func(_ context.Context, _ *sessionlive.Loop, msg messages.StreamMessage, _ sessionlive.MessageContext) (sessionlive.MessageResult, error) {
			seen = append(seen, msg.Type)
			if msg.Type == messages.StreamTypeMessageEnd {
				return sessionlive.MessageResult{Stop: true}, nil
			}
			return sessionlive.MessageResult{}, nil
		},
		WaitForStragglers: func(context.Context) error { return nil },
		StopOwnedResources: func(context.Context) error {
			return errors.Join(providerErr, session.Close())
		},
	})
	if !errors.Is(err, providerErr) {
		t.Fatalf("Run error = %v, want cleanup identity", err)
	}
	for _, want := range []messages.StreamMessageType{messages.StreamTypeSessionOpen, messages.StreamTypeTextDelta, messages.StreamTypeMessageEnd} {
		if !containsMessageType(seen, want) {
			t.Fatalf("handled messages = %v, missing %s", seen, want)
		}
	}
}

func TestNewLoopCopiesSessionConfigAndToolDefinitions(t *testing.T) {
	session := newScriptedSession()
	inferencer := &scriptedInferencer{
		session: session,
		events: []messages.StreamMessage{
			{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("s2", "test")},
			{Type: messages.StreamTypeSessionCreated, Value: messages.NewSessionCreatedValue("s2", "test")},
		},
	}
	tools := []messages.ToolDefinition{{Name: "z", Parameters: []messages.ToolParameter{{Name: "b"}}}, {Name: "a"}}
	config := &sessionlive.SessionUpdateConfig{Instructions: "be exact", Modalities: []string{"audio"}}
	loop, err := New().NewLoop(sessionlive.LoopOptions{
		Inferencer:               inferencer,
		SessionConfig:            config,
		ToolExecutor:             &messages.DefaultToolExecutor{},
		ToolDefinitions:          tools,
		AdvertiseToolDefinitions: true,
	})
	if err != nil {
		t.Fatalf("NewLoop: %v", err)
	}
	tools[0].Name = "mutated"
	config.Instructions = "mutated"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	runResult := make(chan error, 1)
	closeResult := make(chan error, 1)
	go func() {
		runResult <- loop.Run(ctx)
		closeResult <- session.Close()
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-runResult; !isCancellation(err) {
			t.Errorf("loop.Run: %v", err)
		}
		if err := <-closeResult; err != nil {
			t.Errorf("session.Close: %v", err)
		}
	})
	deadline := time.After(time.Second)
	for len(session.sentMessages()) == 0 {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for session configuration; sent=%#v", session.sentMessages())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	for _, msg := range session.sentMessages() {
		if msg.Type != messages.StreamTypeSessionUpdate {
			continue
		}
		value, ok := msg.Value.(*messages.SessionUpdateValue)
		if !ok || value.Instructions != "be exact" || len(value.Tools) != 2 || value.Tools[0].Name != "a" || value.Tools[1].Name != "z" {
			t.Fatalf("session update = %#v, want cloned instructions and canonical tools", msg.Value)
		}
		return
	}
	t.Fatal("session configuration was not sent")
}

func TestRunPreservesCancellationAndDeadlineIdentity(t *testing.T) {
	t.Run("parent cancellation", func(t *testing.T) {
		session := newScriptedSession()
		loop, err := New().NewLoop(sessionlive.LoopOptions{Inferencer: &scriptedInferencer{session: session}})
		if err != nil {
			t.Fatalf("NewLoop: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			result <- New().Run(ctx, sessionlive.RunOptions{
				Loop: loop,
				Handler: func(context.Context, *sessionlive.Loop, messages.StreamMessage, sessionlive.MessageContext) (sessionlive.MessageResult, error) {
					return sessionlive.MessageResult{}, nil
				},
				WaitForStragglers: func(context.Context) error { return nil },
				StopOwnedResources: func(context.Context) error {
					return session.Close()
				},
			})
		}()
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want cancellation identity", err)
		}
	})

	t.Run("max duration", func(t *testing.T) {
		session := newScriptedSession()
		loop, err := New().NewLoop(sessionlive.LoopOptions{Inferencer: &scriptedInferencer{session: session}})
		if err != nil {
			t.Fatalf("NewLoop: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err = New().Run(ctx, sessionlive.RunOptions{
			Loop:        loop,
			MaxDuration: time.Millisecond,
			Handler: func(context.Context, *sessionlive.Loop, messages.StreamMessage, sessionlive.MessageContext) (sessionlive.MessageResult, error) {
				return sessionlive.MessageResult{}, nil
			},
			WaitForStragglers: func(context.Context) error { return nil },
			StopOwnedResources: func(context.Context) error {
				return session.Close()
			},
		})
		if !errors.Is(err, sessionlive.ErrMaxDurationExpired) {
			t.Fatalf("Run error = %v, want max-duration identity", err)
		}
	})
}

func TestRunJoinsProviderAndCleanupErrors(t *testing.T) {
	providerErr := errors.New("provider failed")
	cleanupErr := errors.New("cleanup failed")
	session := newScriptedSession()
	loop, err := New().NewLoop(sessionlive.LoopOptions{Inferencer: &scriptedInferencer{session: session}})
	if err != nil {
		t.Fatalf("NewLoop: %v", err)
	}
	providerErrors := make(chan error, 1)
	providerErrors <- providerErr
	err = New().Run(context.Background(), sessionlive.RunOptions{
		Loop: loop,
		Handler: func(context.Context, *sessionlive.Loop, messages.StreamMessage, sessionlive.MessageContext) (sessionlive.MessageResult, error) {
			return sessionlive.MessageResult{}, nil
		},
		Errors:            providerErrors,
		WaitForStragglers: func(context.Context) error { return nil },
		StopOwnedResources: func(context.Context) error {
			return errors.Join(cleanupErr, session.Close())
		},
	})
	if !errors.Is(err, providerErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("Run error = %v, want provider and cleanup identities", err)
	}
}

func TestRunCancelsInputAfterStragglerWait(t *testing.T) {
	session := newScriptedSession()
	inferencer := &scriptedInferencer{
		session: session,
		events: []messages.StreamMessage{
			{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("ordered", "test")},
		},
	}
	loop, err := New().NewLoop(sessionlive.LoopOptions{Inferencer: inferencer})
	if err != nil {
		t.Fatalf("NewLoop: %v", err)
	}
	done := make(chan struct{})
	var inputCtx context.Context
	waitErr := errors.New("input was cancelled before straggler drain")
	stopErr := errors.New("input was not cancelled before owned cleanup")
	err = New().Run(context.Background(), sessionlive.RunOptions{
		Loop: loop,
		Handler: func(context.Context, *sessionlive.Loop, messages.StreamMessage, sessionlive.MessageContext) (sessionlive.MessageResult, error) {
			return sessionlive.MessageResult{}, nil
		},
		StartInput: func(ctx context.Context, _ *sessionlive.Loop) (<-chan error, error) {
			inputCtx = ctx
			inputErr := make(chan error, 1)
			go func() {
				<-ctx.Done()
				inputErr <- nil
			}()
			close(done)
			return inputErr, nil
		},
		Done: done,
		WaitForStragglers: func(context.Context) error {
			if inputCtx.Err() != nil {
				return waitErr
			}
			return nil
		},
		StopOwnedResources: func(context.Context) error {
			if inputCtx.Err() == nil {
				return stopErr
			}
			return session.Close()
		},
	})
	if err != nil {
		t.Fatalf("Run error = %v, want ordered input cancellation", err)
	}
}

func TestFlushPublishedDrainsAcceptedDelta(t *testing.T) {
	session := newScriptedSession()
	loop, err := New().NewLoop(sessionlive.LoopOptions{Inferencer: &scriptedInferencer{session: session}})
	if err != nil {
		t.Fatalf("NewLoop: %v", err)
	}
	want := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("post-done")}
	if !loop.Deltas().Write(context.Background(), want) {
		t.Fatal("failed to admit final delta")
	}
	var seen []messages.StreamMessageType
	if err := flushPublished(context.Background(), sessionlive.RunOptions{Handler: func(context.Context, *sessionlive.Loop, messages.StreamMessage, sessionlive.MessageContext) (sessionlive.MessageResult, error) {
		seen = append(seen, want.Type)
		return sessionlive.MessageResult{}, nil
	}}, loop); err != nil {
		t.Fatalf("flushPublished: %v", err)
	}
	if len(seen) != 1 || seen[0] != messages.StreamTypeTextDelta {
		t.Fatalf("flushed messages = %v, want final text delta", seen)
	}
}

type immediateTimerClock struct{ timer *immediateTimer }

func (c *immediateTimerClock) Now() time.Time { return time.Unix(0, 0).UTC() }
func (c *immediateTimerClock) NewTimer(time.Duration) platformclock.Timer {
	c.timer = &immediateTimer{ch: make(chan time.Time, 1)}
	c.timer.ch <- time.Unix(0, 0).UTC()
	return c.timer
}

type immediateTimer struct {
	ch      chan time.Time
	stopped atomic.Bool
}

func (t *immediateTimer) C() <-chan time.Time { return t.ch }
func (t *immediateTimer) Stop() bool {
	t.stopped.Store(true)
	return true
}

func TestRunStopsDeadlineTimer(t *testing.T) {
	session := newScriptedSession()
	loop, err := New().NewLoop(sessionlive.LoopOptions{Inferencer: &scriptedInferencer{session: session}})
	if err != nil {
		t.Fatalf("NewLoop: %v", err)
	}
	clock := &immediateTimerClock{}
	err = New().Run(context.Background(), sessionlive.RunOptions{
		Loop:        loop,
		Clock:       clock,
		MaxDuration: time.Second,
		Handler: func(context.Context, *sessionlive.Loop, messages.StreamMessage, sessionlive.MessageContext) (sessionlive.MessageResult, error) {
			return sessionlive.MessageResult{}, nil
		},
		WaitForStragglers: func(context.Context) error { return nil },
		StopOwnedResources: func(context.Context) error {
			return session.Close()
		},
	})
	if !errors.Is(err, sessionlive.ErrMaxDurationExpired) {
		t.Fatalf("Run error = %v, want max-duration identity", err)
	}
	if clock.timer == nil || !clock.timer.stopped.Load() {
		t.Fatal("deadline timer was not stopped")
	}
}

func containsMessageType(messages []messages.StreamMessageType, want messages.StreamMessageType) bool {
	for _, got := range messages {
		if got == want {
			return true
		}
	}
	return false
}
