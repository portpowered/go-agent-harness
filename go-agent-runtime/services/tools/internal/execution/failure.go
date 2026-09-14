package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/sight"
)

func projectFailure(call messages.ToolCall, err error, kind public.ToolExecutionFailureKind, code public.ScreenToolErrorCode) messages.ToolCallResponse {
	if err == nil {
		err = errors.New("tool execution failed")
	}
	content := genericFailure(call, err)
	switch kind {
	case public.ToolExecutionFailurePageSight:
		content = pageFailure()
	case public.ToolExecutionFailurePhysicalDisplay:
		content = screenFailure(err, code)
	case public.ToolExecutionFailureGeneric:
		// Generic projection is already selected above.
	}
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: content}
}

func genericFailure(call messages.ToolCall, err error) string {
	message := fmt.Sprintf("tool %q failed", call.Name)
	if errors.Is(err, public.ErrToolExecutionTimeout) || errors.Is(err, context.DeadlineExceeded) {
		message += fmt.Sprintf(" (classification=%s)", public.ToolExecutionTimeoutClassification)
	}
	return fmt.Sprintf("%s: %s", message, err)
}

func pageFailure() string {
	result := sight.NewError(sight.SourceBrowserPage, errors.New("browser-page sight unavailable"))
	result.ErrorCode = public.PageSightUnavailableErrorCode
	result.Error = "Browser-page sight is unavailable."
	encoded, err := sight.Encode(result)
	if err != nil {
		return `{"version":2,"status":"error","source":"browser_page","error_code":"page_sight_unavailable","error":"Browser-page sight is unavailable."}`
	}
	return string(encoded)
}

type screenFailureEnvelope struct {
	Version   int    `json:"version"`
	Status    string `json:"status"`
	Source    string `json:"source"`
	ErrorCode string `json:"error_code"`
	Error     string `json:"error"`
}

func screenFailure(err error, code public.ScreenToolErrorCode) string {
	errorCode := safeScreenErrorCode(code, err)
	if errorCode == "" {
		errorCode = "capture_failed"
	}
	result := sight.NewError(sight.SourceScreen, err)
	result.ErrorCode = errorCode
	result.Error = "Screen sight is unavailable."
	encoded, encodeErr := sight.Encode(result)
	if encodeErr == nil {
		return string(encoded)
	}
	fallback, fallbackErr := json.Marshal(screenFailureEnvelope{Version: 2, Status: sight.StatusError, Source: sight.SourceScreen, ErrorCode: errorCode, Error: result.Error})
	if fallbackErr != nil {
		return `{"version":2,"status":"error","source":"screen","error_code":"capture_failed","error":"Screen sight is unavailable."}`
	}
	return string(fallback)
}
