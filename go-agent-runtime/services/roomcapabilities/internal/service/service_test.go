package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	roomcapabilities "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomcapabilities"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

type internalExecutor struct{ label string }

func (e internalExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: e.label + ":" + call.Name}, nil
}

func internalDefinitions(names ...string) []roomcapabilities.ToolDefinition {
	definitions := make([]roomcapabilities.ToolDefinition, 0, len(names))
	for _, name := range names {
		definitions = append(definitions, roomcapabilities.ToolDefinition{Name: name})
	}
	return definitions
}

func TestValidationNegativeCasesAndValidControl(t *testing.T) {
	service := New()
	assertStaticControl(t, service)
	assertValidationErrors(t, service)
	if err := service.ValidateTools(roomcapabilities.Participant{ID: "empty"}, roomcapabilities.ToolCapabilities{Definitions: internalDefinitions("ignored")}); err != nil {
		t.Fatal(err)
	}
	ordered := service.OrderDefinitions(internalDefinitions("beta", "alpha"), []string{"beta", "alpha"})
	if got := []string{ordered[0].Name, ordered[1].Name}; !reflect.DeepEqual(got, []string{"alpha", "beta"}) {
		t.Fatalf("ordered definitions = %v", got)
	}
}

func assertStaticControl(t *testing.T, service *Service) {
	t.Helper()
	capability, err := service.Compose(context.Background(), roomcapabilities.Participant{ID: "internal", Tools: []string{"beta", "alpha"}}, roomcapabilities.ToolCapabilities{
		Executor: internalExecutor{label: "static"}, Definitions: internalDefinitions("beta", "alpha"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{capability.Definitions[0].Name, capability.Definitions[1].Name}; !reflect.DeepEqual(got, []string{"alpha", "beta"}) {
		t.Fatalf("definitions = %v", got)
	}
	if capability.Executor == nil || capability.ToolDefinitionBase == nil {
		t.Fatalf("static capability is incomplete: %#v", capability)
	}
}

func assertValidationErrors(t *testing.T, service *Service) {
	t.Helper()
	requireValidationError(t, service, roomcapabilities.Participant{ID: "missing", Tools: []string{"alpha", "beta"}}, roomcapabilities.ToolCapabilities{Executor: internalExecutor{}, Definitions: internalDefinitions("alpha")}, roomcapabilities.ErrMissingTool)
	requireValidationError(t, service, roomcapabilities.Participant{ID: "unrequested", Tools: []string{"alpha"}}, roomcapabilities.ToolCapabilities{Executor: internalExecutor{}, Definitions: internalDefinitions("other")}, roomcapabilities.ErrUnrequestedTool)
	requireValidationError(t, service, roomcapabilities.Participant{ID: "duplicate-request", Tools: []string{"alpha", "alpha"}}, roomcapabilities.ToolCapabilities{Executor: internalExecutor{}, Definitions: internalDefinitions("alpha")}, roomcapabilities.ErrDuplicateTool)
	requireValidationError(t, service, roomcapabilities.Participant{ID: "duplicate-return", Tools: []string{"alpha"}}, roomcapabilities.ToolCapabilities{Executor: internalExecutor{}, Definitions: internalDefinitions("alpha", "alpha")}, roomcapabilities.ErrDuplicateTool)
	requireValidationError(t, service, roomcapabilities.Participant{ID: "empty-request", Tools: []string{" "}}, roomcapabilities.ToolCapabilities{Executor: internalExecutor{}}, roomcapabilities.ErrEmptyToolName)
	requireValidationError(t, service, roomcapabilities.Participant{ID: "empty-return", Tools: []string{"alpha"}}, roomcapabilities.ToolCapabilities{Executor: internalExecutor{}, Definitions: internalDefinitions("")}, roomcapabilities.ErrEmptyToolName)
	var typedNil *internalExecutor
	requireUnavailableError(t, service, roomcapabilities.Participant{ID: "nil-executor", Tools: []string{"alpha"}}, roomcapabilities.ToolCapabilities{Executor: typedNil, Definitions: internalDefinitions("alpha")})
}

func requireUnavailableError(t *testing.T, service *Service, participant roomcapabilities.Participant, capability roomcapabilities.ToolCapabilities) {
	t.Helper()
	err := service.ValidateTools(participant, capability)
	if err == nil || !errors.Is(err, roomcapabilities.ErrToolsUnavailable) || !errors.Is(err, roomcapabilities.ErrNilToolExecutor) {
		t.Fatalf("unavailable validation error = %v", err)
	}
}

func requireValidationError(t *testing.T, service *Service, participant roomcapabilities.Participant, capability roomcapabilities.ToolCapabilities, causes ...error) {
	t.Helper()
	err := service.ValidateTools(participant, capability)
	if err == nil {
		t.Fatal("validation unexpectedly passed")
	}
	for _, cause := range append([]error{roomcapabilities.ErrToolMismatch}, causes...) {
		if !errors.Is(err, cause) {
			t.Errorf("validation error %q does not contain %v", err, cause)
		}
	}
}

func TestBrowserCompositionBaseRefreshAndDispatch(t *testing.T) {
	service := New()
	refreshErr := errors.New("refresh failed")
	closeErr := errors.New("close failed")
	var refreshCalls atomic.Int32
	var initializeCalls atomic.Int32
	var closeCalls atomic.Int32
	mode := atomic.Int32{}
	capability, err := newBrowserTestCapability(service, &refreshCalls, &initializeCalls, &closeCalls, &mode, refreshErr, closeErr)
	if err != nil {
		t.Fatal(err)
	}
	requireInitializeTwice(t, capability)
	if initializeCalls.Load() != 1 {
		t.Fatalf("initialize calls = %d", initializeCalls.Load())
	}
	mode.Store(1)
	requireRefreshError(t, capability, refreshErr)
	mode.Store(2)
	requireRefreshedStatic(t, capability)
	requireCloseTwice(t, capability, closeErr)
	if refreshCalls.Load() != 2 || closeCalls.Load() != 1 {
		t.Fatalf("calls refresh=%d close=%d", refreshCalls.Load(), closeCalls.Load())
	}
}

func newBrowserTestCapability(service *Service, refreshCalls, initializeCalls, closeCalls, mode *atomic.Int32, refreshErr, closeErr error) (roomcapabilities.Capability, error) {
	return service.Compose(context.Background(), roomcapabilities.Participant{ID: "browser", Tools: []string{"static"}}, roomcapabilities.ToolCapabilities{
		Executor: internalExecutor{label: "static"}, Definitions: internalDefinitions("static"),
	}, &roomcapabilities.BrowserCapabilities{
		Executor:           internalExecutor{label: "browser"},
		Definitions:        internalDefinitions("page"),
		ToolDefinitionBase: internalDefinitions("stable"),
		RefreshToolDefinitions: func(context.Context) ([]roomcapabilities.ToolDefinition, error) {
			refreshCalls.Add(1)
			if mode.Load() == 1 {
				return nil, refreshErr
			}
			return internalDefinitions("page-refreshed"), nil
		},
		Initialize: func(context.Context) error { initializeCalls.Add(1); return nil },
		Close:      func() error { closeCalls.Add(1); return closeErr },
	})
}

func requireInitializeTwice(t *testing.T, capability roomcapabilities.Capability) {
	t.Helper()
	if err := capability.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := capability.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func requireRefreshError(t *testing.T, capability roomcapabilities.Capability, want error) {
	t.Helper()
	if _, err := capability.RefreshToolDefinitions(context.Background()); !errors.Is(err, want) {
		t.Fatalf("refresh error = %v", err)
	}
}

func requireRefreshedStatic(t *testing.T, capability roomcapabilities.Capability) {
	t.Helper()
	refreshed, err := capability.RefreshToolDefinitions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{refreshed[0].Name, refreshed[1].Name}; !reflect.DeepEqual(got, []string{"page-refreshed", "static"}) {
		t.Fatalf("refreshed definitions = %v", got)
	}
}

func requireCloseTwice(t *testing.T, capability roomcapabilities.Capability, want error) {
	t.Helper()
	if err := capability.Close(); !errors.Is(err, want) {
		t.Fatalf("first close = %v", err)
	}
	if err := capability.Close(); !errors.Is(err, want) {
		t.Fatalf("second close = %v", err)
	}
}

func TestServiceRejectsUnavailableAndHonorsContext(t *testing.T) {
	participant := roomcapabilities.Participant{ID: "unavailable", Tools: []string{"static"}}
	browser := &roomcapabilities.BrowserCapabilities{Executor: internalExecutor{}, Definitions: internalDefinitions("page")}
	var nilService *Service
	if _, err := nilService.Compose(context.Background(), participant, roomcapabilities.ToolCapabilities{Executor: internalExecutor{}, Definitions: internalDefinitions("static")}, browser); !errors.Is(err, roomcapabilities.ErrServiceUnavailable) {
		t.Fatalf("nil service error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New().Compose(canceled, participant, roomcapabilities.ToolCapabilities{Executor: internalExecutor{}, Definitions: internalDefinitions("static")}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled compose error = %v", err)
	}
	if _, err := New().Compose(nilContextForTest(), participant, roomcapabilities.ToolCapabilities{Executor: internalExecutor{}, Definitions: internalDefinitions("static")}, nil); err != nil {
		t.Fatal(err)
	}
	if err := New().ValidateBrowser(roomcapabilities.BrowserCapabilities{Executor: internalExecutor{}, Definitions: internalDefinitions(" ")}); !errors.Is(err, roomcapabilities.ErrEmptyToolName) || !strings.Contains(err.Error(), "browser") {
		t.Fatalf("empty browser error = %v", err)
	}
}

func nilContextForTest() context.Context { return nil }

func TestLifecycleAndRefreshingExecutorNilPaths(t *testing.T) {
	owner := newLifecycle(nil, nil, nil, 0)
	if err := owner.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if definitions, err := owner.RefreshDefinitions(context.Background()); err != nil || definitions != nil {
		t.Fatalf("nil refresh = %#v, %v", definitions, err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	var executor *refreshingExecutor
	if _, err := executor.Execute(context.Background(), roomcapabilities.ToolCall{Name: "missing"}); err == nil {
		t.Fatal("nil refreshing executor executed")
	}
	executor = newRefreshingExecutor(nil)
	if _, err := executor.Execute(context.Background(), roomcapabilities.ToolCall{Name: "missing"}); err == nil {
		t.Fatal("empty refreshing executor executed")
	}
	executor.Replace(internalExecutor{label: "replacement"})
	response, err := executor.Execute(context.Background(), roomcapabilities.ToolCall{Name: "tool"})
	if err != nil || response.Content != "replacement:tool" {
		t.Fatalf("replacement execute = %#v, %v", response, err)
	}
}

func TestCanonicalCloneAndParticipantIsolation(t *testing.T) {
	service := New()
	executor := internalExecutor{label: "clone"}
	source := []roomcapabilities.ToolDefinition{{
		Name: "zeta",
		Parameters: []roomcapabilities.ToolParameter{
			{Name: "z", Type: "string"},
			{Name: "a", Type: "number"},
		},
		ParameterSchema: []byte(`{"type":"object"}`),
	}, {Name: "alpha"}}
	capability, err := service.Compose(context.Background(), roomcapabilities.Participant{ID: "clone", Tools: []string{"zeta", "alpha"}}, roomcapabilities.ToolCapabilities{
		Executor: executor, Definitions: source,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{capability.Definitions[0].Name, capability.Definitions[1].Name}; !reflect.DeepEqual(got, []string{"alpha", "zeta"}) {
		t.Fatalf("canonical definitions = %v", got)
	}
	if capability.Definitions[1].Parameters[0].Name != "a" {
		t.Fatalf("parameters were not canonicalized: %#v", capability.Definitions[1].Parameters)
	}
	source[0].Parameters[0].Name = "mutated source"
	source[0].ParameterSchema[0] = 'x'
	if capability.Definitions[1].Parameters[1].Name != "z" || string(capability.Definitions[1].ParameterSchema) != `{"type":"object"}` {
		t.Fatalf("source mutation leaked into capability: %#v", capability.Definitions[1])
	}
	capability.Definitions[0].Description = "first only"
	other, err := service.Compose(context.Background(), roomcapabilities.Participant{ID: "other", Tools: []string{"alpha"}}, roomcapabilities.ToolCapabilities{
		Executor: internalExecutor{label: "other"}, Definitions: internalDefinitions("alpha"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if other.Definitions[0].Description == "first only" {
		t.Fatal("participant definition snapshots share state")
	}
	firstResult, err := capability.Executor.Execute(context.Background(), roomcapabilities.ToolCall{Name: "zeta"})
	if err != nil || firstResult.Content != "clone:zeta" {
		t.Fatalf("first participant dispatch = %#v, %v", firstResult, err)
	}
	otherResult, err := other.Executor.Execute(context.Background(), roomcapabilities.ToolCall{Name: "alpha"})
	if err != nil || otherResult.Content != "other:alpha" {
		t.Fatalf("second participant dispatch = %#v, %v", otherResult, err)
	}
}

type optionalExecutor struct {
	label   string
	dynamic bool
	support bool
	calls   atomic.Int32
	last    atomic.Value
}

func (e *optionalExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.calls.Add(1)
	e.last.Store(call)
	return messages.ToolCallResponse{
		ToolCallID: call.ID,
		Name:       call.Name,
		ContentParts: []messages.ContentPart{
			messages.TextPart{Text: e.label + ":" + call.Name},
		},
	}, nil
}

func (e *optionalExecutor) ResolvesDynamicTools() bool { return e.dynamic }

func (e *optionalExecutor) ScreenRecordingPermissionRecheckSupported() bool { return e.support }

func (e *optionalExecutor) RecheckScreenRecordingPermission(context.Context) (runtimeTools.DisplayPermission, error) {
	return runtimeTools.DisplayPermission{State: runtimeTools.DisplayPermissionGranted}, nil
}

func TestBrowserCompositionRoutesOptionalInterfacesAndRefreshesExecutor(t *testing.T) {
	service := New()
	static := &optionalExecutor{label: "static", support: true}
	browser := &optionalExecutor{label: "browser", dynamic: true}
	var refreshCalls atomic.Int32
	capability, err := newOptionalTestCapability(service, static, browser, &refreshCalls)
	if err != nil {
		t.Fatal(err)
	}
	requireOptionalInterfaces(t, capability)
	requireOptionalRoutes(t, capability, browser)
	requireOptionalRefresh(t, capability, &refreshCalls)
}

func newOptionalTestCapability(service *Service, static, browser *optionalExecutor, refreshCalls *atomic.Int32) (roomcapabilities.Capability, error) {
	return service.Compose(context.Background(), roomcapabilities.Participant{ID: "optional", Tools: []string{"show", "static"}}, roomcapabilities.ToolCapabilities{
		Executor:    static,
		Definitions: []roomcapabilities.ToolDefinition{{Name: "show"}, {Name: "static"}},
	}, &roomcapabilities.BrowserCapabilities{
		Executor:           browser,
		Definitions:        []roomcapabilities.ToolDefinition{{Name: runtimeTools.PageSightToolID}, {Name: "page"}},
		ToolDefinitionBase: []roomcapabilities.ToolDefinition{{Name: runtimeTools.PageSightToolID}},
		RefreshToolDefinitions: func(context.Context) ([]roomcapabilities.ToolDefinition, error) {
			refreshCalls.Add(1)
			return internalDefinitions("page-refreshed"), nil
		},
	})
}

func requireOptionalInterfaces(t *testing.T, capability roomcapabilities.Capability) {
	t.Helper()
	pageRouter, ok := capability.Executor.(runtimeTools.PageSightToolRouter)
	if !ok {
		t.Fatal("page-sight routing interface was not forwarded")
	}
	if !pageRouter.IsPageSightTool(runtimeTools.ScreenToolID) {
		t.Fatal("screen alias was not recognized")
	}
	if pageRouter.IsPageSightTool(runtimeTools.HostDisplayToolID) {
		t.Fatal("host display was incorrectly recognized as page sight")
	}
	permission, ok := capability.Executor.(runtimeTools.ScreenRecordingPermissionRechecker)
	if !ok {
		t.Fatal("screen permission interface was not forwarded")
	}
	if !permission.ScreenRecordingPermissionRecheckSupported() {
		t.Fatal("screen permission support was not forwarded")
	}
	if _, err := permission.RecheckScreenRecordingPermission(context.Background()); err != nil {
		t.Fatal(err)
	}
	dynamic, ok := capability.Executor.(runtimeTools.DynamicToolRouter)
	if !ok {
		t.Fatal("dynamic browser interface was not forwarded")
	}
	if !dynamic.ResolvesDynamicTools() {
		t.Fatal("dynamic browser support was not forwarded")
	}
}

func requireOptionalRoutes(t *testing.T, capability roomcapabilities.Capability, browser *optionalExecutor) {
	t.Helper()
	response, err := capability.Executor.Execute(context.Background(), roomcapabilities.ToolCall{ID: "page-alias", Name: runtimeTools.ScreenToolID, Arguments: `{"action":"screenshot"}`})
	if err != nil {
		t.Fatalf("page alias response error = %v", err)
	}
	if response.Content != "browser:show_page" {
		t.Fatalf("page alias response = %#v", response)
	}
	if len(response.ContentParts) != 0 {
		t.Fatalf("page alias content parts = %#v", response.ContentParts)
	}
	lastValue := browser.last.Load()
	lastCall, ok := lastValue.(messages.ToolCall)
	if !ok {
		t.Fatalf("page alias call has unexpected type: %T", lastValue)
	}
	if lastCall.Name != runtimeTools.PageSightToolID {
		t.Fatalf("page alias call name = %#v", lastCall)
	}
	if lastCall.Arguments != `{}` {
		t.Fatalf("page alias call arguments = %#v", lastCall)
	}
	dynamicResponse, err := capability.Executor.Execute(context.Background(), roomcapabilities.ToolCall{Name: "discovered"})
	if err != nil {
		t.Fatalf("dynamic response error = %v", err)
	}
	if dynamicResponse.Content != "browser:discovered" {
		t.Fatalf("dynamic response = %#v", dynamicResponse)
	}
}

func requireOptionalRefresh(t *testing.T, capability roomcapabilities.Capability, refreshCalls *atomic.Int32) {
	t.Helper()
	refreshed, err := capability.RefreshToolDefinitions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("refresh calls = %d", refreshCalls.Load())
	}
	if !reflect.DeepEqual([]string{refreshed[0].Name, refreshed[1].Name, refreshed[2].Name}, []string{"page-refreshed", "show", "static"}) {
		t.Fatalf("refresh = %v", refreshed)
	}
	if refreshedResponse, err := capability.Executor.Execute(context.Background(), roomcapabilities.ToolCall{Name: "page-refreshed"}); err != nil {
		t.Fatalf("refreshed route error = %v", err)
	} else if refreshedResponse.Content != "browser:page-refreshed" {
		t.Fatalf("refreshed route = %#v", refreshedResponse)
	}
}

func TestLifecycleConvertsPanicAndBoundsTimeout(t *testing.T) {
	panicOwner := newLifecycle(nil, nil, func() error { panic("cleanup panic") }, time.Second)
	if err := panicOwner.Close(); !errors.Is(err, roomcapabilities.ErrCapabilityClosePanic) {
		t.Fatalf("panic close = %v", err)
	}
	release := make(chan struct{})
	finished := make(chan struct{})
	timeoutOwner := newLifecycle(nil, nil, func() error {
		defer close(finished)
		<-release
		return nil
	}, time.Millisecond)
	if err := timeoutOwner.Close(); !errors.Is(err, roomcapabilities.ErrCapabilityCloseTimeout) {
		t.Fatalf("timeout close = %v", err)
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("timeout callback did not finish after release")
	}
}
