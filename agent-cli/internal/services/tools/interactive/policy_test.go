package interactive

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const (
	fastToolName    = "read_file"
	longToolName    = "sleep"
	browserToolName = "webmcp_invoke"
	pageToolName    = "page_added_later"
	timedOutText    = "tool execution timed out"
	testFastTimeout = 40 * time.Millisecond
	testLongTimeout = 2 * time.Second
	testAckTimeout  = 20 * time.Millisecond
	testWork        = 150 * time.Millisecond
)

// contextBoundExecutor works for testWork unless its deadline ends it first.
type contextBoundExecutor struct{}

func (contextBoundExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	timer := time.NewTimer(testWork)
	defer timer.Stop()
	select {
	case <-timer.C:
		return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: "done"}, nil
	case <-ctx.Done():
		return messages.ToolCallResponse{}, ctx.Err()
	}
}

func testConfig() *config.Config {
	return &config.Config{Tools: config.ToolsConfig{Interactive: config.InteractiveToolConfig{
		FastReadTimeout: testFastTimeout, LongRunningTimeout: testLongTimeout, AcknowledgementThreshold: testAckTimeout,
	}}}
}

func boundCapabilities(t *testing.T, binding Binding) *runtimeSession.LiveCapabilities {
	t.Helper()
	capabilities := &runtimeSession.LiveCapabilities{
		Executor:    contextBoundExecutor{},
		Definitions: []messages.ToolDefinition{{Name: fastToolName}, {Name: longToolName}},
	}
	if err := Bind(capabilities, binding); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	return capabilities
}

func execute(t *testing.T, executor messages.ToolExecutor, name string) messages.ToolCallResponse {
	t.Helper()
	response, err := executor.Execute(context.Background(), messages.ToolCall{ID: "call-" + name, Name: name})
	if err != nil {
		t.Fatalf("Execute(%s) returned error %v; tool failures must be correlated results", name, err)
	}
	if response.ToolCallID != "call-"+name || response.Name != name {
		t.Fatalf("Execute(%s) response identity = %q/%q", name, response.ToolCallID, response.Name)
	}
	return response
}

func TestBindAppliesConfiguredClassBounds(t *testing.T) {
	executor := boundCapabilities(t, Binding{Config: testConfig()}).Executor
	if got := execute(t, executor, fastToolName).Content; !strings.Contains(got, timedOutText) {
		t.Fatalf("fast/read result = %q, want the %s fast bound to end it", got, testFastTimeout)
	}
	if got := execute(t, executor, longToolName).Content; got != "done" {
		t.Fatalf("long-running result = %q, want completion under the long bound", got)
	}
}

func TestBindExplicitTimeoutOverridesPolicy(t *testing.T) {
	executor := boundCapabilities(t, Binding{Config: testConfig(), Timeout: testLongTimeout}).Executor
	if got := execute(t, executor, fastToolName).Content; got != "done" {
		t.Fatalf("fast/read result with explicit timeout = %q, want completion", got)
	}
}

func TestBindDefaultsWithoutConfig(t *testing.T) {
	executor := boundCapabilities(t, Binding{}).Executor
	if got := execute(t, executor, fastToolName).Content; got != "done" {
		t.Fatalf("fast/read result under the default 5s bound = %q, want completion", got)
	}
}

func TestResolvePolicyClassifiesBrowserAndDynamicTools(t *testing.T) {
	capabilities := &runtimeSession.LiveCapabilities{Definitions: []messages.ToolDefinition{{Name: fastToolName}, {Name: browserToolName}}}
	dynamic, err := ResolvePolicy(newTurnService(), Binding{Config: testConfig(), BrowserToolsEnabled: true}, capabilities)
	if err != nil {
		t.Fatalf("ResolvePolicy: %v", err)
	}
	for name, want := range map[string]runtimeTools.InteractiveToolClass{
		fastToolName: runtimeTools.InteractiveToolClassFastRead, longToolName: runtimeTools.InteractiveToolClassBoundedLongRunning,
		browserToolName: runtimeTools.InteractiveToolClassBoundedLongRunning, pageToolName: runtimeTools.InteractiveToolClassBoundedLongRunning,
	} {
		if got := dynamic.ClassForTool(name); got != want {
			t.Fatalf("browser-enabled class for %q = %q, want %q", name, got, want)
		}
	}
	static, err := ResolvePolicy(newTurnService(), Binding{Config: testConfig()}, capabilities)
	if err != nil {
		t.Fatalf("ResolvePolicy: %v", err)
	}
	if got := static.ClassForTool(pageToolName); got != runtimeTools.InteractiveToolClassFastRead {
		t.Fatalf("unknown tool class without browser tools = %q, want fast/read", got)
	}
	if got := static.TimeoutForTool(pageToolName); got != testFastTimeout {
		t.Fatalf("unknown tool timeout = %s, want %s", got, testFastTimeout)
	}
}

func TestBindRejectsInvalidConfigAndSkipsAbsentExecutor(t *testing.T) {
	invalid := testConfig()
	invalid.Tools.Interactive.FastReadTimeout = time.Minute
	capabilities := &runtimeSession.LiveCapabilities{Executor: contextBoundExecutor{}}
	if err := Bind(capabilities, Binding{Config: invalid}); err == nil || !strings.Contains(err.Error(), "fast_read_timeout") {
		t.Fatalf("invalid config error = %v, want fast_read_timeout validation", err)
	}
	if err := Bind(nil, Binding{}); err != nil {
		t.Fatalf("Bind(nil): %v", err)
	}
	empty := &runtimeSession.LiveCapabilities{}
	if err := Bind(empty, Binding{Config: invalid}); err != nil || empty.Executor != nil {
		t.Fatalf("Bind without executor = %v / %T, want an untouched capability", err, empty.Executor)
	}
}

func TestPresentationProjectsHostFailures(t *testing.T) {
	presentation := Presentation()
	if !presentation.DisplayTool("show") || presentation.DisplayTool(fastToolName) {
		t.Fatal("display tool classification does not follow the CLI display contract")
	}
	if failure := presentation.PageSightFailure(); !strings.Contains(failure, "page_sight_unavailable") {
		t.Fatalf("page sight failure = %q", failure)
	}
	if presentation.FailedContent("plain result") {
		t.Fatal("plain content classified as a filesystem refusal")
	}
	if err := presentation.DisplayPermissionDenied(runtimeTools.DisplayPermission{Reason: "denied"}); err == nil {
		t.Fatal("denied display permission produced no error")
	}
}

type unadvertisedReplacement struct{ contextBoundExecutor }

func (unadvertisedReplacement) AllowUnadvertisedTools() bool { return true }

func TestBindPreservesUnadvertisedReplacementMarker(t *testing.T) {
	capabilities := &runtimeSession.LiveCapabilities{Executor: unadvertisedReplacement{}, Definitions: []messages.ToolDefinition{{Name: longToolName}}}
	if err := Bind(capabilities, Binding{Config: testConfig()}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if !allowsUnadvertisedTools(capabilities.Executor) {
		t.Fatalf("bound executor %T dropped the replacement's unadvertised-tools marker", capabilities.Executor)
	}
	if got := execute(t, capabilities.Executor, longToolName).Content; got != "done" {
		t.Fatalf("marked executor result = %q, want the bounded call to complete", got)
	}
}
