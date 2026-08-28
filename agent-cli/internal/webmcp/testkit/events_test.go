package testkit

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestCanonicalBrowserEventsGolden(t *testing.T) {
	got, err := MarshalEvents(goldenEvents())
	if err != nil {
		t.Fatalf("MarshalEvents: %v", err)
	}
	want, err := os.ReadFile("testdata/browser-events.golden.jsonl")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("canonical browser events differ from golden:\n got: %s\nwant: %s", got, want)
	}
	loaded, err := ValidateEventStream(got)
	if err != nil {
		t.Fatalf("ValidateEventStream: %v", err)
	}
	if len(loaded) != len(goldenEvents()) {
		t.Fatalf("loaded %d events, want %d", len(loaded), len(goldenEvents()))
	}
	if loaded[7].Payload == nil || !bytes.Contains(loaded[7].Payload, []byte(`"input_schema"`)) {
		t.Fatal("tool schema was not retained as JSON data")
	}
	if !bytes.Contains(loaded[12].Payload, []byte("9007199254740993")) {
		t.Fatal("large page-owned integer was changed")
	}
}

func TestRecorderInjectedClockAndIDsAreByteStable(t *testing.T) {
	record := func() []byte {
		var output bytes.Buffer
		clock := NewFakeClock(100)
		recorder, err := NewRecorder(&output, WithClock(clock), WithIDSource(NewDeterministicIDSource("golden")))
		if err != nil {
			t.Fatalf("NewRecorder: %v", err)
		}
		invocationID, err := recorder.NewID("invocation")
		if err != nil {
			t.Fatalf("NewID: %v", err)
		}
		if _, err := recorder.Record(EventInput{
			Type:       EventBrowserInvocationCreated,
			BrowserID:  "browser-1",
			TargetID:   "tab-1",
			Generation: 3,
			Payload:    MustJSONValue(json.RawMessage(`{"invocation_id":"` + invocationID + `"}`)),
		}); err != nil {
			t.Fatalf("Record created: %v", err)
		}
		clock.Advance(25)
		if _, err := recorder.Record(EventInput{
			Type:       EventBrowserInvocationCompleted,
			BrowserID:  "browser-1",
			TargetID:   "tab-1",
			Generation: 3,
			Payload:    MustJSONValue(json.RawMessage(`{"invocation_id":"` + invocationID + `","status":"completed","output":{"count":9007199254740993}}`)),
		}); err != nil {
			t.Fatalf("Record completed: %v", err)
		}
		return output.Bytes()
	}

	first := record()
	second := record()
	if !bytes.Equal(first, second) {
		t.Fatalf("equivalent recordings are not byte stable:\nfirst: %s\nsecond: %s", first, second)
	}
	if _, err := ValidateEventStream(first); err != nil {
		t.Fatalf("stable recording does not validate: %v", err)
	}
}

