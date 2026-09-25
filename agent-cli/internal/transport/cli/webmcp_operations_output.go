package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/direct"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/operations"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production/normalize"
)

const (
	displayUnknown = "unknown"
	jsonNullText   = "null"
)

// errDirectOutputWriterRequired reports a command without an output writer.
func errDirectOutputWriterRequired() error {
	return errors.New("WebMCP command output writer is required")
}

func writeWebMCPDirectJSON(out io.Writer, data any, operationErr error, fallback webmcp.ErrorCode) error {
	if out == nil {
		return errDirectOutputWriterRequired()
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

func writeWebMCPDirectHuman(out io.Writer, _ string, data any, operationErr error, fallback webmcp.ErrorCode) error {
	if out == nil {
		return errDirectOutputWriterRequired()
	}
	if operationErr != nil {
		if err := writeDirectHumanError(out, webmcpDirectErrorFor(operationErr, fallback)); err != nil {
			return fmt.Errorf("write WebMCP command error: %w", err)
		}
		return nil
	}
	if err := writeDirectHumanData(out, data); err != nil {
		return fmt.Errorf("write WebMCP command result: %w", err)
	}
	return nil
}

// humanWriter writes a sequence of fragments and remembers the first
// failure, after which every further write is skipped.
type humanWriter struct {
	out io.Writer
	err error
}

func (w *humanWriter) printf(format string, args ...any) {
	if w.err == nil {
		_, w.err = fmt.Fprintf(w.out, format, args...)
	}
}

func (w *humanWriter) printfIf(condition bool, format string, args ...any) {
	if condition {
		w.printf(format, args...)
	}
}

func writeDirectHumanError(out io.Writer, resultError webmcp.ToolResultError) error {
	w := &humanWriter{out: out}
	w.printf("Error: %s — %s", resultError.Code, resultError.Message)
	if invocationID, ok := resultError.Details["invocation_id"].(string); ok && invocationID != "" {
		w.printf(" invocation_id=%s", normalize.BoundedText(invocationID, operations.MaxTextLength))
	}
	if cancelSource, ok := resultError.Details["cancel_source"].(string); ok && cancelSource != "" {
		w.printf(" cancel_source=%s", normalize.BoundedText(cancelSource, operations.MaxLabelText))
	}
	w.printfIf(resultError.Details["side_effect_unknown"] == true, " side_effect_unknown=true; rollback and retry safety are unknown")
	if w.err == nil {
		w.err = writeDirectAmbiguityDetails(out, resultError)
	}
	w.printf("\n")
	return w.err
}

func writeDirectHumanData(out io.Writer, data any) error {
	switch value := data.(type) {
	case WebMCPDirectBrowsersData:
		return writeDirectHumanBrowsers(out, value)
	case WebMCPDirectTabsData:
		return writeDirectHumanTabs(out, value)
	case WebMCPDirectContext:
		_, err := fmt.Fprintf(out, "Context: %s/%s\n  Title:      %q\n  Origin:     %s\n  URL:        %s\n  Generation: %d\n  Connected:  %t\n  Ready:      %t\n  Catalog:    %t (%d tools)\n", value.BrowserID, value.TargetID, value.Title, displayDoctorValue(value.Origin, displayUnknown), displayDoctorValue(value.URL, displayUnknown), value.Generation, value.Connected, value.Ready, value.CatalogReady, value.ToolCount)
		return err
	case WebMCPDirectToolsData:
		return writeDirectHumanTools(out, value)
	case WebMCPDirectInvocation:
		_, err := fmt.Fprintf(out, "Invocation: %s status=%s tool_ref=%s\nOutput: %s\n", value.InvocationID, value.Status, value.ToolRef, compactDirectJSON(value.Output))
		return err
	case WebMCPDirectCancelData:
		_, err := fmt.Fprintf(out, "Invocation %s: %s\n", value.InvocationID, value.Status)
		return err
	case WebMCPDirectWatchData:
		return writeDirectHumanWatch(out, value)
	default:
		encoded, err := json.MarshalIndent(data, "", "  ")
		if err == nil {
			_, err = fmt.Fprintln(out, string(encoded))
		}
		return err
	}
}

func writeDirectHumanBrowsers(out io.Writer, value WebMCPDirectBrowsersData) error {
	w := &humanWriter{out: out}
	w.printf("Browsers:\n")
	for _, browser := range value.Browsers {
		w.printf("  %s  %s  source=%s scope=%s", browser.ID, displayDoctorValue(browser.Product, displayUnknown), browser.Source, browser.Scope)
		w.printfIf(browser.Endpoint != "", " endpoint=%s", browser.Endpoint)
		w.printf("\n")
	}
	return w.err
}

func writeDirectHumanTabs(out io.Writer, value WebMCPDirectTabsData) error {
	w := &humanWriter{out: out}
	w.printf("Tabs:\n")
	for _, tab := range value.Tabs {
		marker := " "
		if tab.Selected {
			marker = "*"
		}
		w.printf("  %s %s/%s  %q  origin=%s eligible=%t connected=%t", marker, tab.BrowserID, tab.TargetID, tab.Title, displayDoctorValue(tab.Origin, displayUnknown), tab.Eligible, tab.Attached)
		w.printfIf(tab.Generation > 0, " generation=%d", tab.Generation)
		if tab.ToolCount != nil {
			w.printf(" tools=%d", *tab.ToolCount)
		}
		w.printf("\n")
	}
	return w.err
}

func writeDirectHumanTools(out io.Writer, value WebMCPDirectToolsData) error {
	w := &humanWriter{out: out}
	w.printf("Tools: %s/%s generation=%d\n", value.BrowserID, value.TargetID, value.Generation)
	for _, tool := range value.Tools {
		w.printf("  %s  %s  frame=%s origin=%s\n", tool.Ref, tool.Name, tool.Frame.ID, displayDoctorValue(tool.Frame.Origin, displayUnknown))
	}
	return w.err
}

func writeDirectHumanWatch(out io.Writer, value WebMCPDirectWatchData) error {
	w := &humanWriter{out: out}
	w.printf("Watch: %s (%d events)\n", value.Status, len(value.Events))
	for _, event := range value.Events {
		w.printf("  #%d %s", event.Sequence, event.Type)
		w.printfIf(event.BrowserID != "" || event.TargetID != "", " %s/%s", event.BrowserID, event.TargetID)
		w.printfIf(event.Reason != "", " (%s)", event.Reason)
		w.printf("\n")
	}
	return w.err
}

func webmcpDirectErrorFor(err error, fallback webmcp.ErrorCode) webmcp.ToolResultError {
	result := webmcp.ResultErrorFor(err, fallback, nil)
	if result.Details == nil {
		result.Details = map[string]any{}
	}
	switch webmcp.ErrorCode(result.Code) {
	case webmcp.ErrorAmbiguousBrowser:
		result.Details["candidate_browser_ids"] = boundedAmbiguityIDs(result.Details["candidate_browser_ids"])
	case webmcp.ErrorAmbiguousTab:
		browserID := direct.NormalizeOpaqueID(stringValue(result.Details["browser_id"]))
		ids := boundedAmbiguityIDs(result.Details["candidate_target_ids"])
		result.Details["browser_id"] = browserID
		result.Details["candidate_target_ids"] = ids
		if choices := direct.SafeCandidateChoices(result.Details["candidate_choices"], browserID, ids); len(choices) > 0 {
			result.Details["candidate_choices"] = choices
		} else {
			delete(result.Details, "candidate_choices")
		}
	}
	return result
}

func boundedAmbiguityIDs(value any) []string {
	ids := direct.SafeIDList(value)
	if len(ids) > direct.MaxAmbiguityCandidates {
		ids = ids[:direct.MaxAmbiguityCandidates]
	}
	return ids
}

func writeDirectAmbiguityDetails(out io.Writer, result webmcp.ToolResultError) error {
	switch webmcp.ErrorCode(result.Code) {
	case webmcp.ErrorAmbiguousBrowser:
		ids := direct.SafeIDList(result.Details["candidate_browser_ids"])
		if len(ids) > 0 {
			_, err := fmt.Fprintf(out, " candidate_browser_ids=%s", strings.Join(ids, ","))
			return err
		}
	case webmcp.ErrorAmbiguousTab:
		browserID := direct.NormalizeOpaqueID(stringValue(result.Details["browser_id"]))
		ids := direct.SafeIDList(result.Details["candidate_target_ids"])
		if browserID != "" {
			if _, err := fmt.Fprintf(out, " browser_id=%s", browserID); err != nil {
				return err
			}
		}
		if len(ids) > 0 {
			_, err := fmt.Fprintf(out, " candidate_target_ids=%s", strings.Join(ids, ","))
			return err
		}
	}
	return nil
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func compactDirectJSON(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return jsonNullText
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return jsonNullText
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return jsonNullText
	}
	return string(encoded)
}
