package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

func writeWebMCPDirectInvocationReceipt(out io.Writer, result webmcp.InvokeResult, toolRef webmcp.ToolRef) (webmcp.InvocationID, error) {
	invocationID := result.BrowserInvocationID
	if invocationID == "" {
		invocationID = result.InvocationID
	}
	if invocationID == "" {
		return "", directInvocationReceiptError(invocationID, toolRef, errors.New("browser returned no invocation ID"))
	}
	if out == nil {
		return "", directInvocationReceiptError(invocationID, toolRef, errors.New("stderr writer is unavailable"))
	}
	receipt, err := json.Marshal(WebMCPDirectInvocationReceipt{
		Version:      webmcpDirectInvocationReceiptVersion,
		InvocationID: string(invocationID),
		ToolRef:      string(toolRef),
		State:        string(webmcp.InvocationDispatched),
	})
	if err != nil {
		return "", directInvocationReceiptError(invocationID, toolRef, err)
	}
	if len(receipt)+1 > webmcpDirectInvocationReceiptMaxBytes {
		return "", directInvocationReceiptError(invocationID, toolRef, errors.New("receipt exceeds the bounded size"))
	}
	receipt = append(receipt, '\n')
	for len(receipt) > 0 {
		written, writeErr := out.Write(receipt)
		if written > 0 {
			receipt = receipt[written:]
		}
		if writeErr != nil {
			return "", directInvocationReceiptError(invocationID, toolRef, writeErr)
		}
		if written == 0 {
			return "", directInvocationReceiptError(invocationID, toolRef, io.ErrShortWrite)
		}
	}
	if flusher, ok := out.(interface{ Flush() error }); ok {
		if err := flusher.Flush(); err != nil {
			return "", directInvocationReceiptError(invocationID, toolRef, err)
		}
	}
	return invocationID, nil
}

func directInvocationReceiptError(invocationID webmcp.InvocationID, toolRef webmcp.ToolRef, cause error) error {
	err := webmcp.NewClassifiedError(webmcp.ErrorInvocationFailed, "the WebMCP dispatch receipt could not be written", map[string]any{
		"invocation_id":       string(invocationID),
		"tool_ref":            string(toolRef),
		"phase":               "dispatch_receipt",
		"side_effect_unknown": true,
	})
	err.Cause = cause
	return err
}

func (c *WebMCPOperationsCommand) contextWithCatalog(ctx context.Context, broker webmcp.Broker, page webmcp.PageContext, refresh bool) (WebMCPDirectContext, error) {
	if refresh {
		refreshed, err := selectedDirectContext(ctx, broker, true)
		if err != nil {
			return WebMCPDirectContext{}, err
		}
		page = refreshed
	}
	snapshot, err := broker.ListTools(ctx, webmcp.ListToolsOptions{IncludeSchemas: false})
	if err != nil {
		return WebMCPDirectContext{}, err
	}
	if snapshot.Context.Key.BrowserID != "" {
		page = snapshot.Context
	}
	return directContextDataWithCatalog(page, snapshot), nil
}

func directContextData(page webmcp.PageContext) WebMCPDirectContext {
	return WebMCPDirectContext{
		BrowserID:  string(page.Key.BrowserID),
		TargetID:   string(page.Key.TargetID),
		Title:      boundedDoctorText(page.Title, 160),
		URL:        redactedDirectPageURL(page.URL),
		Origin:     safeOrigin(page.Origin),
		Generation: page.Generation,
		Connected:  page.Connected,
		Ready:      page.Ready,
	}
}

func directContextDataWithCatalog(page webmcp.PageContext, snapshot webmcp.ToolCatalogSnapshot) WebMCPDirectContext {
	data := directContextData(page)
	data.CatalogGeneration = snapshot.Generation
	data.ToolCount = len(snapshot.Tools)
	data.CatalogReady = snapshot.Context.Ready && snapshot.Context.Connected
	if data.BrowserID == "" {
		data.BrowserID = string(snapshot.Context.Key.BrowserID)
	}
	if data.TargetID == "" {
		data.TargetID = string(snapshot.Context.Key.TargetID)
	}
	if data.Generation == 0 {
		data.Generation = snapshot.Context.Generation
	}
	if data.Origin == "" {
		data.Origin = safeOrigin(snapshot.Context.Origin)
	}
	return data
}

