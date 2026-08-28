package testkit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRedactionPolicyHasStrictDeterministicWireShape(t *testing.T) {
	policy := RedactionPolicy{
		URLQuery:           true,
		ToolArguments:      []string{"write_secret"},
		ResultJSONPointers: []string{"/token"},
		DigestTools:        nil,
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		t.Fatalf("marshal policy: %v", err)
	}
	want := `{"url_query":true,"url_fragment":false,"tool_arguments":["write_secret"],"result_json_pointers":["/token"],"digest_tools":[],"raw_cdp":false}`
	if string(encoded) != want {
		t.Fatalf("policy JSON = %s, want %s", encoded, want)
	}

	var decoded RedactionPolicy
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal policy: %v", err)
	}
	if decoded.URLQuery != policy.URLQuery || decoded.URLFragment != policy.URLFragment || decoded.RawCDP {
		t.Fatalf("decoded policy flags = %#v", decoded)
	}
	if len(decoded.DigestTools) != 0 || decoded.ToolArguments[0] != "write_secret" {
		t.Fatalf("decoded policy lists = %#v", decoded)
	}

	invalid := []string{
		`{"url_query":true,"url_fragment":false,"tool_arguments":[],"result_json_pointers":["token"],"digest_tools":[],"raw_cdp":false}`,
		`{"url_query":true,"url_fragment":false,"tool_arguments":[],"result_json_pointers":["/bad~2escape"],"digest_tools":[],"raw_cdp":false}`,
		`{"url_query":true,"url_fragment":false,"tool_arguments":["write_secret","write_secret"],"result_json_pointers":[],"digest_tools":[],"raw_cdp":false}`,
		`{"url_query":true,"url_fragment":false,"tool_arguments":[],"result_json_pointers":[],"digest_tools":[],"raw_cdp":false,"extra":true}`,
		`{"url_query":true,"url_fragment":false,"tool_arguments":null,"result_json_pointers":[],"digest_tools":[],"raw_cdp":false}`,
	}
	for _, data := range invalid {
		t.Run(data, func(t *testing.T) {
			var got RedactionPolicy
			if err := json.Unmarshal([]byte(data), &got); !errors.Is(err, ErrInvalidRedactionPolicy) {
				t.Fatalf("unmarshal error = %v, want ErrInvalidRedactionPolicy", err)
			}
		})
	}

	rawDiagnostics := policy
	rawDiagnostics.RawCDP = true
	if err := rawDiagnostics.Validate(); err != nil {
		t.Fatalf("diagnostic policy should have a valid general shape: %v", err)
	}
	if _, err := NewRedactor(rawDiagnostics); !errors.Is(err, ErrRawCDPNotAllowed) {
		t.Fatalf("canonical raw CDP error = %v, want ErrRawCDPNotAllowed", err)
	}
}

