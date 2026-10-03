package wire

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/livedelegation"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// The voice loop's browser call holds the session's browser lock: a
// delegation's call of a different browser tool waits for it, and so does a
// page tool (google_maps_directions) that the session's executor routes to
// the browser, whatever its name, while an ungrouped tool and a filesystem
// write run at once. Two delegations'
// filesystem writes take turns, and the waiting browser call runs as soon
// as the voice loop's call ends.
func TestDelegationsAndTheVoiceLoopShareToolResourceGroupLocks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		provider := newRecordingLiveSession()
		writeProvider(t, provider, messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("provider-session", "audio_inference")})
		voiceTool := &releasedTool{started: make(chan struct{}), release: make(chan struct{})}
		releaseVoice := sync.OnceFunc(func() { close(voiceTool.release) })
		calls := &lockCalls{}
		// A failed assertion must not strand the bubble: the cleanup releases
		// the voice call and every acquisition, and ends those still waiting.
		t.Cleanup(func() {
			releaseVoice()
			calls.releaseAll()
		})
		delegations := &bindingCapture{opened: make(chan livedelegation.Binding, 1)}
		service := NewLiveService(LiveDependencies{
			InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
				return sessionInferencer{session: provider}, nil
			},
			Clock: platformclock.Real{}.Now, Scheduler: platformclock.Real{}, Delegations: delegations,
		})
		handle, err := service.OpenLive(t.Context(), session.LiveRequest{
			SessionID: "tool-locks", Provider: ackProvider, OpeningPrompt: "open the shop",
			Capabilities: &session.LiveCapabilities{
				Executor: pageRoutedTool{voiceTool},
				Definitions: []messages.ToolDefinition{
					{Name: "webmcp_open_tab"}, {Name: "webmcp_list_tabs"}, {Name: "write_file"}, {Name: "edit_file"}, {Name: "lookup_order"}, {Name: pageTool},
				},
			},
			Delegation: &livedelegation.Policy{},
		})
		if err != nil {
			t.Fatalf("OpenLive: %v", err)
		}
		t.Cleanup(func() {
			if err := handle.Close(); err != nil {
				t.Logf("close live handle: %v", err)
			}
		})
		if err := handle.Start(t.Context()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		go func() {
			for range handle.Events() {
			}
		}()
		binding := <-delegations.opened
		if binding.ToolLock == nil {
			t.Fatal("delegation binding has no tool lock")
		}
		waitForSentText(t, provider, "open the shop")
		writeProvider(t, provider, toolCallResponse("call_voice", "webmcp_open_tab")...)
		<-voiceTool.started

		browser := calls.acquire(binding.ToolLock, "webmcp_list_tabs")
		page := calls.acquire(binding.ToolLock, pageTool)
		lookup := calls.acquire(binding.ToolLock, "lookup_order")
		write := calls.acquire(binding.ToolLock, "write_file")
		synctest.Wait()
		if browser.held() || page.held() {
			t.Fatalf("while the voice loop's webmcp_open_tab ran, webmcp_list_tabs held=%t, %s held=%t; want both waiting", browser.held(), pageTool, page.held())
		}
		if !lookup.held() || !write.held() {
			t.Fatalf("ungrouped lookup held=%t, filesystem write held=%t; want both admitted at once", lookup.held(), write.held())
		}
		edit := calls.acquire(binding.ToolLock, "edit_file")
		synctest.Wait()
		if edit.held() {
			t.Fatal("a second delegation's edit_file ran during another's write_file")
		}
		write.release()
		synctest.Wait()
		if !edit.held() {
			t.Fatal("edit_file still waits after write_file ended")
		}

		releaseVoice()
		synctest.Wait()
		if browser.held() == page.held() {
			t.Fatalf("after the voice loop's call: webmcp_list_tabs held=%t, %s held=%t; want exactly one browser call admitted", browser.held(), pageTool, page.held())
		}
		first, second := browser, page
		if page.held() {
			first, second = page, browser
		}
		first.release()
		synctest.Wait()
		if !second.held() {
			t.Fatal("the second browser call still waits after the first ended")
		}
		calls.releaseAll()
		if err := calls.err(); err != nil {
			t.Fatal(err)
		}
	})
}

// pageTool is a site's page tool: its name says nothing about the browser.
const pageTool = "google_maps_directions"

// pageRoutedTool is the session's executor; like the composed tool surface,
// it reports the page tool as routed to the browser.
type pageRoutedTool struct{ *releasedTool }

func (pageRoutedTool) IsBrowserTool(name string) bool { return name == pageTool }

// bindingCapture is a delegation service that hands the session's binding
// to the test and runs nothing.
type bindingCapture struct{ opened chan livedelegation.Binding }

func (c *bindingCapture) Open(_ context.Context, binding livedelegation.Binding) (livedelegation.Executor, error) {
	c.opened <- binding
	return idleExecutor{}, nil
}

type idleExecutor struct{}

func (idleExecutor) Submit(messages.DelegationCreatedValue) error { return nil }
func (idleExecutor) Cancel(string) bool                           { return false }
func (idleExecutor) Close() error                                 { return nil }

// heldLock is one asynchronous ToolLock acquisition.
type heldLock struct {
	mu       sync.Mutex
	done     bool
	failure  error
	release  func()
	released chan struct{}
}

func (h *heldLock) held() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.done
}

// lockCalls owns the test's acquisitions. Its goroutines never touch the
// test: a failed acquisition is kept and reported by err, and releaseAll
// ends every acquisition, a waiting one by cancelling its context.
type lockCalls struct {
	mu     sync.Mutex
	calls  []*heldLock
	cancel []context.CancelFunc
}

func (c *lockCalls) acquire(lock livedelegation.ToolLock, name string) *heldLock {
	ctx, cancel := context.WithCancel(context.Background())
	call := &heldLock{released: make(chan struct{})}
	call.release = sync.OnceFunc(func() { close(call.released) })
	c.mu.Lock()
	c.calls, c.cancel = append(c.calls, call), append(c.cancel, cancel)
	c.mu.Unlock()
	go func() {
		unlock, err := lock(ctx, name)
		call.mu.Lock()
		call.done, call.failure = err == nil, err
		call.mu.Unlock()
		if err != nil {
			return
		}
		<-call.released
		unlock()
	}()
	return call
}

func (c *lockCalls) releaseAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for index, call := range c.calls {
		call.release()
		c.cancel[index]()
	}
}

// err reports the first acquisition that failed.
func (c *lockCalls) err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, call := range c.calls {
		call.mu.Lock()
		failure := call.failure
		call.mu.Unlock()
		if failure != nil && !errors.Is(failure, context.Canceled) {
			return failure
		}
	}
	return nil
}