func directTabFromTarget(target webmcp.Target) WebMCPDirectTab {
	typeName := target.Type
	if typeName == "" {
		typeName = "page"
	}
	return WebMCPDirectTab{
		BrowserID:         string(target.BrowserID),
		TargetID:          string(target.ID),
		Type:              boundedDoctorText(typeName, 40),
		Title:             boundedDoctorText(target.Title, 160),
		Origin:            safeOrigin(target.Origin),
		Eligible:          target.Eligible,
		EligibilityReason: boundedDoctorText(target.EligibilityReason, 160),
		Attached:          target.Attached,
	}
}

func directToolsData(page webmcp.PageContext, snapshot webmcp.ToolCatalogSnapshot, includeSchemas bool) WebMCPDirectToolsData {
	contextValue := snapshot.Context
	if contextValue.Key.BrowserID == "" {
		contextValue = page
	}
	tools := make([]WebMCPDirectTool, 0, len(snapshot.Tools))
	for _, descriptor := range snapshot.Tools {
		tools = append(tools, directToolFromDescriptor(descriptor, includeSchemas))
	}
	sort.SliceStable(tools, func(i, j int) bool {
		if tools[i].Frame.ID != tools[j].Frame.ID {
			return tools[i].Frame.ID < tools[j].Frame.ID
		}
		return tools[i].Name < tools[j].Name
	})
	return WebMCPDirectToolsData{
		BrowserID:  string(contextValue.Key.BrowserID),
		TargetID:   string(contextValue.Key.TargetID),
		Generation: snapshot.Generation,
		Tools:      tools,
	}
}

func directToolFromDescriptor(descriptor webmcp.ToolDescriptor, includeSchemas bool) WebMCPDirectTool {
	schema := json.RawMessage(nil)
	if includeSchemas {
		schema = append(json.RawMessage(nil), descriptor.InputSchema...)
		if len(bytes.TrimSpace(schema)) == 0 || !json.Valid(schema) {
			schema = json.RawMessage("null")
		}
	}
	annotations := make(map[string]any)
	if descriptor.Annotations.ReadOnly != nil {
		annotations["read_only"] = *descriptor.Annotations.ReadOnly
	}
	if descriptor.Annotations.UntrustedContent != nil {
		annotations["untrusted_content"] = *descriptor.Annotations.UntrustedContent
	}
	if descriptor.Annotations.AutoSubmit != nil {
		annotations["autosubmit"] = *descriptor.Annotations.AutoSubmit
	}
	return WebMCPDirectTool{
		Ref:         string(descriptor.Ref),
		Name:        boundedDoctorText(descriptor.Name, 160),
		Description: boundedDoctorText(descriptor.Description, 500),
		InputSchema: schema,
		Annotations: annotations,
		Frame:       WebMCPDirectFrame{ID: string(descriptor.FrameID), Origin: safeOrigin(descriptor.Origin)},
		Generation:  descriptor.Generation,
	}
}

