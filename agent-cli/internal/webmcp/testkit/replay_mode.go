package testkit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// OperationDiscover and the list constants are diagnostic-only caller
// operation names. They are deliberately not included in the frozen
// browser-script.v1 operation vocabulary.
const (
	OperationDiscover           OperationType = "discover"
	OperationList               OperationType = "list"
	OperationListTools          OperationType = "list_tools"
	OperationBrowserDiscover    OperationType = "browser_discover"
	OperationBrowserListTargets OperationType = "browser_list_targets"
	OperationBrowserListTools   OperationType = "browser_list_tools"
	OperationDoctor             OperationType = "doctor"
	OperationContext            OperationType = "context"
	OperationBrowsers           OperationType = "browsers"
	OperationTabs               OperationType = "tabs"
	OperationTools              OperationType = "tools"
)

// IsDiagnosticReadOnlyOperation reports whether request belongs to the fixed
// discovery/list vocabulary. Shape validation is intentionally separate.
func IsDiagnosticReadOnlyOperation(request OperationRequest) bool {
	switch request.Type {
	case OperationDiscover, OperationList, OperationListTargets, OperationListTools,
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

// LoadReplayScriptReader is the reader form of LoadReplayScriptFile.
func LoadReplayScriptReader(reader io.Reader) (BrowserScript, error) {
	return LoadBrowserScriptReader(reader)
}

func validateStableFixtureError(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return errors.New("must be a non-null stable error")
	}
	if trimmed[0] == '"' {
		value, err := parseScriptString(trimmed)
		if err != nil {
			return err
		}
		if strings.TrimSpace(value) == "" {
			return errors.New("must not be empty")
		}
		return nil
	}
	if trimmed[0] != '{' {
		return errors.New("must be a string or object")
	}
	fields, err := decodeJSONObject(trimmed)
	if err != nil {
		return err
	}
	codeRaw, ok := fields["code"]
	if !ok {
		return errors.New("object error requires code")
	}
	code, err := parseScriptString(codeRaw)
	if err != nil {
		return fmt.Errorf("code: %w", err)
	}
	if strings.TrimSpace(code) == "" {
		return errors.New("code must not be empty")
	}
	if messageRaw, ok := fields["message"]; ok {
		if _, err := parseScriptString(messageRaw); err != nil {
			return fmt.Errorf("message: %w", err)
		}
	}
	if detailsRaw, ok := fields["details"]; ok && !isJSONObject(detailsRaw) {
		return errors.New("details must be a JSON object")
	}
	for name := range fields {
		if name != "code" && name != "message" && name != "details" {
			return fmt.Errorf("unknown field %q", name)
		}
	}
	return nil
}
