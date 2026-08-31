package room

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseManifest_BrowserToolsNormalizesJSONOptionsAndRedactsEndpoints(t *testing.T) {
	t.Setenv("ROOM_CUSTOMER_KEY", "customer-secret")
	t.Setenv("ROOM_ASSISTANT_KEY", "assistant-secret")
	data := validManifestData(t, func(document map[string]any) {
		document["participants"].([]any)[0].(map[string]any)["browserTools"] = map[string]any{
			"backend": "webmcp",
			"connection": map[string]any{
				"cdp_url":            " http://127.0.0.1:9222/json/version?token=cdp-secret#fragment-secret ",
				"ws_endpoint":        "ws://127.0.0.1:9222/devtools/browser/browser-secret?token=ws-secret#fragment-secret",
				"user_data_dir":      " /tmp/room-browser ",
				"allow_process_scan": true,
				"allow_remote_cdp":   false,
			},
			"selection": map[string]any{
				"browser":      " browser-1 ",
				"tab":          " tab-1 ",
				"origin":       " https://cube.example ",
				"auto_select":  "single",
				"activate_tab": true,
				"persist":      false,
			},
			"policy": map[string]any{
				"allowed_origins":     []any{" https://cube.example ", "https://docs.example"},
				"denied_origins":      []any{"https://blocked.example"},
				"approval":            "always",
				"cancel_on_interrupt": "always",
			},
			"limits": map[string]any{
				"invocation_timeout":   "2m30s",
				"max_input_bytes":      1024,
				"max_result_bytes":     2048,
				"serialize_per_target": false,
			},
			"recording": map[string]any{
				"enabled":             true,
				"include_arguments":   false,
				"include_results":     false,
				"redact_url_query":    true,
				"redact_url_fragment": true,
			},
			"replay": map[string]any{
				"path":   "/tmp/room-replay.jsonl",
				"strict": false,
			},
		}
	})

	manifest, err := ParseManifest(data)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	participant := manifest.Participants[0]
	if participant.BrowserTools == nil {
		t.Fatal("browserTools = nil, want enabled participant capability")
	}
	if manifest.Participants[1].BrowserTools != nil {
		t.Fatal("omitted browserTools enabled the other participant")
	}
	browser := participant.BrowserTools
	if browser.Backend != "webmcp" || browser.Connection.CDPURL != "http://127.0.0.1:9222/json/version?token=cdp-secret#fragment-secret" || browser.Connection.WSEndpoint == "" {
		t.Fatalf("normalized browser connection = %+v", browser.Connection)
	}
	if browser.Selection.Browser != "browser-1" || browser.Selection.Tab != "tab-1" || browser.Selection.AutoSelect != "single" || !browser.Selection.ActivateTab || browser.Selection.Persist {
		t.Fatalf("normalized browser selection = %+v", browser.Selection)
	}
	if len(browser.Policy.AllowedOrigins) != 2 || browser.Policy.AllowedOrigins[0] != "https://cube.example" || browser.Policy.Approval != "always" {
		t.Fatalf("normalized browser policy = %+v", browser.Policy)
	}
	if browser.Limits.InvocationTimeout != 150*time.Second || browser.Limits.MaxInputBytes != 1024 || browser.Limits.MaxResultBytes != 2048 || browser.Limits.SerializePerTarget {
		t.Fatalf("normalized browser limits = %+v", browser.Limits)
	}
	if !browser.Recording.Enabled || browser.Recording.IncludeArguments || browser.Recording.IncludeResults || !browser.Recording.RedactURLQuery || browser.Replay.Path != "/tmp/room-replay.jsonl" || browser.Replay.Strict {
		t.Fatalf("normalized browser recording/replay = %+v/%+v", browser.Recording, browser.Replay)
	}

	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal normalized manifest: %v", err)
	}
	serialized := string(encoded)
	for _, forbidden := range []string{"cdp-secret", "ws-secret", "fragment-secret", "browser-secret", "token="} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("normalized manifest leaked endpoint material %q: %s", forbidden, serialized)
		}
	}
	if !strings.Contains(serialized, `"browserTools"`) || !strings.Contains(serialized, `"invocation_timeout":"2m30s"`) {
		t.Fatalf("normalized manifest omitted browser configuration: %s", serialized)
	}
	if !strings.Contains(serialized, `"ws_endpoint":"ws://127.0.0.1:9222/%3Credacted%3E"`) {
		t.Fatalf("normalized manifest did not redact browser websocket path: %s", serialized)
	}
}