func resolveDirectInvocation(args []string, values *webmcpDirectFlags, broker webmcp.Broker, ctx context.Context) (webmcp.ToolRef, json.RawMessage, error) {
	if values == nil {
		return "", nil, errors.New("invoke flags are required")
	}
	if values.toolRef != "" && len(args) > 0 {
		return "", nil, directInvalidInputError("--tool-ref cannot be combined with a positional tool name", "/tool_ref")
	}
	if len(args) > 1 && values.inputJSON != "" {
		return "", nil, directInvalidInputError("--input-json cannot be combined with key=value arguments", "/input_json")
	}
	input := json.RawMessage(values.inputJSON)
	if len(bytes.TrimSpace(input)) == 0 {
		input = json.RawMessage(`{}`)
	}
	// Keep malformed input opaque until the broker has resolved the exact
	// descriptor. The broker owns page-schema validation and can therefore
	// include the selected tool's complete schema in its retryable error.
	// Performing json.Valid here would lose that descriptor context and would
	// also make positional tool names behave differently from --tool-ref.
	if values.toolRef != "" {
		return webmcp.ToolRef(values.toolRef), append(json.RawMessage(nil), input...), nil
	}
	if len(args) == 0 {
		return "", nil, webmcp.NewClassifiedError(webmcp.ErrorInvalidToolInput, "a tool reference or unique tool name is required", map[string]any{
			"issues": []webmcp.ToolResultIssue{{Path: "/tool_ref", Code: "required"}},
		})
	}
	toolName := args[0]
	keyValues, err := parseKeyValueArgs(args[1:])
	if err != nil {
		return "", nil, err
	}
	if len(args) > 1 {
		encoded, err := json.Marshal(keyValues)
		if err != nil {
			return "", nil, webmcp.NewClassifiedError(webmcp.ErrorInvalidToolInput, "key=value arguments could not be encoded as JSON", map[string]any{
				"issues": []webmcp.ToolResultIssue{{Path: "/arguments", Code: "invalid_json"}},
			})
		}
		input = encoded
	}
	snapshot, err := broker.ListTools(ctx, webmcp.ListToolsOptions{IncludeSchemas: false})
	if err != nil {
		return "", nil, err
	}
	var match *webmcp.ToolDescriptor
	for index := range snapshot.Tools {
		if snapshot.Tools[index].Name != toolName {
			continue
		}
		if match != nil {
			return "", nil, webmcp.NewClassifiedError(webmcp.ErrorInvalidToolInput, "the positional tool name is ambiguous; use --tool-ref", map[string]any{
				"issues": []webmcp.ToolResultIssue{{Path: "/tool_name", Code: "ambiguous"}},
			})
		}
		selected := snapshot.Tools[index]
		match = &selected
	}
	if match == nil {
		return "", nil, webmcp.NewClassifiedError(webmcp.ErrorInvalidToolInput, "the positional tool name was not found in the current catalog", map[string]any{
			"issues": []webmcp.ToolResultIssue{{Path: "/tool_name", Code: "unknown_tool"}},
		})
	}
	return match.Ref, append(json.RawMessage(nil), input...), nil
}

func directInvalidInputError(message, path string) error {
	return webmcp.NewClassifiedError(webmcp.ErrorInvalidToolInput, message, map[string]any{
		"issues": []webmcp.ToolResultIssue{{Path: path, Code: "invalid"}},
	})
}

func directInvocationFailed(state webmcp.InvocationState) bool {
	switch state {
	case webmcp.InvocationError, webmcp.InvocationCanceled, webmcp.InvocationTimedOut, webmcp.InvocationOrphaned, webmcp.InvocationPolicyDenied:
		return true
	default:
		return false
	}
}

type directInvocationWaiter interface {
	WaitInvocation(context.Context, webmcp.InvocationID) (webmcp.InvokeResult, error)
}

// waitDirectInvocation adapts the broker's non-blocking Invoke contract to the
// CLI contract: a direct command returns only after a live broker has emitted
// the correlated terminal result. Keeping this as an optional seam preserves
// compatibility with small command fakes whose Invoke result is already
// terminal.
func waitDirectInvocation(ctx context.Context, broker webmcp.Broker, result webmcp.InvokeResult) (webmcp.InvokeResult, error) {
	if broker == nil || result.InvocationID == "" || result.ErrorCode != "" || directInvocationTerminal(result.State) {
		return result, nil
	}
	waiter, ok := broker.(directInvocationWaiter)
	if !ok {
		return result, nil
	}
	return waiter.WaitInvocation(ctx, result.InvocationID)
}

func directInvocationTerminal(state webmcp.InvocationState) bool {
	switch state {
	case webmcp.InvocationCompleted, webmcp.InvocationError, webmcp.InvocationCanceled, webmcp.InvocationTimedOut, webmcp.InvocationOrphaned, webmcp.InvocationPolicyDenied:
		return true
	default:
		return false
	}
}

func directInvocationResultError(result webmcp.InvokeResult, toolRef webmcp.ToolRef) error {
	code := webmcp.ErrorCode(result.ErrorCode)
	if !webmcp.IsKnownErrorCode(code) {
		switch result.State {
		case webmcp.InvocationCanceled:
			code = webmcp.ErrorInvocationCanceled
		case webmcp.InvocationTimedOut:
			code = webmcp.ErrorInvocationTimedOut
		case webmcp.InvocationOrphaned:
			code = webmcp.ErrorInvocationOrphaned
		default:
			code = webmcp.ErrorInvocationFailed
		}
	}
	details := result.ErrorDetails
	if details == nil {
		details = map[string]any{"invocation_id": string(result.InvocationID), "tool_ref": string(toolRef), "phase": "invoke"}
	}
	if result.BrowserInvocationID != "" {
		copied := make(map[string]any, len(details)+1)
		for key, value := range details {
			copied[key] = value
		}
		copied["invocation_id"] = string(result.BrowserInvocationID)
		details = copied
	}
	return webmcp.NewClassifiedError(code, "the WebMCP invocation could not be completed", details)
}

