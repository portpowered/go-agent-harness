package agentruntime

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/sight"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

const (
	defaultSessionToolExecutionTimeout   = runtimeTools.DefaultToolExecutionTimeout
	SessionToolTimeoutClassification     = runtimeTools.ToolExecutionTimeoutClassification
	SessionPageSightUnavailableErrorCode = runtimeTools.PageSightUnavailableErrorCode
)

type sessionToolLifecycleMux struct {
	recording sessionToolLifecycleObserver
	progress  *sessionProgressObserver
	runtime   *sessionRuntimeObservationRecorder
}

func (m sessionToolLifecycleMux) observeToolCall(call messages.ToolCall) {
	if m.runtime != nil {
		m.runtime.observeToolCall(call)
	}
	if m.progress != nil {
		m.progress.beginLocalToolExecution()
	}
	if m.recording != nil {
		m.recording.observeToolCall(call)
	}
}
func (m sessionToolLifecycleMux) observeToolResult(call messages.ToolCall, response messages.ToolCallResponse, failed bool) {
	if m.runtime != nil {
		m.runtime.observeToolResult(call, response, failed)
	}
	if m.recording != nil {
		m.recording.observeToolResult(call, response, failed)
	}
	if m.progress != nil {
		m.progress.endLocalToolExecution()
	}
}
func composeSessionToolLifecycleObserver(recording sessionToolLifecycleObserver, progress *sessionProgressObserver, runtime *sessionRuntimeObservationRecorder) sessionToolLifecycleObserver {
	if recording == nil && progress == nil && runtime == nil {
		return nil
	}
	return sessionToolLifecycleMux{recording: recording, progress: progress, runtime: runtime}
}

type sessionToolExecutor struct {
	controller runtimeTools.ToolExecutionController
}

var _ messages.ToolExecutor = (*sessionToolExecutor)(nil)

func newSessionToolExecutor(inner messages.ToolExecutor) *sessionToolExecutor {
	return newSessionToolExecutorWithTimeoutAndObserver(inner, 0, nil)
}
func newSessionToolExecutorWithTimeout(inner messages.ToolExecutor, timeout time.Duration) *sessionToolExecutor {
	return newSessionToolExecutorWithTimeoutAndObserver(inner, timeout, nil)
}
func newSessionToolExecutorWithTimeoutAndObserver(inner messages.ToolExecutor, timeout time.Duration, observer sessionToolLifecycleObserver) *sessionToolExecutor {
	return newSessionToolExecutorWithTimeoutAndObserverAndCancellationIntent(inner, timeout, observer, nil)
}
func newSessionToolExecutorWithTimeoutAndObserverAndCancellationIntent(inner messages.ToolExecutor, timeout time.Duration, observer sessionToolLifecycleObserver, cancellationIntent *SessionCancellationIntent) *sessionToolExecutor {
	return newSessionToolExecutorWithDependencies(inner, nil, timeout, false, observer, cancellationIntent, nil)
}
func newSessionToolExecutorWithInteractivePolicyAndObserverAndCancellationIntent(inner messages.ToolExecutor, policy *InteractiveToolPolicy, timeoutOverride time.Duration, observer sessionToolLifecycleObserver, cancellationIntent *SessionCancellationIntent) *sessionToolExecutor {
	return newSessionToolExecutorWithInteractivePolicyAndObserverAndCancellationIntentAndDiagnostics(inner, policy, timeoutOverride, observer, cancellationIntent, nil)
}
func newSessionToolExecutorWithInteractivePolicyAndObserverAndCancellationIntentAndDiagnostics(inner messages.ToolExecutor, policy *InteractiveToolPolicy, timeoutOverride time.Duration, observer sessionToolLifecycleObserver, cancellationIntent *SessionCancellationIntent, diagnostics SessionToolDiagnosticSink) *sessionToolExecutor {
	return newSessionToolExecutorWithDependencies(inner, policy, timeoutOverride, true, observer, cancellationIntent, diagnostics)
}
func newSessionToolExecutorWithDependencies(inner messages.ToolExecutor, policy *InteractiveToolPolicy, timeout time.Duration, interactive bool, observer sessionToolLifecycleObserver, cancellationIntent *SessionCancellationIntent, diagnostics SessionToolDiagnosticSink) *sessionToolExecutor {
	var timeoutPolicy runtimeTools.ToolExecutionTimeoutPolicy
	if policy != nil {
		snapshot := policy.Clone()
		timeoutPolicy = snapshot.TimeoutForTool
	}
	var runtimeObserver runtimeTools.ToolExecutionObserver
	if observer != nil {
		runtimeObserver = sessionToolObserver{value: observer}
	}
	var runtimeDiagnostics runtimeTools.ToolExecutionDiagnosticSink
	if diagnostics != nil {
		runtimeDiagnostics = sessionToolDiagnostics{sink: diagnostics}
	}
	permissionRechecker := newSessionToolRuntimePermissionRechecker(inner)
	executionService := runtimeToolsWire.NewExecutionService()
	return &sessionToolExecutor{controller: executionService.NewToolExecutionController(runtimeTools.ToolExecutionRequest{
		Executor: inner, TimeoutOverride: timeout, TimeoutForTool: timeoutPolicy,
		UseDefaultInteractivePolicy: interactive, Observer: runtimeObserver,
		CancellationIntent: cancellationIntent, Diagnostics: runtimeDiagnostics, PermissionRechecker: permissionRechecker,
		IsPhysicalDisplayTool: cliTools.IsPhysicalDisplayToolName, ScreenErrorCode: cliTools.ScreenToolErrorCode,
		PermissionDeniedError: sessionPermissionDeniedError,
	})}
}
func (e *sessionToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if e == nil {
		return newSessionToolExecutor(nil).Execute(ctx, call)
	}
	return e.controller.Execute(ctx, call)
}