func TestParseManifest_BrowserToolsAcceptsYAMLAndAppliesSessionDefaults(t *testing.T) {
	t.Setenv("ROOM_CUSTOMER_KEY", "customer-secret")
	t.Setenv("ROOM_ASSISTANT_KEY", "assistant-secret")
	data := []byte(`schema_version: 1
room:
  max_turns: 1
participants:
  - id: customer
    system_prompt: "Use the browser"
    opening_prompt: "Use the browser to start."
    provider: openai
    model: gpt-realtime
    api_key_env: ROOM_CUSTOMER_KEY
    tools: []
    browserTools: {}
  - id: assistant
    system_prompt: "Answer"
    provider: openai
    model: gpt-realtime
    api_key_env: ROOM_ASSISTANT_KEY
    tools: []
`)

	manifest, err := ParseManifest(data)
	if err != nil {
		t.Fatalf("ParseManifest YAML: %v", err)
	}
	browser := manifest.Participants[0].BrowserTools
	if browser == nil {
		t.Fatal("browserTools = nil, want enabled participant capability")
	}
	defaults := DefaultBrowserToolsConfig()
	if browser.Backend != defaults.Backend || browser.Selection.AutoSelect != defaults.Selection.AutoSelect || browser.Policy.Approval != defaults.Policy.Approval || browser.Policy.CancelOnInterrupt != defaults.Policy.CancelOnInterrupt || browser.Limits != defaults.Limits || browser.Recording != defaults.Recording || browser.Replay != defaults.Replay {
		t.Fatalf("browser defaults = %+v, want %+v", *browser, defaults)
	}
	if browser.Policy.AllowedOrigins == nil || browser.Policy.DeniedOrigins == nil {
		t.Fatalf("default origin lists = %+v, want initialized empty lists", browser.Policy)
	}
}

