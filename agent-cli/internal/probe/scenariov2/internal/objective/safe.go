package objective

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
)

// Text presence labels used instead of transcript content.
const (
	TextPresent = "text-present"
	TextAbsent  = "text-absent"
)

const (
	presentLabel     = "present"
	absentLabel      = "absent"
	invalidJSONLabel = "<invalid-json>"
)

// SafeJSON summarizes a JSON document by shape only, never by content.
func SafeJSON(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return Missing
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return invalidJSONLabel
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return invalidJSONLabel
	}
	return describeJSONValue(value)
}

func describeJSONValue(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		return fmt.Sprintf("object(fields=%d)", len(typed))
	case []any:
		return fmt.Sprintf("array(items=%d)", len(typed))
	case string:
		return "string"
	case json.Number:
		return "number"
	case bool:
		return "boolean"
	case nil:
		return "null"
	default:
		return "json"
	}
}

// SafeText reports transcript presence without echoing the transcript.
func SafeText(present bool) string {
	if present {
		return TextPresent
	}
	return TextAbsent
}

// Presence renders a boolean observation as present/absent.
func Presence(present bool) string {
	if present {
		return presentLabel
	}
	return absentLabel
}

type errorLabel struct {
	target error
	label  string
}

// errorLabels is ordered: the first matching sentinel names the error.
func errorLabels() []errorLabel {
	return []errorLabel{
		{webmcp.ErrStaleToolRef, string(webmcp.ErrorStaleToolRef)},
		{testkit.ErrFixtureOperationMismatch, "fixture_operation_mismatch"},
		{testkit.ErrFixtureIncomplete, "fixture_incomplete"},
		{testkit.ErrFixturePendingInvocations, "fixture_pending_invocations"},
		{testkit.ErrFixtureClosed, "fixture_closed"},
		{testkit.ErrFixtureCanceled, "fixture_canceled"},
		{testkit.ErrInvalidBrowserScript, "invalid_browser_script"},
		{webmcp.ErrBrowserNotFound, "no_browser"},
		{webmcp.ErrTargetNotFound, "no_page"},
		{webmcp.ErrInvalidToolInput, string(webmcp.ErrorInvalidToolInput)},
		{webmcp.ErrInvocationNotFound, "no_invocation"},
	}
}

// SafeError maps an observation failure to a stable code without its text.
func SafeError(err error) string {
	if err == nil {
		return ""
	}
	for _, candidate := range errorLabels() {
		if errors.Is(err, candidate.target) {
			return candidate.label
		}
	}
	var classified *webmcp.ClassifiedError
	if errors.As(err, &classified) && classified != nil && classified.Code != "" {
		return string(classified.Code)
	}
	return "observation_error"
}

// SafeURL strips user info, query, and fragment from an evidence URL.
func SafeURL(raw string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String()
}

// StringList renders names as a bracketed, comma-separated list.
func StringList(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	return "[" + strings.Join(values, ", ") + "]"
}