func TestDigestOnlyEventRoundTripsThroughRecorderAndRedactor(t *testing.T) {
	digest := strings.Repeat("a", 64)
	input := EventInput{
		Type:          EventBrowserDiscoveryStarted,
		PayloadSHA256: digest,
	}

	var recorded bytes.Buffer
	recorder, err := NewRecorder(&recorded, WithRedaction(RedactionPolicy{}))
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	if _, err := recorder.Record(input); err != nil {
		t.Fatalf("Record digest-only event: %v", err)
	}
	if !bytes.Contains(recorded.Bytes(), []byte(`"payload_sha256":"`+digest+`"`)) {
		t.Fatalf("recorded digest-only event omitted digest: %s", recorded.Bytes())
	}

	loaded, err := ValidateEventStream(recorded.Bytes())
	if err != nil {
		t.Fatalf("ValidateEventStream recorded digest-only event: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Payload != nil || loaded[0].PayloadSHA256 != digest {
		t.Fatalf("loaded digest-only event = %#v", loaded)
	}

	redactor, err := NewRedactor(RedactionPolicy{})
	if err != nil {
		t.Fatalf("NewRedactor: %v", err)
	}
	redacted, err := redactor.MarshalEvents(loaded)
	if err != nil {
		t.Fatalf("MarshalEvents digest-only event: %v", err)
	}
	if !bytes.Equal(recorded.Bytes(), redacted) {
		t.Fatalf("redactor changed digest-only event:\nrecorded: %s\nredacted: %s", recorded.Bytes(), redacted)
	}
}

func TestRecorderDoesNotAdvanceAfterValidationOrClockFailure(t *testing.T) {
	var output bytes.Buffer
	clock := NewFakeClock(10)
	recorder, err := NewRecorder(&output, WithClock(clock))
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	if _, err := recorder.Record(EventInput{Type: EventBrowserDiscoveryStarted}); err == nil {
		t.Fatal("missing payload unexpectedly recorded")
	}
	if output.Len() != 0 {
		t.Fatal("failed validation wrote bytes")
	}
	if _, err := recorder.Record(EventInput{
		Type:    EventBrowserDiscoveryStarted,
		Payload: MustJSONValue(map[string]any{"source": "fixture"}),
	}); err != nil {
		t.Fatalf("Record valid event: %v", err)
	}
	clock.Set(9)
	if _, err := recorder.Record(EventInput{
		Type:    EventBrowserDiscoveryStarted,
		Payload: MustJSONValue(map[string]any{"source": "fixture"}),
	}); !errors.Is(err, ErrRecorderClock) {
		t.Fatalf("backwards clock error = %v, want ErrRecorderClock", err)
	}
	clock.Set(10)
	if _, err := recorder.Record(EventInput{
		Type:    EventBrowserDiscoveryStarted,
		Payload: MustJSONValue(map[string]any{"source": "fixture"}),
	}); err != nil {
		t.Fatalf("Record after failed clock check: %v", err)
	}
	events, err := ValidateEventStream(output.Bytes())
	if err != nil {
		t.Fatalf("ValidateEventStream: %v", err)
	}
	if len(events) != 2 || events[1].Sequence != 2 {
		t.Fatalf("recording cursor after failures = %#v", events)
	}
}

func TestBrowserEventValidationRejectsMalformedStreams(t *testing.T) {
	base, err := MarshalEvents(goldenEvents()[:2])
	if err != nil {
		t.Fatalf("MarshalEvents: %v", err)
	}
	firstLine, secondLine := splitTwoLines(t, base)
	tests := []struct {
		name string
		data []byte
	}{
		{name: "unknown version", data: []byte(strings.Replace(string(firstLine), BrowserEventsVersion, "webmcp.browser-events.v9", 1) + "\n" + string(secondLine) + "\n")},
		{name: "unknown top-level field", data: []byte(strings.TrimSuffix(string(firstLine), "}") + `,"extra":true}` + "\n" + string(secondLine) + "\n")},
		{name: "duplicate top-level field", data: []byte(strings.Replace(string(firstLine), `"sequence":1`, `"sequence":1,"sequence":1`, 1) + "\n" + string(secondLine) + "\n")},
		{name: "unknown event type", data: []byte(strings.Replace(string(firstLine), string(EventBrowserDiscoveryStarted), "browser.unknown", 1) + "\n" + string(secondLine) + "\n")},
		{name: "non-contiguous sequence", data: []byte(string(firstLine) + "\n" + strings.Replace(string(secondLine), `"sequence":2`, `"sequence":3`, 1) + "\n")},
		{name: "decreasing clock", data: []byte(strings.Replace(string(firstLine), `"monotonic_ms":0`, `"monotonic_ms":1`, 1) + "\n" + strings.Replace(string(secondLine), `"monotonic_ms":1`, `"monotonic_ms":0`, 1) + "\n")},
		{name: "both payload and digest", data: []byte(strings.TrimSuffix(string(firstLine), "}") + `,"payload_sha256":"` + strings.Repeat("a", 64) + `"}` + "\n" + string(secondLine) + "\n")},
		{name: "neither payload nor digest", data: []byte(strings.Replace(string(firstLine), `,"payload":{"source":"devtools_http","attempt":1}`, "", 1) + "\n" + string(secondLine) + "\n")},
		{name: "invalid digest", data: []byte(strings.TrimSuffix(string(firstLine), "}") + `,"payload_sha256":"ABC"}` + "\n" + string(secondLine) + "\n")},
		{name: "missing target context", data: []byte(strings.Replace(string(goldenLine(t, 7)), `,"target_id":"tab-1"`, "", 1) + "\n")},
		{name: "unknown payload control", data: []byte(strings.TrimSuffix(string(firstLine), "}") + `,"payload":{"source":"fixture","unexpected":true}}` + "\n")},
		{name: "invalid redaction rule", data: []byte(strings.Replace(string(firstLine), `{"mode":"none"}`, `{"mode":"redacted","rules":["secret"]}`, 1) + "\n")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ValidateEventStream(test.data); !errors.Is(err, ErrInvalidBrowserEvent) {
				t.Fatalf("ValidateEventStream error = %v, want ErrInvalidBrowserEvent", err)
			}
		})
	}
}

