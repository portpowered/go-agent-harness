package execution

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/filesystem"
)

type testExecutor struct {
	fn          func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error)
	page        bool
	supported   bool
	permission  public.DisplayPermission
	recheckErr  error
	recheckWait bool
	calls       atomic.Int32
	exited      atomic.Int32
}

func (e *testExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.calls.Add(1)
	if e.fn != nil {
		return e.fn(ctx, call)
	}
	<-ctx.Done()
	e.exited.Add(1)
	return messages.ToolCallResponse{}, ctx.Err()
}

func (e *testExecutor) IsPageSightTool(string) bool { return e.page }

func (e *testExecutor) ScreenRecordingPermissionRecheckSupported() bool { return e.supported }

func (e *testExecutor) RecheckScreenRecordingPermission(ctx context.Context) (public.DisplayPermission, error) {
	if e.recheckWait {
		<-ctx.Done()
		return public.DisplayPermission{}, ctx.Err()
	}
	return e.permission, e.recheckErr
}

type recordingObserver struct {
	calls   []messages.ToolCall
	results []messages.ToolCallResponse
	failed  []bool
	panic   bool
}

func (o *recordingObserver) ObserveToolCall(call messages.ToolCall) {
	if o.panic {
		panic("observer call")
	}
	o.calls = append(o.calls, call)
}

func (o *recordingObserver) ObserveToolResult(_ messages.ToolCall, response messages.ToolCallResponse, failed bool) {
	if o.panic {
		panic("observer result")
	}
	o.results = append(o.results, response)
	o.failed = append(o.failed, failed)
}

type recordingDiagnosticSink struct {
	values []public.ToolExecutionDiagnostic
}

func (s *recordingDiagnosticSink) RecordToolExecutionDiagnostic(value public.ToolExecutionDiagnostic) {
	s.values = append(s.values, value)
}

type cancellationIntent bool

func (i cancellationIntent) SIGINTReceived() bool { return bool(i) }

type panicIntent struct{}

func (panicIntent) SIGINTReceived() bool { panic("intent") }

type timeoutPolicy struct {
	timeout time.Duration
	panic   bool
}

func (p timeoutPolicy) Settings() public.InteractiveToolPolicySettings {
	return public.InteractiveToolPolicySettings{}
}
func (p timeoutPolicy) ClassForTool(string) public.InteractiveToolClass {
	return public.InteractiveToolClassFastRead
}
func (p timeoutPolicy) TimeoutForTool(string) time.Duration { return p.timeout }
func (p timeoutPolicy) Clone() public.InteractiveToolPolicy {
	if p.panic {
		panic("policy clone")
	}
	return p
}
func (p timeoutPolicy) Validate() error { return nil }

type panicRechecker struct{}

func (panicRechecker) ScreenRecordingPermissionRecheckSupported() bool { panic("support") }
func (panicRechecker) RecheckScreenRecordingPermission(context.Context) (public.DisplayPermission, error) {
	panic("recheck")
}

func newControllerForTest(executor *testExecutor, timeout time.Duration, observer public.ToolExecutionObserver, diagnostics public.ToolExecutionDiagnosticSink) public.ToolExecutionController {
	return newController(public.ToolExecutionRequest{
		Executor: executor, TimeoutOverride: timeout, Observer: observer, Diagnostics: diagnostics,
		IsPhysicalDisplayTool: func(name string) bool { return name == public.ScreenToolID },
		ScreenErrorCode:       func(error) string { return "screen_test_failure" },
	})
}

func TestControllerSuccessCorrelatesAndOwnsContent(t *testing.T) {
	original := []byte{1, 2, 3}
	executor := &testExecutor{fn: func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
		return messages.ToolCallResponse{ToolCallID: "wrong", Name: "wrong", ContentParts: []messages.ContentPart{messages.ImagePart{Bytes: original}}}, nil
	}}
	observer := &recordingObserver{}
	controller := newControllerForTest(executor, time.Second, observer, nil)
	response, err := controller.Execute(context.Background(), messages.ToolCall{ID: "call-1", Name: "show", Arguments: `{}`})
	if err != nil || response.ToolCallID != "call-1" || response.Name != "show" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	part, ok := response.ContentParts[0].(messages.ImagePart)
	if !ok {
		t.Fatalf("response content part = %T, want messages.ImagePart", response.ContentParts[0])
	}
	part.Bytes[0] = 9
	observed, ok := observer.results[0].ContentParts[0].(messages.ImagePart)
	if !ok || original[0] != 1 || observed.Bytes[0] != 1 {
		t.Fatal("result content was aliased to executor or observer storage")
	}
	if len(observer.calls) != 1 || len(observer.results) != 1 || observer.failed[0] {
		t.Fatalf("observer calls=%d results=%d failed=%v", len(observer.calls), len(observer.results), observer.failed)
	}
}