func runDirectWatchStream(ctx context.Context, stream <-chan webmcp.BrokerEvent, once bool) (WebMCPDirectWatchData, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	data := WebMCPDirectWatchData{Status: webmcpDirectWatchStatusEnded, Events: []WebMCPDirectEvent{}}
	for {
		if ctx.Err() != nil {
			data.Status = webmcpDirectWatchStatusCanceled
			return data, nil
		}
		select {
		case <-ctx.Done():
			data.Status = webmcpDirectWatchStatusCanceled
			return data, nil
		case event, ok := <-stream:
			if !ok {
				if ctx.Err() != nil {
					data.Status = webmcpDirectWatchStatusCanceled
				}
				return data, nil
			}
			data.Events = append(data.Events, directEventFrom(event))
			if event.Type == webmcp.BrokerEventSessionClosed &&
				(event.Reason == webmcp.BrokerWatchBufferFullReason || event.Reason == webmcp.BrowserEventBufferFullReason) {
				data.Status = webmcpDirectWatchStatusFailed
				return data, nil
			}
			if once {
				data.Status = webmcpDirectWatchStatusOnce
				return data, nil
			}
		}
	}
}

func directEventFrom(event webmcp.BrokerEvent) WebMCPDirectEvent {
	return WebMCPDirectEvent{
		Version:      event.Version,
		Type:         string(event.Type),
		Sequence:     event.Sequence,
		BrowserID:    string(event.BrowserID),
		TargetID:     string(event.TargetID),
		Generation:   event.Generation,
		InvocationID: string(event.InvocationID),
		ToolRef:      string(event.ToolRef),
		State:        string(event.State),
		Reason:       boundedDoctorText(event.Reason, 160),
	}
}

func writeWebMCPDirectJSON(out io.Writer, data any, operationErr error, fallback webmcp.ErrorCode) error {
	if out == nil {
		return errors.New("WebMCP command output writer is required")
	}
	var encoded []byte
	var err error
	if operationErr != nil {
		resultError := webmcpDirectErrorFor(operationErr, fallback)
		encoded, err = webmcp.EncodeToolResult(nil, &resultError)
	} else {
		if data == nil {
			data = map[string]any{"status": "ok"}
		}
		encoded, err = webmcp.EncodeToolResult(data, nil)
	}
	if err != nil {
		return fmt.Errorf("encode WebMCP command result: %w", err)
	}
	encoded = append(encoded, '\n')
	if _, err := out.Write(encoded); err != nil {
		return fmt.Errorf("write WebMCP command result: %w", err)
	}
	return nil
}

