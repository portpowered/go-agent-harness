package objective

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
)

const fixtureBrowserPath = "fixture.browser.json"

func TestVerifierUsesPersistedPageState(t *testing.T) {
	scenario := probe.ScenarioV2{
		SchemaVersion:  probe.ScenarioV2Version,
		ID:             "oracle-negative-control",
		BrowserFixture: fixtureBrowserPath,
		Expectations: []probe.ScenarioV2Expectation{{
			Type:  probe.ScenarioV2ExpectationPageStateEquals,
			Path:  "$.value",
			Value: json.RawMessage(`"expected"`),
		}},
	}
	events := []testkit.Event{{
		Version:   testkit.BrowserEventsVersion,
		Sequence:  1,
		Type:      testkit.EventBrowserDiscoveryCompleted,
		Payload:   testkit.MustJSONValue(map[string]any{"candidate_count": 1}),
		Redaction: testkit.RedactionMetadata{Mode: testkit.RedactionNone},
	}}
	wrong := VerifyEvidenceData(scenario, events, json.RawMessage(`{"value":"wrong"}`), runtimeReplay.CaptureProbeObservation{}, true)
	if wrong.Verified || !strings.Contains(wrong.Error, "page_state_equals") || strings.Contains(wrong.Error, "wrong") {
		t.Fatalf("wrong oracle verification = %+v, want a safe mismatch", wrong)
	}
	right := VerifyEvidenceData(scenario, events, json.RawMessage(`{"value":"expected"}`), runtimeReplay.CaptureProbeObservation{}, true)
	if !right.Verified {
		t.Fatalf("matching oracle verification = %+v, want verified", right)
	}
	missing := VerifyEvidenceData(scenario, nil, json.RawMessage(`{"value":"expected"}`), runtimeReplay.CaptureProbeObservation{}, false)
	if missing.Verified || !strings.Contains(missing.Error, "missing browser event artifact") {
		t.Fatalf("missing artifact verification = %+v", missing)
	}
}

func TestVerifierUsesProviderCaptureForTranscript(t *testing.T) {
	scenario := probe.ScenarioV2{
		SchemaVersion: probe.ScenarioV2Version,
		ID:            "provider-transcript-objective",
		Expectations: []probe.ScenarioV2Expectation{{
			Type: probe.ScenarioV2ExpectationTranscriptContains,
			Text: "expected-secret-transcript",
		}},
	}
	capture := runtimeReplay.CaptureProbeObservation{Transcript: "expected-secret-transcript"}
	verification := VerifyEvidenceData(scenario, nil, json.RawMessage(`null`), capture, false)
	if !verification.Verified || verification.CheckedClaim != "provider objectives" {
		t.Fatalf("provider transcript verification = %+v, want verified", verification)
	}
	wrong := scenario
	wrong.Expectations = []probe.ScenarioV2Expectation{{Type: probe.ScenarioV2ExpectationTranscriptContains, Text: "missing-secret"}}
	failed := VerifyEvidenceData(wrong, nil, json.RawMessage(`null`), capture, false)
	if failed.Verified || !strings.Contains(failed.Error, "transcript_contains") || strings.Contains(failed.Error, "missing-secret") || strings.Contains(failed.Error, "expected-secret-transcript") {
		t.Fatalf("safe provider transcript divergence = %+v", failed)
	}
}

func TestVerifierMatchesStaleToolReference(t *testing.T) {
	toolRef := "webmcp.tool-ref.v1:AAAAAAAAAAAAAAAAAAAAAA"
	scenario := probe.ScenarioV2{
		SchemaVersion:  probe.ScenarioV2Version,
		ID:             "stale-ref-objective",
		BrowserFixture: fixtureBrowserPath,
		Expectations: []probe.ScenarioV2Expectation{{
			Type:    probe.ScenarioV2ExpectationStaleToolRejected,
			ToolRef: toolRef,
		}},
	}
	events := []testkit.Event{{
		Version:    testkit.BrowserEventsVersion,
		Sequence:   1,
		BrowserID:  "fixture-browser",
		TargetID:   "tab-1",
		Generation: 2,
		Type:       testkit.EventBrowserInvocationError,
		Payload:    testkit.MustJSONValue(map[string]any{"code": string(webmcp.ErrorStaleToolRef), "tool_ref": toolRef}),
		Redaction:  testkit.RedactionMetadata{Mode: testkit.RedactionNone},
	}}
	verification := VerifyEvidenceData(scenario, events, json.RawMessage(`{}`), runtimeReplay.CaptureProbeObservation{}, true)
	if !verification.Verified {
		t.Fatalf("matching stale reference verification = %+v, want verified", verification)
	}
	wrong := scenario
	wrong.Expectations = []probe.ScenarioV2Expectation{{Type: probe.ScenarioV2ExpectationStaleToolRejected, ToolRef: "webmcp.tool-ref.v1:BBBBBBBBBBBBBBBBBBBBBB"}}
	failed := VerifyEvidenceData(wrong, events, json.RawMessage(`{}`), runtimeReplay.CaptureProbeObservation{}, true)
	if failed.Verified || !strings.Contains(failed.Error, "stale_tool_rejected") || !strings.Contains(failed.Error, "stale_tool_ref") {
		t.Fatalf("mismatched stale reference verification = %+v", failed)
	}
}

func TestSafeSummariesNeverEchoContent(t *testing.T) {
	cases := map[string]string{
		`{"a":1,"b":2}`: "object(fields=2)",
		`[1,2,3]`:       "array(items=3)",
		`"secret"`:      "string",
		`12`:            "number",
		`true`:          "boolean",
		`null`:          "null",
		`{} {}`:         invalidJSONLabel,
		``:              Missing,
	}
	for raw, want := range cases {
		if got := SafeJSON(json.RawMessage(raw)); got != want {
			t.Fatalf("SafeJSON(%s) = %q, want %q", raw, got, want)
		}
	}
	if got := SafeURL("https://user:pass@example.test/path?token=x#frag"); got != "https://example.test/path" {
		t.Fatalf("SafeURL = %q", got)
	}
	if got := SafeError(errors.Join(errors.New("detail"), webmcp.ErrStaleToolRef)); got != string(webmcp.ErrorStaleToolRef) {
		t.Fatalf("SafeError(stale) = %q", got)
	}
	if got := SafeError(errors.New("secret detail")); got != "observation_error" {
		t.Fatalf("SafeError(other) = %q", got)
	}
}

func TestJSONPathValueAndSemanticEquality(t *testing.T) {
	raw := json.RawMessage(`{"a":{"b":[1,{"c":9007199254740993}]}}`)
	value, err := JSONPathValue(raw, "$.a")
	if err != nil || !SemanticJSONEqual(value, json.RawMessage(`{"b":[1,{"c":9007199254740993}]}`)) {
		t.Fatalf("JSONPathValue($.a) = %s, %v", value, err)
	}
	if SemanticJSONEqual(value, json.RawMessage(`{"b":[1,{"c":9007199254740992}]}`)) {
		t.Fatal("semantic equality lost large integer precision")
	}
	for _, path := range []string{"a", "$.missing", "$.a.b.c", "$..a"} {
		if _, err := JSONPathValue(raw, path); err == nil {
			t.Fatalf("JSONPathValue(%q) succeeded", path)
		}
	}
}