func TestParseManifest_BrowserToolsRejectsInvalidParticipantQualifiedOptions(t *testing.T) {
	t.Setenv("ROOM_CUSTOMER_KEY", "customer-secret")
	t.Setenv("ROOM_ASSISTANT_KEY", "assistant-secret")
	tests := []struct {
		name   string
		field  string
		cause  error
		mutate func(map[string]any)
	}{
		{
			name:  "unsupported backend",
			field: "participants[0].browserTools.backend",
			cause: ErrUnsupportedBrowserToolsBackend,
			mutate: func(document map[string]any) {
				document["participants"].([]any)[0].(map[string]any)["browserTools"] = map[string]any{"backend": "chrome"}
			},
		},
		{
			name:  "invalid auto select",
			field: "participants[0].browserTools.selection.auto_select",
			cause: ErrInvalidBrowserToolsOption,
			mutate: func(document map[string]any) {
				document["participants"].([]any)[0].(map[string]any)["browserTools"] = map[string]any{
					"selection": map[string]any{"auto_select": "many"},
				}
			},
		},
		{
			name:  "invalid duration",
			field: "participants[0].browserTools.limits.invocation_timeout",
			cause: ErrInvalidBrowserToolsOption,
			mutate: func(document map[string]any) {
				document["participants"].([]any)[0].(map[string]any)["browserTools"] = map[string]any{
					"limits": map[string]any{"invocation_timeout": "soon"},
				}
			},
		},
		{
			name:  "negative size",
			field: "participants[0].browserTools.limits.max_input_bytes",
			cause: ErrInvalidBrowserToolsOption,
			mutate: func(document map[string]any) {
				document["participants"].([]any)[0].(map[string]any)["browserTools"] = map[string]any{
					"limits": map[string]any{"max_input_bytes": -1},
				}
			},
		},
		{
			name:  "invalid CDP scheme",
			field: "participants[0].browserTools.connection.cdp_url",
			cause: ErrInvalidBrowserEndpoint,
			mutate: func(document map[string]any) {
				document["participants"].([]any)[0].(map[string]any)["browserTools"] = map[string]any{
					"connection": map[string]any{"cdp_url": "file:///tmp/debug"},
				}
			},
		},
		{
			name:  "page websocket",
			field: "participants[0].browserTools.connection.ws_endpoint",
			cause: ErrInvalidBrowserEndpoint,
			mutate: func(document map[string]any) {
				document["participants"].([]any)[0].(map[string]any)["browserTools"] = map[string]any{
					"connection": map[string]any{"ws_endpoint": "ws://127.0.0.1:9222/devtools/page/page-secret"},
				}
			},
		},
		{
			name:  "remote endpoint without opt in",
			field: "participants[0].browserTools.connection.cdp_url",
			cause: ErrInvalidBrowserEndpoint,
			mutate: func(document map[string]any) {
				document["participants"].([]any)[0].(map[string]any)["browserTools"] = map[string]any{
					"connection": map[string]any{"cdp_url": "https://browser.example:9222"},
				}
			},
		},
		{
			name:  "null browser tools object",
			field: "participants[0].browserTools",
			cause: ErrInvalidBrowserToolsOption,
			mutate: func(document map[string]any) {
				document["participants"].([]any)[0].(map[string]any)["browserTools"] = nil
			},
		},
		{
			name:  "malformed boolean",
			field: "participants[0].browserTools.connection.allow_remote_cdp",
			cause: ErrInvalidBrowserToolsOption,
			mutate: func(document map[string]any) {
				document["participants"].([]any)[0].(map[string]any)["browserTools"] = map[string]any{
					"connection": map[string]any{"allow_remote_cdp": "yes"},
				}
			},
		},
		{
			name:  "malformed duration type",
			field: "participants[0].browserTools.limits.invocation_timeout",
			cause: ErrInvalidBrowserToolsOption,
			mutate: func(document map[string]any) {
				document["participants"].([]any)[0].(map[string]any)["browserTools"] = map[string]any{
					"limits": map[string]any{"invocation_timeout": 30},
				}
			},
		},
		{
			name:  "malformed origin list",
			field: "participants[0].browserTools.policy.allowed_origins",
			cause: ErrInvalidBrowserToolsOption,
			mutate: func(document map[string]any) {
				document["participants"].([]any)[0].(map[string]any)["browserTools"] = map[string]any{
					"policy": map[string]any{"allowed_origins": "https://cube.example"},
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseManifest(validManifestData(t, test.mutate))
			assertManifestError(t, err, test.field, test.cause)
			if strings.Contains(err.Error(), "browser-secret") || strings.Contains(err.Error(), "page-secret") {
				t.Fatalf("browser endpoint secret leaked in validation error: %v", err)
			}
		})
	}
}

func TestParseManifest_BrowserToolsRejectsUnknownNestedFields(t *testing.T) {
	t.Setenv("ROOM_CUSTOMER_KEY", "customer-secret")
	t.Setenv("ROOM_ASSISTANT_KEY", "assistant-secret")
	data := validManifestData(t, func(document map[string]any) {
		document["participants"].([]any)[0].(map[string]any)["browserTools"] = map[string]any{
			"connection": map[string]any{"unknown": true},
		}
	})
	_, err := ParseManifest(data)
	if err == nil || !errors.Is(err, ErrInvalidDocument) || !strings.Contains(err.Error(), "field unknown") {
		t.Fatalf("unknown browser field error = %v, want strict browserTools document error", err)
	}
}

func TestManifestValidate_BrowserToolsRejectsDirectUnnormalizedValue(t *testing.T) {
	t.Setenv("A", "a")
	t.Setenv("B", "b")
	manifest := Manifest{
		SchemaVersion: SchemaVersion,
		Room:          Room{MaxTurns: 1},
		Participants: []Participant{
			{ID: "a", SystemPrompt: "a", Provider: "openai", Model: "gpt", APIKeyEnv: "A", Tools: []string{}, BrowserTools: &BrowserToolsConfig{Backend: "webmcp"}},
			{ID: "b", SystemPrompt: "b", Provider: "openai", Model: "gpt", APIKeyEnv: "B", Tools: []string{}},
		},
	}
	err := manifest.Validate()
	assertManifestError(t, err, "participants[0].browserTools.selection.auto_select", ErrInvalidBrowserToolsOption)
}
