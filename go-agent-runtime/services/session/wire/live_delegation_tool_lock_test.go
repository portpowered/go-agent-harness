package wire

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/livedelegation"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// The voice loop's browser call holds the session's browser lock: a
// delegation's call of a different browser tool waits for it, while an
// ungrouped tool and a filesystem write run at once. Two delegations'
// filesystem writes take turns, and the waiting browser call runs as soon
// as the voice loop's call ends.
func TestDelegationsAndTheVoiceLoopShareToolResourceGroupLocks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		provider := newRecordingLiveSession()
		writeProvider(t, provider, messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("provider-session", "audio_inference")})
		voiceTool := &releasedTool{started: make(chan struct{}), release: make(chan struct{})}
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
				Executor: voiceTool,
				Definitions: []messages.ToolDefinition{
					{Name: "webmcp_open_tab"}, {Name: "webmcp_list_tabs"}, {Name: "write_file"}, {Name: "edit_file"}, {Name: "lookup_order"},
				},
			},
			Delegation: &livedelegation.Policy{},
		})
		if err != nil {
			t.Fatalf("OpenLive: %v", err)
		}
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

		browser := acquireAsync(t, binding.ToolLock, "webmcp_list_tabs")
		lookup := acquireAsync(t, binding.ToolLock, "lookup_order")
		write := acquireAsync(t, binding.ToolLock, "write_file")
		synctest.Wait()
		if browser.held() {
			t.Fatal("a delegation's webmcp_list_tabs ran while the voice loop's webmcp_open_tab was running")
		}
		if !lookup.held() || !write.held() {
			t.Fatalf("ungrouped lookup held=%t, filesystem write held=%t; want both admitted at once", lookup.held(), write.held())
		}
		edit := acquireAsync(t, binding.ToolLock, "edit_file")
		synctest.Wait()
		if edit.held() {
			t.Fatal("a second delegation's edit_file ran during another's write_file")
		}
		write.release()
		synctest.Wait()
		if !edit.held() {
			t.Fatal("edit_file still waits after write_file ended")
		}

		close(voiceTool.release)
		synctest.Wait()
		if !browser.held() {
			t.Fatal("the delegation's browser call still waits after the voice loop's call ended")
		}
		for _, call := range []*heldLock{browser, lookup, edit} {
			call.release()
		}
		if err := handle.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
}

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
	mu      sync.Mutex
	done    bool
	release func()
}

func (h *heldLock) held() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.done
}

func acquireAsync(t *testing.T, lock livedelegation.ToolLock, name string) *heldLock {
	t.Helper()
	call := &heldLock{}
	released := make(chan struct{})
	call.release = sync.OnceFunc(func() { close(released) })
	go func() {
		unlock, err := lock(t.Context(), name)
		if err != nil {
			t.Errorf("acquire %s: %v", name, err)
			return
		}
		call.mu.Lock()
		call.done = true
		call.mu.Unlock()
		<-released
		unlock()
	}()
	return call
}