func TestControllerFailuresAndContinuation(t *testing.T) {
	failure := errors.New("executor failed")
	diagnostics := &recordingDiagnosticSink{}
	executor := &testExecutor{fn: func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
		return messages.ToolCallResponse{}, failure
	}}
	observer := &recordingObserver{}
	controller := newControllerForTest(executor, time.Second, observer, diagnostics)
	response, err := controller.Execute(context.Background(), messages.ToolCall{ID: "failed", Name: "lookup"})
	if err != nil || !strings.Contains(response.Content, `tool "lookup" failed: executor failed`) {
		t.Fatalf("failure response=%q err=%v", response.Content, err)
	}
	if len(diagnostics.values) != 1 || diagnostics.values[0].Kind != public.ToolExecutionFailureGeneric || !errors.Is(diagnostics.values[0].Error, failure) {
		t.Fatalf("diagnostics=%+v", diagnostics.values)
	}
	if len(observer.results) != 1 || !observer.failed[0] {
		t.Fatalf("observer results=%d failed=%v", len(observer.results), observer.failed)
	}
	executor.fn = func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
		return messages.ToolCallResponse{Content: "continued"}, nil
	}
	response, err = controller.Execute(context.Background(), messages.ToolCall{ID: "next", Name: "lookup"})
	if err != nil || response.Content != "continued" || response.ToolCallID != "next" || executor.calls.Load() != 2 {
		t.Fatalf("continuation=%+v err=%v calls=%d", response, err, executor.calls.Load())
	}
}

func TestControllerPanicAndStructuredFailure(t *testing.T) {
	observer := &recordingObserver{}
	diagnostics := &recordingDiagnosticSink{}
	executor := &testExecutor{fn: func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) { panic("boom") }}
	controller := newControllerForTest(executor, time.Second, observer, diagnostics)
	response, err := controller.Execute(context.Background(), messages.ToolCall{ID: "panic", Name: "panic_tool"})
	if err != nil || !strings.Contains(response.Content, "tool executor panicked") || len(diagnostics.values) != 1 {
		t.Fatalf("panic response=%+v err=%v diagnostics=%+v", response, err, diagnostics.values)
	}
	refusal := filesystem.FilesystemRefusal{Type: filesystem.FilesystemRefusalType, Version: filesystem.FilesystemRefusalVersion, Status: filesystem.FilesystemRefusalStatus, Operation: "read_image", Path: "image.png", WorkDir: "/workspace", Reason: filesystem.FilesystemRefusalOutsidePermittedRoots, Message: "denied", Remediation: "retry"}
	encoded, marshalErr := filesystem.MarshalFilesystemRefusal(refusal)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	executor.fn = func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
		return messages.ToolCallResponse{Content: string(encoded)}, nil
	}
	response, err = controller.Execute(context.Background(), messages.ToolCall{ID: "refusal", Name: "read_image"})
	if err != nil || response.Content != string(encoded) || !observer.failed[len(observer.failed)-1] {
		t.Fatalf("refusal response=%+v err=%v failed=%v", response, err, observer.failed)
	}
	executor.fn = func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
		return messages.ToolCallResponse{Content: `{"version":"webmcp.tool-result.v1","ok":false}`}, nil
	}
	response, err = controller.Execute(context.Background(), messages.ToolCall{ID: "webmcp", Name: "remote"})
	if err != nil || response.Content == "" || !observer.failed[len(observer.failed)-1] {
		t.Fatalf("webmcp response=%+v err=%v failed=%v", response, err, observer.failed)
	}
}

