package externalconsumer

import (
	"context"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionlive"
	sessionlivewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionlive/wire"
)

type session struct {
	recv *sessionlive.StreamBuffer
	done chan struct{}
	once sync.Once
}

func (s *session) Send(context.Context, sessionlive.StreamMessage) bool { return true }
func (s *session) Receive() *sessionlive.StreamBuffer                   { return s.recv }
func (s *session) Done() <-chan struct{}                                { return s.done }
func (s *session) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}
func (s *session) RequestResponse(context.Context) sessionlive.SessionSendOutcome {
	return sessionlive.SessionSendOutcome{Status: "succeeded"}
}
func (*session) SupportsResponseRequests() bool { return true }

type inferencer struct{ current *session }

func (i *inferencer) ConnectSession(ctx context.Context) (sessionlive.Session, error) {
	for _, typ := range []sessionlive.StreamMessageType{
		sessionlive.StreamTypeSessionOpen,
		sessionlive.StreamTypeSessionCreated,
		sessionlive.StreamTypeTextDelta,
		sessionlive.StreamTypeMessageEnd,
	} {
		if !i.current.recv.Write(ctx, sessionlive.StreamMessage{Type: typ}) {
			return nil, ctx.Err()
		}
	}
	return i.current, nil
}

func TestExternalConsumerRunsAndCloses(t *testing.T) {
	provider := &session{recv: sessionlive.StreamBuffers{}.New(16), done: make(chan struct{})}
	loop, err := sessionlivewire.NewService().NewLoop(sessionlive.LoopOptions{Inferencer: &inferencer{current: provider}})
	if err != nil {
		t.Fatalf("NewLoop: %v", err)
	}
	capabilities := sessionlive.SessionCapabilities{}
	if !capabilities.SupportsResponseRequests(provider) || !capabilities.RequestResponse(context.Background(), provider).OK() {
		t.Fatal("response request capability was not observable")
	}
	var order []sessionlive.StreamMessageType
	err = sessionlivewire.NewService().Run(context.Background(), sessionlive.RunOptions{
		Loop: loop,
		Handler: func(_ context.Context, _ *sessionlive.Loop, msg sessionlive.StreamMessage, _ sessionlive.MessageContext) (sessionlive.MessageResult, error) {
			order = append(order, msg.Type)
			return sessionlive.MessageResult{Stop: msg.Type == sessionlive.StreamTypeMessageEnd}, nil
		},
		WaitForStragglers:  func(context.Context) error { return nil },
		StopOwnedResources: func(context.Context) error { return provider.Close() },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(order) != 4 || order[2] != sessionlive.StreamTypeTextDelta || order[3] != sessionlive.StreamTypeMessageEnd {
		t.Fatalf("order = %v, want open, created, text delta, message end", order)
	}
}