func writeWebMCPDirectHuman(out io.Writer, kind string, data any, operationErr error, fallback webmcp.ErrorCode) error {
	if out == nil {
		return errors.New("WebMCP command output writer is required")
	}
	if operationErr != nil {
		resultError := webmcpDirectErrorFor(operationErr, fallback)
		_, err := fmt.Fprintf(out, "Error: %s — %s", resultError.Code, resultError.Message)
		if err == nil {
			if invocationID, ok := resultError.Details["invocation_id"].(string); ok && invocationID != "" {
				_, err = fmt.Fprintf(out, " invocation_id=%s", boundedDoctorText(invocationID, 160))
			}
		}
		if err == nil {
			if cancelSource, ok := resultError.Details["cancel_source"].(string); ok && cancelSource != "" {
				_, err = fmt.Fprintf(out, " cancel_source=%s", boundedDoctorText(cancelSource, 40))
			}
		}
		if err == nil && resultError.Details["side_effect_unknown"] == true {
			_, err = io.WriteString(out, " side_effect_unknown=true; rollback and retry safety are unknown")
		}
		if err == nil {
			_, err = fmt.Fprintln(out)
		}
		if err != nil {
			return fmt.Errorf("write WebMCP command error: %w", err)
		}
		return nil
	}

	var err error
	switch value := data.(type) {
	case WebMCPDirectBrowsersData:
		_, err = io.WriteString(out, "Browsers:\n")
		for _, browser := range value.Browsers {
			if err != nil {
				break
			}
			_, err = fmt.Fprintf(out, "  %s  %s  source=%s scope=%s", browser.ID, displayDoctorValue(browser.Product, "unknown"), browser.Source, browser.Scope)
			if browser.Endpoint != "" {
				if err == nil {
					_, err = fmt.Fprintf(out, " endpoint=%s", browser.Endpoint)
				}
			}
			if err == nil {
				_, err = fmt.Fprintln(out)
			}
		}
	case WebMCPDirectTabsData:
		_, err = io.WriteString(out, "Tabs:\n")
		for _, tab := range value.Tabs {
			if err != nil {
				break
			}
			marker := " "
			if tab.Selected {
				marker = "*"
			}
			_, err = fmt.Fprintf(out, "  %s %s/%s  %q  origin=%s eligible=%t connected=%t", marker, tab.BrowserID, tab.TargetID, tab.Title, displayDoctorValue(tab.Origin, "unknown"), tab.Eligible, tab.Attached)
			if tab.Generation > 0 {
				if err == nil {
					_, err = fmt.Fprintf(out, " generation=%d", tab.Generation)
				}
			}
			if tab.ToolCount != nil && err == nil {
				_, err = fmt.Fprintf(out, " tools=%d", *tab.ToolCount)
			}
			if err == nil {
				_, err = fmt.Fprintln(out)
			}
		}
	case WebMCPDirectContext:
		_, err = fmt.Fprintf(out, "Context: %s/%s\n  Title:      %q\n  Origin:     %s\n  URL:        %s\n  Generation: %d\n  Connected:  %t\n  Ready:      %t\n  Catalog:    %t (%d tools)\n", value.BrowserID, value.TargetID, value.Title, displayDoctorValue(value.Origin, "unknown"), displayDoctorValue(value.URL, "unknown"), value.Generation, value.Connected, value.Ready, value.CatalogReady, value.ToolCount)
	case WebMCPDirectToolsData:
		_, err = fmt.Fprintf(out, "Tools: %s/%s generation=%d\n", value.BrowserID, value.TargetID, value.Generation)
		for _, tool := range value.Tools {
			if err != nil {
				break
			}
			_, err = fmt.Fprintf(out, "  %s  %s  frame=%s origin=%s\n", tool.Ref, tool.Name, tool.Frame.ID, displayDoctorValue(tool.Frame.Origin, "unknown"))
		}
	case WebMCPDirectInvocation:
		_, err = fmt.Fprintf(out, "Invocation: %s status=%s tool_ref=%s\nOutput: %s\n", value.InvocationID, value.Status, value.ToolRef, compactDirectJSON(value.Output))
	case WebMCPDirectCancelData:
		_, err = fmt.Fprintf(out, "Invocation %s: %s\n", value.InvocationID, value.Status)
	case WebMCPDirectWatchData:
		_, err = fmt.Fprintf(out, "Watch: %s (%d events)\n", value.Status, len(value.Events))
		for _, event := range value.Events {
			if err != nil {
				break
			}
			_, err = fmt.Fprintf(out, "  #%d %s", event.Sequence, event.Type)
			if event.BrowserID != "" || event.TargetID != "" {
				if err == nil {
					_, err = fmt.Fprintf(out, " %s/%s", event.BrowserID, event.TargetID)
				}
			}
			if event.Reason != "" && err == nil {
				_, err = fmt.Fprintf(out, " (%s)", event.Reason)
			}
			if err == nil {
				_, err = fmt.Fprintln(out)
			}
		}
	default:
		var encoded []byte
		encoded, err = json.MarshalIndent(data, "", "  ")
		if err == nil {
			_, err = fmt.Fprintln(out, string(encoded))
		}
	}
	if err != nil {
		return fmt.Errorf("write WebMCP command result: %w", err)
	}
	return nil
}

func webmcpDirectErrorFor(err error, fallback webmcp.ErrorCode) webmcp.ToolResultError {
	result := webmcp.ResultErrorFor(err, fallback, nil)
	return result
}

func compactDirectJSON(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "null"
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "null"
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(encoded)
}

func redactedDirectPageURL(raw string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		if index := strings.IndexAny(raw, "?#"); index >= 0 {
			raw = raw[:index]
		}
		return boundedDoctorText(raw, 240)
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return boundedDoctorText(parsed.String(), 240)
}