type sessionToolObserver struct{ value sessionToolLifecycleObserver }

func (o sessionToolObserver) ObserveToolCall(call messages.ToolCall) { o.value.observeToolCall(call) }
func (o sessionToolObserver) ObserveToolResult(call messages.ToolCall, response messages.ToolCallResponse, failed bool) {
	o.value.observeToolResult(call, response, failed)
}

type sessionToolDiagnostics struct{ sink SessionToolDiagnosticSink }

func (d sessionToolDiagnostics) RecordToolExecutionDiagnostic(value runtimeTools.ToolExecutionDiagnostic) {
	diagnostic := SessionToolDiagnostic{ToolCallID: value.ToolCallID, ToolName: value.ToolName, Error: value.Error}
	switch value.Kind {
	case runtimeTools.ToolExecutionFailureGeneric:
		// Generic failures intentionally carry no source-specific projection.
	case runtimeTools.ToolExecutionFailurePageSight:
		diagnostic.Source, diagnostic.ErrorCode = sight.SourceBrowserPage, SessionPageSightUnavailableErrorCode
	case runtimeTools.ToolExecutionFailurePhysicalDisplay:
		diagnostic.Source, diagnostic.ErrorCode = sight.SourceScreen, cliTools.ScreenToolErrorCode(value.Error)
	}
	d.sink.RecordSessionToolDiagnostic(diagnostic)
}
func sessionPermissionDeniedError(reason string) error {
	return &cliTools.ScreenCaptureError{State: cliTools.ScreenCaptureDenied, Operation: "show permission re-check", Reason: reason}
}

type sessionToolRuntimePermissionRechecker struct {
	value cliTools.ScreenRecordingPermissionRechecker
}

func (r sessionToolRuntimePermissionRechecker) ScreenRecordingPermissionRecheckSupported() bool {
	return safeScreenPermissionRecheckSupported(r.value)
}
func (r sessionToolRuntimePermissionRechecker) RecheckScreenRecordingPermission(ctx context.Context) (runtimeTools.DisplayPermission, error) {
	permission, err := invokeScreenPermissionRecheck(ctx, r.value)
	return runtimeTools.DisplayPermission{State: runtimeTools.DisplayPermissionState(permission.State), Reason: permission.Reason}, err
}
func newSessionToolRuntimePermissionRechecker(executor messages.ToolExecutor) runtimeTools.ScreenRecordingPermissionRechecker {
	rechecker, _ := sessionScreenPermissionRechecker(executor)
	return sessionToolRuntimePermissionRechecker{value: rechecker}
}
