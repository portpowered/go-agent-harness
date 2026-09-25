package operations

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/direct"
)

const (
	issuePathToolRef        = "/tool_ref"
	issuePathToolName       = "/tool_name"
	safeRetryableKey        = "safe_retryable"
	phaseInvoke             = "invoke"
	messageInvocationFailed = "the WebMCP invocation could not be completed"

	// resultDetailExtras is the room reserved for the correlation details
	// added to a failed result: invocation ID, tool reference, and phase.
	resultDetailExtras = 3
)

// InvocationInput is the caller's tool selection and page input: an exact
// ToolRef, or a positional unique tool name followed by key=value arguments,
// with InputJSON as the alternative to those arguments. ParseArguments
// decodes the key=value arguments.
type InvocationInput struct {
	Args           []string
	ToolRef        string
	InputJSON      string
	ParseArguments func([]string) (map[string]any, error)
}

// ResolveInvocation resolves the tool reference and page input. Malformed
// input JSON stays opaque until the broker has resolved the exact
// descriptor: the broker owns page-schema validation and can therefore
// include the selected tool's complete schema in its retryable error, and a
// positional tool name then behaves exactly like --tool-ref.
func ResolveInvocation(ctx context.Context, broker webmcp.Broker, request InvocationInput) (webmcp.ToolRef, json.RawMessage, error) {
	if request.ToolRef != "" && len(request.Args) > 0 {
		return "", nil, direct.InvalidInputError("--tool-ref cannot be combined with a positional tool name", issuePathToolRef)
	}
	if len(request.Args) > 1 && request.InputJSON != "" {
		return "", nil, direct.InvalidInputError("--input-json cannot be combined with key=value arguments", "/input_json")
	}
	input := json.RawMessage(request.InputJSON)
	if len(bytes.TrimSpace(input)) == 0 {
		input = json.RawMessage(`{}`)
	}
	if request.ToolRef != "" {
		return webmcp.ToolRef(request.ToolRef), append(json.RawMessage(nil), input...), nil
	}
	if len(request.Args) == 0 {
		return "", nil, invalidToolInput("a tool reference or unique tool name is required", issuePathToolRef, "required")
	}
	if len(request.Args) > 1 {
		encoded, err := encodeArguments(request.Args[1:], request.ParseArguments)
		if err != nil {
			return "", nil, err
		}
		input = encoded
	}
	ref, err := toolRefByName(ctx, broker, request.Args[0])
	if err != nil {
		return "", nil, err
	}
	return ref, append(json.RawMessage(nil), input...), nil
}

func encodeArguments(args []string, parse func([]string) (map[string]any, error)) (json.RawMessage, error) {
	if parse == nil {
		return nil, direct.InvalidInputError("key=value arguments are not supported", "/arguments")
	}
	keyValues, err := parse(args)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(keyValues)
	if err != nil {
		return nil, invalidToolInput("key=value arguments could not be encoded as JSON", "/arguments", "invalid_json")
	}
	return encoded, nil
}

// toolRefByName resolves a positional name against the current catalog; the
// name must match exactly one tool.
func toolRefByName(ctx context.Context, broker webmcp.Broker, toolName string) (webmcp.ToolRef, error) {
	snapshot, err := broker.ListTools(ctx, webmcp.ListToolsOptions{IncludeSchemas: false})
	if err != nil {
		return "", err
	}
	var match webmcp.ToolRef
	found := false
	for _, tool := range snapshot.Tools {
		if tool.Name != toolName {
			continue
		}
		if found {
			return "", invalidToolInput("the positional tool name is ambiguous; use --tool-ref", issuePathToolName, "ambiguous")
		}
		match, found = tool.Ref, true
	}
	if !found {
		return "", invalidToolInput("the positional tool name was not found in the current catalog", issuePathToolName, "unknown_tool")
	}
	return match, nil
}

func invalidToolInput(message, path, code string) error {
	return webmcp.NewClassifiedError(webmcp.ErrorInvalidToolInput, message, map[string]any{
		"issues": []webmcp.ToolResultIssue{{Path: path, Code: code}},
	})
}

// WaitInvocation adapts the broker's non-blocking Invoke contract to the
// direct command contract: a command returns only after a live broker has
// emitted the correlated terminal result. The waiter is an optional seam, so
// a small broker whose Invoke result is already terminal stays compatible.
func WaitInvocation(ctx context.Context, broker webmcp.Broker, result webmcp.InvokeResult) (webmcp.InvokeResult, error) {
	if broker == nil || result.InvocationID == "" || result.ErrorCode != "" || invocationTerminal(result.State) {
		return result, nil
	}
	waiter, ok := broker.(webmcp.InvocationWaiter)
	if !ok {
		return result, nil
	}
	return waiter.WaitInvocation(ctx, result.InvocationID)
}

func invocationTerminal(state webmcp.InvocationState) bool {
	return state == webmcp.InvocationCompleted || state.Failed()
}

// InvocationResultError classifies a failed terminal result. The broker's
// internal retry marker becomes the error's Retryable flag and never
// crosses the result boundary; the browser invocation ID is preferred for
// correlation.
func InvocationResultError(result webmcp.InvokeResult, toolRef webmcp.ToolRef) error {
	details := make(map[string]any, len(result.ErrorDetails)+resultDetailExtras)
	for key, value := range result.ErrorDetails {
		details[key] = value
	}
	if len(details) == 0 {
		details = map[string]any{
			detailInvocationID: string(result.InvocationID),
			detailToolRef:      string(toolRef),
			detailPhase:        phaseInvoke,
		}
	}
	retryable, isBool := details[safeRetryableKey].(bool)
	retryable = isBool && retryable
	delete(details, safeRetryableKey)
	if result.BrowserInvocationID != "" {
		details[detailInvocationID] = string(result.BrowserInvocationID)
	}
	classified := webmcp.NewClassifiedError(resultErrorCode(result), messageInvocationFailed, details)
	classified.Retryable = retryable
	return classified
}

func resultErrorCode(result webmcp.InvokeResult) webmcp.ErrorCode {
	code := webmcp.ErrorCode(result.ErrorCode)
	if webmcp.IsKnownErrorCode(code) {
		return code
	}
	if stateCode, ok := terminalStateCodes()[result.State]; ok {
		return stateCode
	}
	return webmcp.ErrorInvocationFailed
}

// terminalStateCodes classifies the terminal states that have their own
// error code; every other failed state is a generic invocation failure.
func terminalStateCodes() map[webmcp.InvocationState]webmcp.ErrorCode {
	return map[webmcp.InvocationState]webmcp.ErrorCode{
		webmcp.InvocationCanceled: webmcp.ErrorInvocationCanceled,
		webmcp.InvocationTimedOut: webmcp.ErrorInvocationTimedOut,
		webmcp.InvocationOrphaned: webmcp.ErrorInvocationOrphaned,
	}
}