func TestRedactorSanitizesURLsCredentialsArgumentsAndResultPointers(t *testing.T) {
	const secret = "webmcp-sentinel-credential-20260828"
	policy := RedactionPolicy{
		URLQuery:           true,
		URLFragment:        true,
		ToolArguments:      []string{"write_secret"},
		ResultJSONPointers: []string{"/nested/token", "/nested/a~1b/~0key/2", "/missing"},
	}
	events := []Event{
		{
			Version:   BrowserEventsVersion,
			Sequence:  1,
			Type:      EventBrowserTargetsSnapshot,
			BrowserID: "browser-1",
			Payload:   MustJSONValue(json.RawMessage(`{"targets":[{"id":"tab-1","url":"https://operator:` + secret + `@fixture.test/path?token=` + secret + `#private-fragment","title":"` + secret + `"}]} `)),
			Redaction: RedactionMetadata{Mode: RedactionNone},
		},
		{
			Version:     BrowserEventsVersion,
			Sequence:    2,
			MonotonicMS: 1,
			Type:        EventBrowserInvocationCreated,
			BrowserID:   "browser-1",
			TargetID:    "tab-1",
			Generation:  7,
			Payload:     MustJSONValue(json.RawMessage(`{"invocation_id":"inv-1","tool_name":"write_secret","tool_ref":"ref-1"}`)),
			Redaction:   RedactionMetadata{Mode: RedactionNone},
		},
		{
			Version:     BrowserEventsVersion,
			Sequence:    3,
			MonotonicMS: 2,
			Type:        EventBrowserInvocationDispatched,
			BrowserID:   "browser-1",
			TargetID:    "tab-1",
			Generation:  7,
			Payload:     MustJSONValue(json.RawMessage(`{"invocation_id":"inv-1","tool_ref":"ref-1","input":{"amount":9007199254740993,"secret":"` + secret + `"}}`)),
			Redaction:   RedactionMetadata{Mode: RedactionNone},
		},
		{
			Version:     BrowserEventsVersion,
			Sequence:    4,
			MonotonicMS: 3,
			Type:        EventBrowserInvocationCompleted,
			BrowserID:   "browser-1",
			TargetID:    "tab-1",
			Generation:  7,
			Payload:     MustJSONValue(json.RawMessage(`{"invocation_id":"inv-1","status":"completed","output":{"nested":{"token":"` + secret + `","a/b":{"~key":["keep",9007199254740993,"` + secret + `"]}},"large":9007199254740993}}`)),
			Redaction:   RedactionMetadata{Mode: RedactionNone},
		},
	}

	redactor, err := NewRedactor(policy, []string{secret})
	if err != nil {
		t.Fatalf("NewRedactor: %v", err)
	}
	redacted, err := redactor.RedactEvents(events)
	if err != nil {
		t.Fatalf("RedactEvents: %v", err)
	}

	var targetPayload struct {
		Targets []struct {
			URL   string `json:"url"`
			Title string `json:"title"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(redacted[0].Payload, &targetPayload); err != nil {
		t.Fatalf("decode target payload: %v", err)
	}
	if got := targetPayload.Targets[0].URL; got != "https://operator:REDACTED@fixture.test/path" {
		t.Fatalf("redacted target URL = %q", got)
	}
	if targetPayload.Targets[0].Title != RedactionMarker {
		t.Fatalf("redacted target title = %q", targetPayload.Targets[0].Title)
	}
	if redacted[0].Redaction.Mode != RedactionRedacted || !sameStrings(redacted[0].Redaction.Rules, []string{RedactionRuleURLQuery, RedactionRuleURLFragment, RedactionRuleRawCDPDisabled}) {
		t.Fatalf("target redaction metadata = %#v", redacted[0].Redaction)
	}

	var dispatched map[string]json.RawMessage
	if err := json.Unmarshal(redacted[2].Payload, &dispatched); err != nil {
		t.Fatalf("decode dispatched payload: %v", err)
	}
	if string(dispatched["input"]) != `"REDACTED"` {
		t.Fatalf("redacted invocation input = %s", dispatched["input"])
	}
	if redacted[2].Redaction.Mode != RedactionRedacted || !sameStrings(redacted[2].Redaction.Rules, []string{RedactionRuleToolArguments, RedactionRuleRawCDPDisabled}) {
		t.Fatalf("argument redaction metadata = %#v", redacted[2].Redaction)
	}

	var completed map[string]json.RawMessage
	if err := json.Unmarshal(redacted[3].Payload, &completed); err != nil {
		t.Fatalf("decode completed payload: %v", err)
	}
	var output map[string]json.RawMessage
	if err := json.Unmarshal(completed["output"], &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	var nested map[string]json.RawMessage
	if err := json.Unmarshal(output["nested"], &nested); err != nil {
		t.Fatalf("decode nested output: %v", err)
	}
	if string(nested["token"]) != `"REDACTED"` {
		t.Fatalf("pointer-redacted token = %s", nested["token"])
	}
	var escaped map[string]json.RawMessage
	if err := json.Unmarshal(nested["a/b"], &escaped); err != nil {
		t.Fatalf("decode escaped pointer object: %v", err)
	}
	var values []json.RawMessage
	if err := json.Unmarshal(escaped["~key"], &values); err != nil {
		t.Fatalf("decode escaped pointer array: %v", err)
	}
	if string(values[0]) != `"keep"` || string(values[1]) != `9007199254740993` || string(values[2]) != `"REDACTED"` {
		t.Fatalf("escaped pointer result = %s", values)
	}
	if string(output["large"]) != `9007199254740993` {
		t.Fatalf("large output number changed = %s", output["large"])
	}
	if redacted[3].Redaction.Mode != RedactionRedacted || !sameStrings(redacted[3].Redaction.Rules, []string{RedactionRuleResultJSONPointers, RedactionRuleRawCDPDisabled}) {
		t.Fatalf("result redaction metadata = %#v", redacted[3].Redaction)
	}

	first, digest, err := redactor.HashEvents(events)
	if err != nil {
		t.Fatalf("HashEvents: %v", err)
	}
	second, secondDigest, err := redactor.HashEvents(events)
	if err != nil {
		t.Fatalf("second HashEvents: %v", err)
	}
	if !bytes.Equal(first, second) || digest != secondDigest {
		t.Fatalf("redacted artifact is not byte stable: %q vs %q", digest, secondDigest)
	}
	if !strings.Contains(string(first), `"REDACTED"`) || bytes.Contains(first, []byte(secret)) {
		t.Fatalf("final artifact did not contain only safe redaction: %s", first)
	}
	decoded, err := ValidateEventStream(first)
	if err != nil {
		t.Fatalf("redacted artifact failed event validation: %v", err)
	}
	if len(decoded) != len(events) {
		t.Fatalf("redacted artifact has %d events, want %d", len(decoded), len(events))
	}
}

func TestRedactorDigestToolsRetainOnlyLowercaseResultAndInputDigests(t *testing.T) {
	policy := RedactionPolicy{DigestTools: []string{"hash_tool"}}
	events := []Event{
		{
			Version: BrowserEventsVersion, Sequence: 1, Type: EventBrowserInvocationCreated,
			BrowserID: "browser-1", TargetID: "tab-1", Generation: 1,
			Payload:   MustJSONValue(json.RawMessage(`{"invocation_id":"inv-1","tool_name":"hash_tool","tool_ref":"ref-1"}`)),
			Redaction: RedactionMetadata{Mode: RedactionNone},
		},
		{
			Version: BrowserEventsVersion, Sequence: 2, MonotonicMS: 1, Type: EventBrowserInvocationDispatched,
			BrowserID: "browser-1", TargetID: "tab-1", Generation: 1,
			Payload:   MustJSONValue(json.RawMessage(`{"invocation_id":"inv-1","tool_ref":"ref-1","input":{"count":9007199254740993}}`)),
			Redaction: RedactionMetadata{Mode: RedactionNone},
		},
		{
			Version: BrowserEventsVersion, Sequence: 3, MonotonicMS: 2, Type: EventBrowserInvocationCompleted,
			BrowserID: "browser-1", TargetID: "tab-1", Generation: 1,
			Payload:   MustJSONValue(json.RawMessage(`{"invocation_id":"inv-1","status":"completed","output":{"count":9007199254740993}}`)),
			Redaction: RedactionMetadata{Mode: RedactionNone},
		},
	}
	redacted, err := RedactEvents(events, policy)
	if err != nil {
		t.Fatalf("RedactEvents: %v", err)
	}
	var dispatched map[string]json.RawMessage
	var completed map[string]json.RawMessage
	if err := json.Unmarshal(redacted[1].Payload, &dispatched); err != nil {
		t.Fatalf("decode dispatched: %v", err)
	}
	if err := json.Unmarshal(redacted[2].Payload, &completed); err != nil {
		t.Fatalf("decode completed: %v", err)
	}
	if _, ok := dispatched["input"]; ok {
		t.Fatal("digest-only input was retained")
	}
	if _, ok := completed["output"]; ok {
		t.Fatal("digest-only output was retained")
	}
	inputDigest := sha256.Sum256([]byte(`{"count":9007199254740993}`))
	outputDigest := sha256.Sum256([]byte(`{"count":9007199254740993}`))
	if string(dispatched["input_sha256"]) != `"`+hex.EncodeToString(inputDigest[:])+`"` {
		t.Fatalf("input digest = %s", dispatched["input_sha256"])
	}
	if string(completed["output_sha256"]) != `"`+hex.EncodeToString(outputDigest[:])+`"` {
		t.Fatalf("output digest = %s", completed["output_sha256"])
	}
	if redacted[1].Redaction.Mode != RedactionDigest || redacted[2].Redaction.Mode != RedactionDigest {
		t.Fatalf("digest modes = %q/%q", redacted[1].Redaction.Mode, redacted[2].Redaction.Mode)
	}
	artifact, err := MarshalRedactedEvents(events, policy)
	if err != nil {
		t.Fatalf("MarshalRedactedEvents: %v", err)
	}
	if bytes.Contains(artifact, []byte(`"count"`)) {
		t.Fatalf("digest-only artifact retained result data: %s", artifact)
	}
	if _, err := ValidateEventStream(artifact); err != nil {
		t.Fatalf("digest-only artifact failed validation: %v", err)
	}
}

func TestRedactorRejectsSurvivingCredentialsAndRawCDP(t *testing.T) {
	const secret = "redaction-survivor-sentinel-20260828"
	redactor, err := NewRedactor(RedactionPolicy{}, []string{secret})
	if err != nil {
		t.Fatalf("NewRedactor: %v", err)
	}
	for name, check := range map[string]func() error{
		"artifact": func() error { return redactor.ValidateArtifactBytes([]byte("prefix=" + secret)) },
		"metadata": func() error { return EnsureNoConfiguredCredentials([]byte("metadata="+secret), []string{secret}) },
	} {
		t.Run(name, func(t *testing.T) {
			err := check()
			if !errors.Is(err, ErrRedactionCredentialSurvived) {
				t.Fatalf("error = %v, want ErrRedactionCredentialSurvived", err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("credential leaked in error: %v", err)
			}
		})
	}

	if _, err := NewRedactor(RedactionPolicy{}, []string{RedactionMarker}); !errors.Is(err, ErrInvalidRedactionCredential) {
		t.Fatalf("marker credential error = %v, want ErrInvalidRedactionCredential", err)
	}
	rawCDPEvent := Event{
		Version:   BrowserEventsVersion,
		Sequence:  1,
		Type:      EventBrowserDiscoveryStarted,
		Payload:   MustJSONValue(json.RawMessage(`{"source":"fixture","raw_cdp":{"method":"Runtime.evaluate"}}`)),
		Redaction: RedactionMetadata{Mode: RedactionNone},
	}
	if _, err := redactor.RedactEvent(rawCDPEvent); !errors.Is(err, ErrRawCDPDetected) {
		t.Fatalf("raw CDP error = %v, want ErrRawCDPDetected", err)
	}

	diagnostic, err := RedactRawDiagnostics([]byte("frame="+secret), []string{secret})
	if err != nil {
		t.Fatalf("RedactRawDiagnostics: %v", err)
	}
	if string(diagnostic) != "frame="+RedactionMarker || bytes.Contains(diagnostic, []byte(secret)) {
		t.Fatalf("diagnostic redaction = %q", diagnostic)
	}
}

func TestRecorderAppliesRedactionBeforeWriter(t *testing.T) {
	const secret = "recorder-redaction-sentinel-20260828"
	var output bytes.Buffer
	recorder, err := NewRecorder(&output, WithRedaction(RedactionPolicy{URLQuery: true}, []string{secret}))
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	if _, err := recorder.Record(EventInput{
		Type:      EventBrowserTargetsSnapshot,
		BrowserID: "browser-1",
		Payload:   MustJSONValue(json.RawMessage(`{"targets":[{"id":"tab-1","url":"https://fixture.test/?secret=` + secret + `"}]}`)),
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if bytes.Contains(output.Bytes(), []byte(secret)) || bytes.Contains(output.Bytes(), []byte("?secret=")) {
		t.Fatalf("writer saw unsanitized event: %s", output.Bytes())
	}
	if _, err := ValidateEventStream(output.Bytes()); err != nil {
		t.Fatalf("recorded redacted event failed validation: %v", err)
	}

	rawPolicy := RedactionPolicy{RawCDP: true}
	if _, err := NewRecorder(&bytes.Buffer{}, WithRedaction(rawPolicy)); !errors.Is(err, ErrRawCDPNotAllowed) {
		t.Fatalf("raw policy recorder error = %v, want ErrRawCDPNotAllowed", err)
	}
}

func sameStrings(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}
