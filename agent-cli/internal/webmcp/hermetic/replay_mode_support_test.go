package hermetic

import (
	"errors"
)

// IsDiagnosticReadOnlyOperation reports whether request belongs to the fixed
// discovery/list vocabulary. Shape validation is intentionally separate.
func IsDiagnosticReadOnlyOperation(request OperationRequest) bool {
	switch request.Type {
	case OperationDiscover, OperationList, listTargetsOperation, OperationListTools,
		OperationBrowserDiscover, OperationBrowserListTargets, OperationBrowserListTools,
		OperationDoctor, OperationContext, OperationBrowsers, OperationTabs, OperationTools:
		return true
	case OperationEnableLifecycle, OperationEnableWebMCP, OperationInvokeTool, OperationCancelTool,
		OperationNavigate, OperationCloseTarget, OperationDetachTarget:
		return false
	default:
		return false
	}
}

func isDiagnosticReadOnlyOperation(request OperationRequest) bool {
	return IsDiagnosticReadOnlyOperation(request)
}

func validateDiagnosticReadOnlyRequest(request OperationRequest) error {
	if !IsDiagnosticReadOnlyOperation(request) {
		return errors.New("operation is not a diagnostic discovery/list operation")
	}
	if request.FrameID != "" || request.ToolName != "" || request.Input != nil || request.InvocationID != "" || request.URL != "" {
		return errors.New("read-only discovery/list operation does not accept arguments")
	}
	return nil
}

func cloneOperationRequests(requests []OperationRequest) []OperationRequest {
	if requests == nil {
		return nil
	}
	result := make([]OperationRequest, len(requests))
	for index, request := range requests {
		result[index] = cloneOperationRequest(request)
	}
	return result
}

func cloneBrowserScript(script BrowserScript) BrowserScript {
	result := script
	result.Endpoint.Targets = append([]BrowserTarget(nil), script.Endpoint.Targets...)
	result.Operations = make([]BrowserScriptOperation, len(script.Operations))
	for index, operation := range script.Operations {
		result.Operations[index] = operation
		result.Operations[index].Expect.Input = cloneRaw(operation.Expect.Input)
		result.Operations[index].Result = cloneRaw(operation.Result)
		result.Operations[index].Emit = make([]EmittedEvent, len(operation.Emit))
		for emitIndex, emitted := range operation.Emit {
			result.Operations[index].Emit[emitIndex] = emitted
			result.Operations[index].Emit[emitIndex].Tools = cloneToolDescriptors(emitted.Tools)
			result.Operations[index].Emit[emitIndex].Output = cloneRaw(emitted.Output)
			result.Operations[index].Emit[emitIndex].Error = cloneRaw(emitted.Error)
		}
	}
	return result
}
