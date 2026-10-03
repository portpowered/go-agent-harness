package live

import (
	"context"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

// Resource groups of the tools that share host state. A call holds its
// group's lock while it runs, so the voice loop and every delegation of a
// session drive that state one call at a time.
const (
	// browserToolGroup holds every browser and WebMCP tool: they drive one
	// set of tabs and pages, whichever tool the call names.
	browserToolGroup = "browser"
	// filesystemWriteToolGroup holds the filesystem write tools, so two
	// writers never interleave on one file.
	filesystemWriteToolGroup = "filesystem-write"
)

// toolResourceGroup names the lock a call of name takes, or "" for a tool
// that is safe to run concurrently. A tool the session's executor routes to
// the browser surface (tools.BrowserToolRouter: the WebMCP broker's tools
// and the page tools discovered from the selected page, such as a site's
// google_maps_* tools) is in the browser group; so are the stable broker
// names, for an executor that does not report its routing. Other tools the
// interactive policy admits as long-running (display, exec and remote
// operations) each form their own group.
func (l *toolLocks) toolResourceGroup(name string) string {
	switch {
	case l.browser != nil && l.browser.IsBrowserTool(name),
		strings.HasPrefix(name, "webmcp_") || strings.HasPrefix(name, "browser_") || name == tools.ShowPageToolName:
		return browserToolGroup
	case name == "write_file" || name == "edit_file":
		return filesystemWriteToolGroup
	case l.policy != nil && l.policy.ClassForTool(name) == tools.InteractiveToolClassBoundedLongRunning:
		return "tool:" + name
	default:
		return ""
	}
}

// toolLocks is a session's resource-group locks. The voice loop's tool
// executor and the delegation executor both acquire them; nothing else
// does, and a call holds one lock at most, so no lock order can cycle.
type toolLocks struct {
	policy  tools.InteractiveToolPolicy
	browser tools.BrowserToolRouter

	mu    sync.Mutex
	locks map[string]chan struct{}
}

// newToolLocks groups the tools of executor; its browser routing is used
// when it reports one.
func newToolLocks(policy tools.InteractiveToolPolicy, executor messages.ToolExecutor) *toolLocks {
	locks := &toolLocks{policy: policy, locks: make(map[string]chan struct{})}
	if browser, ok := executor.(tools.BrowserToolRouter); ok {
		locks.browser = browser
	}
	return locks
}

// acquire waits for the lock of name's resource group, or returns a no-op
// release for an ungrouped tool. It gives up with ctx's cause when ctx ends
// first, so an interrupted voice call or an ended delegation never waits on.
func (l *toolLocks) acquire(ctx context.Context, name string) (func(), error) {
	group := l.toolResourceGroup(name)
	if group == "" {
		return func() {}, nil
	}
	l.mu.Lock()
	lock, ok := l.locks[group]
	if !ok {
		lock = make(chan struct{}, 1)
		l.locks[group] = lock
	}
	l.mu.Unlock()
	select {
	case lock <- struct{}{}:
		return func() { <-lock }, nil
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}

// lockedToolExecutor runs the voice loop's tool calls under their resource
// group's lock.
type lockedToolExecutor struct {
	inner messages.ToolExecutor
	locks *toolLocks
}

func (e lockedToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	release, err := e.locks.acquire(ctx, call.Name)
	if err != nil {
		return messages.ToolCallResponse{}, err
	}
	defer release()
	return e.inner.Execute(ctx, call)
}