func TestControllerTimeoutParentCancelAndSIGINT(t *testing.T) {
	executor := &testExecutor{}
	observer := &recordingObserver{}
	controller := newControllerForTest(executor, 15*time.Millisecond, observer, nil)
	started := time.Now()
	response, err := controller.Execute(context.Background(), messages.ToolCall{ID: "timeout", Name: "slow"})
	if err != nil || !strings.Contains(response.Content, "classification="+public.ToolExecutionTimeoutClassification) || time.Since(started) > time.Second {
		t.Fatalf("timeout response=%q err=%v", response.Content, err)
	}
	deadline := time.Now().Add(time.Second)
	for executor.exited.Load() != 1 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if executor.exited.Load() != 1 || len(observer.results) != 1 || !observer.failed[0] {
		t.Fatalf("worker exit=%d observer=%+v", executor.exited.Load(), observer)
	}

	parent, cancel := context.WithCancel(context.Background())
	executor = &testExecutor{}
	controller = newControllerForTest(executor, time.Second, nil, nil)
	cancel()
	response, err = controller.Execute(parent, messages.ToolCall{ID: "parent", Name: "slow"})
	if err != nil || !strings.Contains(response.Content, "tool execution canceled") || strings.Contains(response.Content, "classification=") {
		t.Fatalf("parent cancellation response=%q err=%v", response.Content, err)
	}

	parent, cancel = context.WithCancel(context.Background())
	executor = &testExecutor{}
	observer = &recordingObserver{}
	controller = newController(public.ToolExecutionRequest{Executor: executor, TimeoutOverride: time.Second, Observer: observer, CancellationIntent: cancellationIntent(true)})
	cancel()
	response, err = controller.Execute(parent, messages.ToolCall{ID: "sigint", Name: "slow"})
	if !errors.Is(err, context.Canceled) || response.ToolCallID != "sigint" || response.Content != "" || len(observer.results) != 0 {
		t.Fatalf("SIGINT response=%+v err=%v observer=%+v", response, err, observer)
	}
}

func TestControllerInteractivePolicyAndNilDependencies(t *testing.T) {
	deadlineSeen := make(chan time.Duration, 1)
	executor := &testExecutor{fn: func(ctx context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			return messages.ToolCallResponse{}, errors.New("deadline missing")
		}
		deadlineSeen <- time.Until(deadline)
		return messages.ToolCallResponse{Content: "ok"}, nil
	}}
	runner := newController(public.ToolExecutionRequest{Executor: executor, UseDefaultInteractivePolicy: true})
	if _, err := runner.Execute(context.Background(), messages.ToolCall{ID: "default", Name: "read"}); err != nil {
		t.Fatal(err)
	}
	if got := <-deadlineSeen; got <= 0 || got > public.DefaultInteractiveFastReadTimeout {
		t.Fatalf("default interactive deadline=%s", got)
	}
	executor.fn = func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
		return messages.ToolCallResponse{Content: "policy"}, nil
	}
	runner = newController(public.ToolExecutionRequest{Executor: executor, InteractivePolicy: timeoutPolicy{timeout: 25 * time.Millisecond}})
	response, err := runner.Execute(context.Background(), messages.ToolCall{ID: "policy", Name: "read"})
	if err != nil || response.Content != "policy" {
		t.Fatalf("policy response=%+v err=%v", response, err)
	}
	runner = newController(public.ToolExecutionRequest{Executor: executor, InteractivePolicy: timeoutPolicy{panic: true}, TimeoutForTool: func(string) time.Duration { return time.Second }})
	if _, err := runner.Execute(context.Background(), messages.ToolCall{ID: "callback", Name: "read"}); err != nil {
		t.Fatal(err)
	}
	var nilController *controller
	response, err = nilController.Execute(context.Background(), messages.ToolCall{ID: "nil", Name: "lookup"})
	if err != nil || response.ToolCallID != "nil" || !strings.Contains(response.Content, "controller is not configured") {
		t.Fatalf("nil controller response=%+v err=%v", response, err)
	}
	response, err = newController(public.ToolExecutionRequest{}).Execute(context.Background(), messages.ToolCall{ID: "empty", Name: "lookup"})
	if err != nil || !strings.Contains(response.Content, "controller is not configured") {
		t.Fatalf("nil executor response=%+v err=%v", response, err)
	}
	//lint:ignore SA1012 Exercise the controller's nil-context compatibility fallback.
	if _, err := newController(public.ToolExecutionRequest{Executor: executor}).Execute(nil, messages.ToolCall{ID: "nil-context", Name: "lookup"}); err != nil {
		t.Fatal(err)
	}
}

