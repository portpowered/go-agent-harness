package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

// blockingStream models a provider stream that does not finish until its
// owner closes it. It lets the handle tests exercise cancellation and join
// behavior without constructing a real provider or touching host resources.
type blockingStream struct {
	closed    chan struct{}
	closeOnce sync.Once
	closeCall atomic.Int32
}

func newBlockingStream() *blockingStream {
	return &blockingStream{closed: make(chan struct{})}
}

func (s *blockingStream) HasNext() bool {
	<-s.closed
	return false
}

func (*blockingStream) Response() agentloop.Response { return messages.StreamMessage{} }
func (*blockingStream) Err() error                   { return nil }
func (*blockingStream) Outcome() agentloop.StreamOutcome {
	return agentloop.StreamOutcome{Status: agentloop.StreamClosed}
}
func (s *blockingStream) Close() error {
	s.closeOnce.Do(func() {
		s.closeCall.Add(1)
		close(s.closed)
	})
	return nil
}

func TestHandleCloseCancelsAndClosesActiveStreams(t *testing.T) {
	openContext, openCancel := context.WithCancel(context.Background())
	defer openCancel()
	h := &handle{
		ctx:       openContext,
		cancel:    openCancel,
		closeDone: make(chan struct{}),
		active:    make(map[*ownedStream]struct{}),
	}
	stream := newBlockingStream()
	owned := &ownedStream{
		handle: h,
		stream: stream,
		done:   make(chan struct{}),
	}
	h.active[owned] = struct{}{}

	if err := h.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	select {
	case <-openContext.Done():
	default:
		t.Fatal("Close() did not cancel the Open lifetime context")
	}
	if got := stream.closeCall.Load(); got != 1 {
		t.Fatalf("stream Close calls = %d, want one", got)
	}
	h.mu.Lock()
	active := len(h.active)
	h.mu.Unlock()
	if active != 0 {
		t.Fatalf("active streams after Close() = %d, want zero", active)
	}

	// Close is safe to call repeatedly and concurrently after the first close.
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := h.Close(); err != nil {
				t.Errorf("concurrent Close() error = %v", err)
			}
		}()
	}
	wait.Wait()
}

func TestOwnedStreamCloseUnblocksReaderAndFinishesOwner(t *testing.T) {
	h := &handle{active: make(map[*ownedStream]struct{})}
	stream := newBlockingStream()
	owned := &ownedStream{handle: h, stream: stream, done: make(chan struct{})}
	h.active[owned] = struct{}{}

	readDone := make(chan bool, 1)
	go func() { readDone <- owned.HasNext() }()
	select {
	case <-readDone:
		t.Fatal("stream reader returned before Close()")
	case <-time.After(10 * time.Millisecond):
	}
	if err := owned.Close(); err != nil {
		t.Fatalf("owned stream Close() error = %v", err)
	}
	select {
	case got := <-readDone:
		if got {
			t.Fatal("stream reader returned an event after Close()")
		}
	case <-time.After(time.Second):
		t.Fatal("stream reader did not unblock after Close()")
	}
	select {
	case <-owned.done:
	default:
		t.Fatal("owned stream did not finish after Close()")
	}
}

// A host must be able to report cleanup failures after cancellation.
type failedCloseStream struct {
	*blockingStream
	err error
}

func (s *failedCloseStream) Close() error {
	return errors.Join(s.blockingStream.Close(), s.err)
}
func TestHandleCloseRetainsCleanupFailure(t *testing.T) {
	cause := errors.New("provider cleanup failed")
	h := &handle{closeDone: make(chan struct{}), active: make(map[*ownedStream]struct{})}
	stream := &ownedStream{handle: h, stream: &failedCloseStream{blockingStream: newBlockingStream(), err: cause}, done: make(chan struct{})}
	h.active[stream] = struct{}{}
	for range 2 {
		if err := h.Close(); !errors.Is(err, cause) {
			t.Fatalf("cleanup error lost: %v", err)
		}
	}
}

func TestResolvedEmptyPromptDoesNotRestoreHostSentinel(t *testing.T) {
	request := session.Request{SystemPrompt: "none"}
	resolution := ensureResolutionDefaults(request, session.Resolution{SystemPromptResolved: true})
	if resolution.SystemPrompt != "" {
		t.Fatalf("resolved empty prompt became %q", resolution.SystemPrompt)
	}
	inherited := ensureResolutionDefaults(session.Request{SystemPrompt: "literal"}, session.Resolution{})
	if inherited.SystemPrompt != "literal" {
		t.Fatalf("partial resolver lost literal prompt: %q", inherited.SystemPrompt)
	}
}