func TestEventContextRulesAndRedactionNormalization(t *testing.T) {
	valid := Event{
		Version:     BrowserEventsVersion,
		Sequence:    1,
		MonotonicMS: 1,
		Type:        EventBrowserWebMCPEnabled,
		BrowserID:   "browser-1",
		TargetID:    "tab-1",
		Generation:  0,
		Payload:     MustJSONValue(map[string]any{"enabled": true}),
		Redaction: RedactionMetadata{
			Mode:  RedactionRedacted,
			Rules: []string{RedactionRuleRawCDPDisabled, RedactionRuleURLFragment},
		},
	}
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"generation":0`) {
		t.Fatalf("required zero generation was omitted: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"rules":["url_fragment","raw_cdp_disabled"]`) {
		t.Fatalf("redaction rules were not normalized: %s", encoded)
	}
	var decoded Event
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if decoded.Generation != 0 || len(decoded.Redaction.Rules) != 2 {
		t.Fatalf("decoded event lost zero generation or rules: %#v", decoded)
	}

	invalidContext := valid
	invalidContext.TargetID = ""
	if err := invalidContext.Validate(); !errors.Is(err, ErrInvalidBrowserEvent) {
		t.Fatalf("missing target validation error = %v", err)
	}
	invalidContext = valid
	invalidContext.BrowserID = "https://user:secret@example.test"
	if err := invalidContext.Validate(); !errors.Is(err, ErrInvalidBrowserEvent) {
		t.Fatalf("URL-shaped ID validation error = %v", err)
	}
	invalidContext = Event{
		Version:   BrowserEventsVersion,
		Sequence:  1,
		Type:      EventBrowserDiscoveryStarted,
		BrowserID: "browser-1",
		Payload:   MustJSONValue(map[string]any{}),
		Redaction: RedactionMetadata{Mode: RedactionNone},
	}
	if err := invalidContext.Validate(); !errors.Is(err, ErrInvalidBrowserEvent) {
		t.Fatalf("unexpected browser context validation error = %v", err)
	}
}

func TestLoadEventsAndIDSource(t *testing.T) {
	data, err := MarshalEvents(goldenEvents()[:1])
	if err != nil {
		t.Fatalf("MarshalEvents: %v", err)
	}
	loaded, err := LoadEvents(bytes.NewReader(data))
	if err != nil || len(loaded) != 1 {
		t.Fatalf("LoadEvents = %#v, %v", loaded, err)
	}
	if _, err := LoadEvents(nil); !errors.Is(err, ErrInvalidBrowserEvent) {
		t.Fatalf("nil reader error = %v", err)
	}
	if _, err := NewRecorder(nil); !errors.Is(err, ErrRecorderWrite) {
		t.Fatalf("nil writer error = %v", err)
	}
	if _, err := NewRecorder(&bytes.Buffer{}); err != nil {
		t.Fatalf("default recorder: %v", err)
	}
	var recorder Recorder
	if _, err := recorder.NewID("invocation"); !errors.Is(err, ErrIDSourceUnavailable) {
		t.Fatalf("nil recorder ID error = %v", err)
	}
	ids := NewFakeIDs("demo")
	if got := ids.NextID("tool"); got != "demo-tool-001" {
		t.Fatalf("first deterministic ID = %q", got)
	}
	if got := ids.NextID("bad kind"); got != "demo-badkind-002" {
		t.Fatalf("normalized deterministic ID = %q", got)
	}
}