func TestControllerPageAndPhysicalFailureRoutes(t *testing.T) {
	diagnostics := &recordingDiagnosticSink{}
	executor := &testExecutor{page: true, fn: func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
		return messages.ToolCallResponse{}, errors.New("page unavailable")
	}}
	controller := newController(public.ToolExecutionRequest{Executor: executor, Diagnostics: diagnostics, IsPhysicalDisplayTool: func(string) bool { return true }, ScreenErrorCode: func(error) string { return "physical_code" }})
	response, err := controller.Execute(context.Background(), messages.ToolCall{ID: "page", Name: "show_page"})
	if err != nil || !strings.Contains(response.Content, `"source":"browser_page"`) || !strings.Contains(response.Content, public.PageSightUnavailableErrorCode) {
		t.Fatalf("page response=%q err=%v", response.Content, err)
	}
	if diagnostics.values[0].Kind != public.ToolExecutionFailurePageSight {
		t.Fatalf("page diagnostic=%+v", diagnostics.values[0])
	}
	executor.page = false
	response, err = controller.Execute(context.Background(), messages.ToolCall{ID: "screen", Name: "show"})
	if err != nil || !strings.Contains(response.Content, `"error_code":"physical_code"`) || diagnostics.values[1].Kind != public.ToolExecutionFailurePhysicalDisplay {
		t.Fatalf("screen response=%q diagnostics=%+v err=%v", response.Content, diagnostics.values, err)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(response.Content), &envelope); err != nil || envelope["error"] != "Screen sight is unavailable." {
		t.Fatalf("screen envelope=%s err=%v", response.Content, err)
	}
}

func TestControllerPermissionRecheckOutcomes(t *testing.T) {
	newPhysical := func(executor *testExecutor, parent context.Context) (messages.ToolCallResponse, error) {
		controller := newController(public.ToolExecutionRequest{
			Executor: executor, TimeoutOverride: 10 * time.Millisecond,
			IsPhysicalDisplayTool: func(string) bool { return true },
			ScreenErrorCode:       func(error) string { return "permission_code" },
			PermissionDeniedError: func(reason string) error {
				return errors.Join(public.ErrToolExecutionPermissionDenied, errors.New(reason))
			},
		})
		return controller.Execute(parent, messages.ToolCall{ID: "screen", Name: "show"})
	}
	denied := &testExecutor{supported: true, permission: public.DisplayPermission{State: public.DisplayPermissionDenied, Reason: "TCC"}}
	response, err := newPhysical(denied, context.Background())
	if err != nil || !strings.Contains(response.Content, `"error_code":"permission_code"`) {
		t.Fatalf("denied response=%q err=%v", response.Content, err)
	}
	for _, candidate := range []*testExecutor{
		{supported: false},
		{supported: true, permission: public.DisplayPermission{State: public.DisplayPermissionGranted}},
		{supported: true, recheckErr: errors.New("checker failed")},
		{supported: true, recheckWait: true},
	} {
		response, err = newPhysical(candidate, context.Background())
		if err != nil || !strings.Contains(response.Content, `"error_code":"permission_code"`) {
			t.Fatalf("non-denial response=%q err=%v", response.Content, err)
		}
	}
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	response, err = newPhysical(&testExecutor{supported: true, permission: public.DisplayPermission{State: public.DisplayPermissionDenied}}, parent)
	if err != nil || !strings.Contains(response.Content, `"error_code":"permission_code"`) {
		t.Fatalf("parent canceled response=%q err=%v", response.Content, err)
	}
}

func TestPolicySafetyHelpers(t *testing.T) {
	if clonePolicy(nil) != nil || safePolicyTimeout(nil, "x") != 0 || safeTimeoutPolicy(nil, "x") != 0 || safeSIGINT(nil) {
		t.Fatal("nil safety helpers returned an unexpected value")
	}
	if clonePolicy(timeoutPolicy{timeout: time.Second}) == nil || safePolicyTimeout(timeoutPolicy{timeout: time.Second}, "x") != time.Second || safeTimeoutPolicy(func(string) time.Duration { return time.Second }, "x") != time.Second {
		t.Fatal("normal policy safety helper failed")
	}
	if clonePolicy(timeoutPolicy{panic: true}) != nil || safePolicyTimeout(timeoutPolicy{panic: true}, "x") != 0 || safeTimeoutPolicy(func(string) time.Duration { panic("timeout") }, "x") != 0 || safeSIGINT(cancellationIntent(false)) || safeSIGINT(panicIntent{}) {
		t.Fatal("panic or false safety helper failed")
	}
}

func TestRouteSafetyHelpers(t *testing.T) {
	if !safeSIGINT(cancellationIntent(true)) || !safePhysicalMatch(func(string) bool { return true }, "x") || safePhysicalMatch(nil, "x") || safePhysicalMatch(func(string) bool { panic("match") }, "x") {
		t.Fatal("route safety helper failed")
	}
	page := &testExecutor{page: true}
	if !isPageSightTool(page, "x") || isPageSightTool(nil, "x") || isPageSightTool(&panicRouter{}, "x") {
		t.Fatal("page safety helper failed")
	}
	if safeScreenErrorCode(func(error) string { return "code" }, nil) != "code" || safeScreenErrorCode(nil, nil) != "" || safeScreenErrorCode(func(error) string { panic("code") }, nil) != "" {
		t.Fatal("screen code safety helper failed")
	}
}

