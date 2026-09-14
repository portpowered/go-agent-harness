package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/sight"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	sessionturnwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/wire"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

type sessionToolExecutorFunc func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error)

func (f sessionToolExecutorFunc) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	return f(ctx, call)
}

func newSessionToolExecutor(inner messages.ToolExecutor) messages.ToolExecutor {
	return newSessionToolExecutorWithTimeout(inner, 0)
}

func newSessionToolExecutorWithTimeout(inner messages.ToolExecutor, timeout time.Duration) messages.ToolExecutor {
	return newSessionToolExecutorWithInteractivePolicyAndObserverAndCancellationIntentAndDiagnostics(inner, nil, timeout, nil, nil, nil)
}

func newSessionToolExecutorWithInteractivePolicyAndObserverAndCancellationIntentAndDiagnostics(
	inner messages.ToolExecutor,
	policy runtimeTools.InteractiveToolPolicy,
	timeout time.Duration,
	observer sessionToolLifecycleObserver,
	_ *SessionCancellationIntent,
	diagnostics SessionToolDiagnosticSink,
) messages.ToolExecutor {
	request := sessionturn.Request{
		ToolExecutor:          inner,
		InteractiveToolPolicy: policy,
		ToolExecutionTimeout:  timeout,
	}
	if observer != nil {
		request.ToolCallObserver = observer.observeToolCall
		request.ToolResultObserver = observer.observeToolResult
	}
	if diagnostics != nil {
		request.ToolDiagnostic = func(call messages.ToolCall, err error) {
			recordSessionToolDiagnostic(diagnostics, inner, call, err)
		}
	}
	request.ToolFailurePresenter = testToolFailurePresenter(inner)
	runtime, err := sessionturnwire.NewDefaultService().Prepare(context.Background(), request)
	if err != nil {
		panic(fmt.Sprintf("prepare test session tool executor: %v", err))
	}
	return runtime.ToolExecutor()
}

func testToolFailurePresenter(inner messages.ToolExecutor) func(messages.ToolCall, error) messages.ToolCallResponse {
	return func(call messages.ToolCall, err error) messages.ToolCallResponse {
		if router, ok := inner.(runtimeTools.PageSightToolRouter); ok && router.IsPageSightTool(call.Name) {
			result := sight.NewError(sight.SourceBrowserPage, errors.New("browser-page sight unavailable"))
			result.Error = "Browser-page sight is unavailable."
			result.ErrorCode = "page_sight_unavailable"
			encoded, encodeErr := sight.Encode(result)
			if encodeErr == nil {
				return messages.ToolCallResponse{Content: string(encoded)}
			}
		}
		if cliTools.IsPhysicalDisplayToolName(call.Name) {
			return messages.ToolCallResponse{Content: cliTools.ScreenToolSessionErrorResult(err)}
		}
		return messages.ToolCallResponse{Content: fmt.Sprintf("tool %q failed: %v", call.Name, err)}
	}
}

var _ messages.ToolExecutor = sessionToolExecutorFunc(nil)