func goldenEvents() []Event {
	redaction := RedactionMetadata{Mode: RedactionNone}
	return []Event{
		{Version: BrowserEventsVersion, Sequence: 1, MonotonicMS: 0, Type: EventBrowserDiscoveryStarted, Payload: json.RawMessage(`{"source":"devtools_http","attempt":1}`), Redaction: redaction},
		{Version: BrowserEventsVersion, Sequence: 2, MonotonicMS: 1, Type: EventBrowserDiscoveryCompleted, BrowserID: "chrome-local-1", Payload: json.RawMessage(`{"candidate_count":1,"candidates":[{"id":"chrome-local-1"}]}`), Redaction: redaction},
		{Version: BrowserEventsVersion, Sequence: 3, MonotonicMS: 2, Type: EventBrowserEndpointVersion, BrowserID: "chrome-local-1", Payload: json.RawMessage(`{"Browser":"Chrome/Test","Protocol-Version":"1.3"}`), Redaction: redaction},
		{Version: BrowserEventsVersion, Sequence: 4, MonotonicMS: 3, Type: EventBrowserTargetsSnapshot, BrowserID: "chrome-local-1", Payload: json.RawMessage(`{"targets":[{"id":"tab-1","type":"page","url":"https://fixture.test/"}]}`), Redaction: redaction},
		{Version: BrowserEventsVersion, Sequence: 5, MonotonicMS: 4, Type: EventBrowserTargetSelected, BrowserID: "chrome-local-1", TargetID: "tab-1", Payload: json.RawMessage(`{"generation":1,"reason":"explicit"}`), Redaction: redaction},
		{Version: BrowserEventsVersion, Sequence: 6, MonotonicMS: 5, Type: EventBrowserChromeTargetAttached, BrowserID: "chrome-local-1", TargetID: "tab-1", Payload: json.RawMessage(`{"ownership_mode":"external","phase":"attached"}`), Redaction: redaction},
		{Version: BrowserEventsVersion, Sequence: 7, MonotonicMS: 6, Type: EventBrowserWebMCPEnabled, BrowserID: "chrome-local-1", TargetID: "tab-1", Generation: 1, Payload: json.RawMessage(`{"enabled":true}`), Redaction: redaction},
		{Version: BrowserEventsVersion, Sequence: 8, MonotonicMS: 7, Type: EventBrowserCatalogToolAdded, BrowserID: "chrome-local-1", TargetID: "tab-1", Generation: 1, Payload: json.RawMessage(`{"tools":[{"name":"read_state","input_schema":{"properties":{"limit":{"type":"integer"}},"type":"object"}}]}`), Redaction: redaction},
		{Version: BrowserEventsVersion, Sequence: 9, MonotonicMS: 8, Type: EventBrowserCatalogToolRemoved, BrowserID: "chrome-local-1", TargetID: "tab-1", Generation: 1, Payload: json.RawMessage(`{"tool_refs":["webmcp.tool-ref.v1:AAECAwQFBgcICQoLDA0ODw"]}`), Redaction: redaction},
		{Version: BrowserEventsVersion, Sequence: 10, MonotonicMS: 9, Type: EventBrowserCatalogReady, BrowserID: "chrome-local-1", TargetID: "tab-1", Generation: 1, Payload: json.RawMessage(`{"schema_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","tool_count":1}`), Redaction: redaction},
		{Version: BrowserEventsVersion, Sequence: 11, MonotonicMS: 10, Type: EventBrowserInvocationCreated, BrowserID: "chrome-local-1", TargetID: "tab-1", Generation: 1, Payload: json.RawMessage(`{"invocation_id":"inv-1","tool_ref":"webmcp.tool-ref.v1:AAECAwQFBgcICQoLDA0ODw"}`), Redaction: redaction},
		{Version: BrowserEventsVersion, Sequence: 12, MonotonicMS: 11, Type: EventBrowserInvocationApproval, BrowserID: "chrome-local-1", TargetID: "tab-1", Generation: 1, Payload: json.RawMessage(`{"approved":true,"decision":"approved","invocation_id":"inv-1"}`), Redaction: redaction},
		{Version: BrowserEventsVersion, Sequence: 13, MonotonicMS: 12, Type: EventBrowserInvocationDispatched, BrowserID: "chrome-local-1", TargetID: "tab-1", Generation: 1, Payload: json.RawMessage(`{"input":{"n":9007199254740993},"input_sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","invocation_id":"inv-1","tool_ref":"webmcp.tool-ref.v1:AAECAwQFBgcICQoLDA0ODw"}`), Redaction: RedactionMetadata{Mode: RedactionNone, Rules: []string{RedactionRuleRawCDPDisabled}}},
		{Version: BrowserEventsVersion, Sequence: 14, MonotonicMS: 13, Type: EventBrowserInvocationCompleted, BrowserID: "chrome-local-1", TargetID: "tab-1", Generation: 1, Payload: json.RawMessage(`{"invocation_id":"inv-1","output":{"count":9007199254740993},"status":"completed"}`), Redaction: redaction},
		{Version: BrowserEventsVersion, Sequence: 15, MonotonicMS: 14, Type: EventBrowserInvocationError, BrowserID: "chrome-local-1", TargetID: "tab-1", Generation: 1, Payload: json.RawMessage(`{"code":"browser_disconnected","invocation_id":"inv-1"}`), Redaction: redaction},
		{Version: BrowserEventsVersion, Sequence: 16, MonotonicMS: 15, Type: EventBrowserInvocationCancel, BrowserID: "chrome-local-1", TargetID: "tab-1", Generation: 1, Payload: json.RawMessage(`{"invocation_id":"inv-1","source":"user"}`), Redaction: redaction},
		{Version: BrowserEventsVersion, Sequence: 17, MonotonicMS: 16, Type: EventBrowserInvocationCanceled, BrowserID: "chrome-local-1", TargetID: "tab-1", Generation: 1, Payload: json.RawMessage(`{"invocation_id":"inv-1","source":"user"}`), Redaction: redaction},
		{Version: BrowserEventsVersion, Sequence: 18, MonotonicMS: 17, Type: EventBrowserPageGenerationChanged, BrowserID: "chrome-local-1", TargetID: "tab-1", Payload: json.RawMessage(`{"current_generation":2,"previous_generation":1,"reason":"navigation"}`), Redaction: redaction},
		{Version: BrowserEventsVersion, Sequence: 19, MonotonicMS: 18, Type: EventBrowserTargetDetached, BrowserID: "chrome-local-1", TargetID: "tab-1", Payload: json.RawMessage(`{"ownership_mode":"external","reason":"session_closed"}`), Redaction: redaction},
		{Version: BrowserEventsVersion, Sequence: 20, MonotonicMS: 19, Type: EventBrowserChromeTargetClosed, BrowserID: "chrome-local-1", TargetID: "tab-1", Payload: json.RawMessage(`{"reason":"fixture_complete"}`), Redaction: redaction},
	}
}

func splitTwoLines(t *testing.T, data []byte) ([]byte, []byte) {
	t.Helper()
	lines := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	return lines[0], lines[1]
}

func goldenLine(t *testing.T, line int) []byte {
	t.Helper()
	data, err := MarshalEvents(goldenEvents())
	if err != nil {
		t.Fatalf("MarshalEvents: %v", err)
	}
	lines := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})
	return lines[line-1]
}