func TestPermissionAndCloningHelpers(t *testing.T) {
	if safePermissionError(func(string) error { return errors.New("typed") }, "x") == nil || safePermissionError(nil, "x") != nil || safePermissionError(func(string) error { panic("permission") }, "x") != nil {
		t.Fatal("permission safety helper failed")
	}
	if !errors.Is(permissionError(nil, "why"), public.ErrToolExecutionPermissionDenied) || !errors.Is(permissionError(nil, ""), public.ErrToolExecutionPermissionDenied) {
		t.Fatal("permission error identity was not preserved")
	}
	if safeRecheckSupported(panicRechecker{}) {
		t.Fatal("panicking permission support check was not contained")
	}
	if _, err := invokeRecheck(context.Background(), panicRechecker{}); err == nil {
		t.Fatal("panicking permission recheck was not contained")
	}

	parts := []messages.ContentPart{
		messages.TextPart{Text: "text"}, messages.ImagePart{Bytes: []byte{1}}, messages.AudioPart{Bytes: []byte{2}},
		messages.VideoPart{Bytes: []byte{3}}, messages.FilePart{Bytes: []byte{4}}, messages.EmbeddingPart{Bytes: []byte{5}},
	}
	cloned := cloneContentParts(parts)
	if len(cloned) != len(parts) {
		t.Fatalf("cloned parts=%d", len(cloned))
	}
	if cloneContentParts(nil) != nil || cloneToolCallResponse(messages.ToolCallResponse{}).ContentParts != nil {
		t.Fatal("nil content parts were not preserved")
	}
	observer := &recordingObserver{panic: true}
	controller := newController(public.ToolExecutionRequest{Executor: &testExecutor{fn: func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
		return messages.ToolCallResponse{Content: "ok"}, nil
	}}, Observer: observer})
	if _, err := controller.Execute(context.Background(), messages.ToolCall{ID: "observer", Name: "ok"}); err != nil {
		t.Fatal(err)
	}
}

func TestFactoryBuildsPublicController(t *testing.T) {
	executor := &testExecutor{fn: func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
		return messages.ToolCallResponse{Content: "factory"}, nil
	}}
	service := New()
	controller := service.NewToolExecutionController(public.ToolExecutionRequest{Executor: executor})
	response, err := controller.Execute(context.Background(), messages.ToolCall{ID: "factory", Name: "lookup"})
	if err != nil || response.Content != "factory" {
		t.Fatalf("factory response=%+v err=%v", response, err)
	}
}

type panicRouter struct{}

func (*panicRouter) IsPageSightTool(string) bool { panic("router") }

func TestRouteAndFailureHelpers(t *testing.T) {
	if isPageSightTool(&testExecutor{}, "x") || responseFailed("not json") || responseFailed(`{"version":"webmcp.tool-result.v1","ok":true}`) || responseFailed(`{"version":"webmcp.tool-result.v1"}`) {
		t.Fatal("negative response or route helper result was unexpected")
	}
	if !responseFailed(`{"version":"webmcp.tool-result.v1","ok":false}`) {
		t.Fatal("failed WebMCP envelope was not recognized")
	}
	if !strings.Contains(genericFailure(messages.ToolCall{Name: "x"}, public.ErrToolExecutionTimeout), public.ToolExecutionTimeoutClassification) {
		t.Fatal("timeout generic failure lost classification")
	}
	if !strings.Contains(string([]byte(pageFailure())), public.PageSightUnavailableErrorCode) || !strings.Contains(screenFailure(errors.New("screen"), nil), "capture_failed") {
		t.Fatal("failure projection helper lost stable envelope")
	}
	if !errors.Is(contextFailure(context.DeadlineExceeded), public.ErrToolExecutionTimeout) || contextFailure(context.Canceled).Error() != "tool execution canceled" || !strings.Contains(contextFailure(errors.New("other")).Error(), "tool execution stopped") {
		t.Fatal("context failure projection changed")
	}
	if _, err := invoke(context.Background(), &testExecutor{fn: func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
		return messages.ToolCallResponse{Content: "ok"}, nil
	}}, messages.ToolCall{}); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke(context.Background(), &testExecutor{fn: func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) { panic("invoke") }}, messages.ToolCall{}); err == nil {
		t.Fatal("invoke did not contain panic")
	}
}
