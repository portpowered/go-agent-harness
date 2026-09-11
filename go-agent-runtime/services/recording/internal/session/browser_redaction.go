package session

import (
	"bytes"
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
)

const browserRedactionRuleCapacity = 5

func redactBrowserPayload(raw json.RawMessage, options recording.BrowserOptions, credentials []string) (json.RawMessage, browserRedaction, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, browserRedaction{}, err
	}
	changed := redactBrowserValue(&value, options, credentials)
	data, err := json.Marshal(value)
	if err != nil {
		return nil, browserRedaction{}, err
	}
	return data, browserRedactionFor(options, changed), nil
}

func browserRedactionFor(options recording.BrowserOptions, changed bool) browserRedaction {
	redaction := browserRedaction{Mode: "none", Rules: []string{"raw_cdp_disabled"}}
	if !changed {
		return redaction
	}
	rules := make([]string, 0, browserRedactionRuleCapacity)
	if options.RedactURLQuery {
		rules = append(rules, "url_query")
	}
	if options.RedactURLFragment {
		rules = append(rules, "url_fragment")
	}
	if len(options.ToolArguments) > 0 {
		rules = append(rules, "tool_arguments")
	}
	if len(options.ResultJSONPointers) > 0 {
		rules = append(rules, "result_json_pointers")
	}
	rules = append(rules, "raw_cdp_disabled")
	return browserRedaction{Mode: "redacted", Rules: rules}
}

func redactBrowserValue(value *any, options recording.BrowserOptions, credentials []string) bool {
	if value == nil || *value == nil {
		return false
	}
	switch current := (*value).(type) {
	case string:
		return redactBrowserString(value, current, options, credentials)
	case []any:
		return redactBrowserSlice(current, options, credentials)
	case map[string]any:
		return redactBrowserMap(current, options, credentials)
	default:
		return false
	}
}

func redactBrowserString(value *any, current string, options recording.BrowserOptions, credentials []string) bool {
	redacted := current
	for _, credential := range credentials {
		if strings.TrimSpace(credential) != "" {
			redacted = strings.ReplaceAll(redacted, credential, transcript.RecordingRedactionMarker)
		}
	}
	if parsed, ok := redactBrowserURL(redacted, options); ok {
		redacted = parsed
	}
	if redacted == current {
		return false
	}
	*value = redacted
	return true
}

func redactBrowserURL(value string, options recording.BrowserOptions) (string, bool) {
	if !options.RedactURLQuery && !options.RedactURLFragment {
		return value, false
	}
	parsed, err := url.Parse(value)
	if err != nil || !isRedactableURL(parsed) {
		return value, false
	}
	if options.RedactURLQuery {
		parsed.RawQuery = ""
		parsed.ForceQuery = false
	}
	if options.RedactURLFragment {
		parsed.Fragment = ""
		parsed.RawFragment = ""
	}
	return parsed.String(), true
}

func redactBrowserSlice(values []any, options recording.BrowserOptions, credentials []string) bool {
	changed := false
	for index := range values {
		child := any(values[index])
		if redactBrowserValue(&child, options, credentials) {
			changed = true
		}
		values[index] = child
	}
	return changed
}

func redactBrowserMap(values map[string]any, options recording.BrowserOptions, credentials []string) bool {
	changed := false
	for key, child := range values {
		copyChild := any(child)
		if redactBrowserValue(&copyChild, options, credentials) {
			changed = true
		}
		values[key] = copyChild
	}
	return changed
}

func isRedactableURL(value *url.URL) bool {
	switch strings.ToLower(value.Scheme) {
	case "http", "https", "ws", "wss", "ftp":
		return true
	default:
		return false
	}
}

func sortedStrings(values []string) []string {
	copyValues := append([]string(nil), values...)
	sort.Strings(copyValues)
	return copyValues
}
