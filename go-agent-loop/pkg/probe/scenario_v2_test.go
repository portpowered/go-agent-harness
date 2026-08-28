package probe

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const validScenarioV2ToolRef = "webmcp.tool-ref.v1:AAAAAAAAAAAAAAAAAAAAAA"

type scenarioV2Corpus map[string]bool

func (c scenarioV2Corpus) Has(id string) bool { return c[id] }

func TestLoadScenarioV2AcceptsFrozenGrammarAndPreservesJSON(t *testing.T) {
	root := t.TempDir()
	scenarioDir := filepath.Join(root, "scenarios")
	if err := os.MkdirAll(filepath.Join(scenarioDir, "fixtures"), 0o755); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	for _, name := range []string{"browser.json", "provider.jsonl"} {
		if err := os.WriteFile(filepath.Join(scenarioDir, "fixtures", name), []byte(name), 0o600); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}
	scenarioPath := filepath.Join(scenarioDir, "case.json")
	data := []byte(`{
  "schema_version": "probe.scenario.v2",
  "id": "webmcp-object-output-and-voice",
  "name": "WebMCP object output and voice",
  "description": "all frozen step and expectation variants",
  "browser_fixture": "fixtures/browser.json",
  "provider_fixture": "fixtures/provider.jsonl",
  "steps": [
    {"type":"browser_connect","browser_id":"browser-1","endpoint_id":"endpoint-1"},
    {"type":"browser_discover","browser_id":"browser-1","origin_contains":"fixture.test","eligible_only":true,"include_zero_tool_pages":true},
    {"type":"browser_select","browser_id":"browser-1","target_id":"tab-1","activate":false},
    {"type":"browser_activate","browser_id":"browser-1","target_id":"tab-1"},
    {"type":"browser_disconnect","browser_id":"browser-1"},
	{"type":"browser_navigate_fixture","fixture":"fixtures/browser.json"},
    {"type":"webmcp_wait_ready"},
    {"type":"webmcp_list_tools","refresh":true,"name_contains":"read","include_schemas":true,"frame_id":"frame-1"},
    {"type":"webmcp_invoke","tool_ref":"webmcp.tool-ref.v1:AAAAAAAAAAAAAAAAAAAAAA","input_json":"{\"count\":9007199254740993}","reason":"read the state"},
    {"type":"webmcp_cancel","invocation_id":"inv-1","reason":"stop the request"},
    {"type":"send_text","text":"Continue."},
    {"type":"send_audio","corpus_id":"read-state","text":"Read the current state."},
    {"type":"interrupt","after_event":"assistant_audio_started"},
    {"type":"close_tab","browser_id":"browser-1","target_id":"tab-1"},
    {"type":"open_tab","browser_id":"browser-1","url":"https://fixture.test/new"},
    {"type":"switch_browser","browser_id":"browser-2"},
    {"type":"sleep_fake","duration_ms":25},
    {"type":"close"}
  ],
  "expectations": [
    {"type":"browser_count_equals","equals":1},
    {"type":"eligible_tab_count_equals","equals":2},
    {"type":"selected_tab_equals","target_id":"tab-1"},
    {"type":"selected_origin_equals","origin":"https://fixture.test"},
    {"type":"catalog_generation_equals","equals":7},
    {"type":"tool_catalog_contains","name":"read_state"},
    {"type":"tool_catalog_not_contains","name":"write_state"},
    {"type":"tool_schema_equals","name":"read_state","schema":{"type":"object","properties":{"count":{"type":"integer"}}}},
    {"type":"tool_invocation_count","name":"read_state","equals":1},
    {"type":"tool_input_json_equals","name":"read_state","input_json":"{\"count\":9007199254740993}"},
    {"type":"tool_result_jsonpath_equals","name":"read_state","path":"$.data.output.value","value":9007199254740993},
    {"type":"tool_status_equals","name":"read_state","status":"completed"},
    {"type":"chrome_operation_order","operations":["connect","select","invoke"]},
    {"type":"no_unexpected_chrome_operations","operations":[]},
    {"type":"generated_cdp_method_order","methods":["WebMCP.enable","WebMCP.invokeTool"]},
    {"type":"no_unexpected_generated_cdp_methods","methods":[]},
    {"type":"no_pending_invocations"},
    {"type":"page_state_equals","path":"$.cube.face[0]","value":{"color":"blue","count":9007199254740993}},
    {"type":"response_canceled"},
    {"type":"assistant_audio_started"},
    {"type":"assistant_audio_stopped"},
    {"type":"transcript_contains","text":"state"},
    {"type":"approval_requested","tool_ref":"webmcp.tool-ref.v1:AAAAAAAAAAAAAAAAAAAAAA"},
    {"type":"approval_not_requested"},
    {"type":"stale_tool_rejected","tool_ref":"webmcp.tool-ref.v1:AAAAAAAAAAAAAAAAAAAAAA"},
    {"type":"browser_connection_closed"}
  ]
}`)

	got, err := LoadScenarioV2(data, scenarioPath, scenarioV2Corpus{"read-state": true})
	if err != nil {
		t.Fatalf("LoadScenarioV2: %v", err)
	}
	if got.SchemaVersion != ScenarioV2Version || got.ID != "webmcp-object-output-and-voice" || got.Name == "" {
		t.Fatalf("identity = %#v", got)
	}
	canonicalScenarioDir, err := filepath.EvalSymlinks(scenarioDir)
	if err != nil {
		t.Fatalf("canonicalize scenario directory: %v", err)
	}
	if got.FixtureRoot != canonicalScenarioDir {
		t.Fatalf("fixture root = %q, want %q", got.FixtureRoot, canonicalScenarioDir)
	}
	if got.BrowserFixturePath != filepath.Join(canonicalScenarioDir, "fixtures", "browser.json") || got.ProviderFixturePath != filepath.Join(canonicalScenarioDir, "fixtures", "provider.jsonl") {
		t.Fatalf("resolved fixtures = %q, %q", got.BrowserFixturePath, got.ProviderFixturePath)
	}
	if len(got.Steps) != 18 || len(got.Expectations) != 26 {
		t.Fatalf("variant counts = %d steps, %d expectations", len(got.Steps), len(got.Expectations))
	}
	if got.Steps[7].Refresh != true || !got.Steps[7].HasRefresh || !got.Steps[16].HasDurationMS || got.Steps[16].DurationMS != 25 {
		t.Fatalf("optional control fields were not preserved: %#v", got.Steps[7:8])
	}
	invoke := got.Steps[8]
	if invoke.ToolRef == "" || invoke.Reason != "read the state" || invoke.InputJSON != `{"count":9007199254740993}` {
		t.Fatalf("invoke step = %#v", invoke)
	}
	if string(got.Expectations[10].Value) != "9007199254740993" || string(got.Expectations[7].Schema) != `{"type":"object","properties":{"count":{"type":"integer"}}}` {
		t.Fatalf("JSON token preservation: value=%s schema=%s", got.Expectations[10].Value, got.Expectations[7].Schema)
	}
	if got.Expectations[10].Path != "$.data.output.value" || got.Expectations[10].JSONPath != got.Expectations[10].Path {
		t.Fatalf("JSONPath = %#v", got.Expectations[10])
	}
	if err := got.Validate(scenarioV2Corpus{"read-state": true}); err != nil {
		t.Fatalf("typed validation: %v", err)
	}

	browser, err := got.OpenBrowserFixture()
	if err != nil {
		t.Fatalf("open browser fixture: %v", err)
	}
	browserData, err := io.ReadAll(browser)
	_ = browser.Close()
	if err != nil || string(browserData) != "browser.json" {
		t.Fatalf("browser fixture bytes = %q, err=%v", browserData, err)
	}
	provider, err := got.OpenProviderFixture()
	if err != nil {
		t.Fatalf("open provider fixture: %v", err)
	}
	providerData, err := io.ReadAll(provider)
	_ = provider.Close()
	if err != nil || string(providerData) != "provider.jsonl" {
		t.Fatalf("provider fixture bytes = %q, err=%v", providerData, err)
	}
}

func TestLoadScenarioV2RejectsStrictShapeViolations(t *testing.T) {
	base := `{"schema_version":"probe.scenario.v2","id":"case","steps":[{"type":"close"}],"expectations":[{"type":"no_pending_invocations"}]}`
	cases := []struct {
		name    string
		input   string
		lookup  CorpusLookup
		wantErr error
		path    string
	}{
		{"missing version", `{"id":"case","steps":[{"type":"close"}],"expectations":[{"type":"no_pending_invocations"}]}`, nil, ErrInvalidScenarioV2, "schema_version"},
		{"empty document", ``, nil, ErrInvalidScenarioV2, "scenario"},
		{"malformed object key", `{"`, nil, ErrInvalidScenarioV2, "scenario"},
		{"malformed object end", `{"a":1]`, nil, ErrInvalidScenarioV2, "scenario"},
		{"malformed trailing JSON", `{} {`, nil, ErrInvalidScenarioV2, "scenario"},
		{"unknown version", strings.Replace(base, "probe.scenario.v2", "probe.scenario.v3", 1), nil, ErrInvalidScenarioV2, "schema_version"},
		{"missing id", strings.Replace(base, `"id":"case",`, "", 1), nil, ErrInvalidScenarioV2, "id"},
		{"empty id", strings.Replace(base, `"id":"case",`, `"id":"",`, 1), nil, ErrInvalidScenarioV2, "id"},
		{"bad name", strings.Replace(base, `"id":"case",`, `"id":"case","name":1,`, 1), nil, ErrInvalidScenarioV2, "name"},
		{"bad description", strings.Replace(base, `"id":"case",`, `"id":"case","description":false,`, 1), nil, ErrInvalidScenarioV2, "description"},
		{"bad browser fixture", strings.Replace(base, `"id":"case",`, `"id":"case","browser_fixture":true,`, 1), nil, ErrInvalidScenarioV2, "browser_fixture"},
		{"bad provider fixture", strings.Replace(base, `"id":"case",`, `"id":"case","provider_fixture":[],`, 1), nil, ErrInvalidScenarioV2, "provider_fixture"},
		{"singular expect", strings.Replace(base, `"expectations":`, `"expect":`, 1), nil, ErrInvalidScenarioV2, "expect"},
		{"legacy expected alias", strings.Replace(base, `"expectations":`, `"expected":`, 1), nil, ErrInvalidScenarioV2, "expected"},
		{"legacy expected behavior alias", strings.Replace(base, `"expectations":`, `"expected_behavior":`, 1), nil, ErrInvalidScenarioV2, "expected_behavior"},
		{"unknown root field", strings.Replace(base, `"id":"case",`, `"extra":true,"id":"case",`, 1), nil, ErrInvalidScenarioV2, "extra"},
		{"unknown step field", strings.Replace(base, `{"type":"close"}`, `{"type":"close","kind":"close"}`, 1), nil, ErrInvalidScenarioV2, "kind"},
		{"unknown step variant", strings.Replace(base, "close", "mystery", 1), nil, ErrInvalidScenarioV2, "type"},
		{"malformed step value", strings.Replace(base, `{"type":"close"}`, `{"type":true}`, 1), nil, ErrInvalidScenarioV2, "type"},
		{"unknown expectation field", strings.Replace(base, `{"type":"no_pending_invocations"}`, `{"type":"no_pending_invocations","value":1}`, 1), nil, ErrInvalidScenarioV2, "value"},
		{"invalid browser fixture reference without source path", strings.Replace(base, `"id":"case",`, `"id":"case","browser_fixture":"../browser.json",`, 1), nil, ErrInvalidScenarioV2, "scenario"},
		{"invalid provider fixture reference without source path", strings.Replace(base, `"id":"case",`, `"id":"case","provider_fixture":"https://fixture.test/provider.json",`, 1), nil, ErrInvalidScenarioV2, "scenario"},
		{"empty provider fixture", strings.Replace(base, `"id":"case",`, `"id":"case","provider_fixture":"",`, 1), nil, ErrScenarioV2FixturePath, "provider_fixture"},
		{"missing steps", strings.Replace(base, `"steps":[{"type":"close"}],`, "", 1), nil, ErrInvalidScenarioV2, "steps"},
		{"empty steps", strings.Replace(base, `"steps":[{"type":"close"}]`, `"steps":[]`, 1), nil, ErrInvalidScenarioV2, "steps"},
		{"missing expectations", strings.Replace(base, `,"expectations":[{"type":"no_pending_invocations"}]`, "", 1), nil, ErrInvalidScenarioV2, "expectations"},
		{"empty expectations", strings.Replace(base, `"expectations":[{"type":"no_pending_invocations"}]`, `"expectations":[]`, 1), nil, ErrInvalidScenarioV2, "expectations"},
		{"malformed expectations value", strings.Replace(base, `{"type":"no_pending_invocations"}`, `{"type":"page_state_equals","path":"$.state","value":}`, 1), nil, ErrInvalidScenarioV2, "expectations"},
		{"missing invoke field", strings.Replace(base, `{"type":"close"}`, `{"type":"webmcp_invoke","tool_ref":"`+validScenarioV2ToolRef+`","input_json":"{}"}`, 1), nil, ErrInvalidScenarioV2, "reason"},
		{"invoke nested input", strings.Replace(base, `{"type":"close"}`, `{"type":"webmcp_invoke","tool_ref":"`+validScenarioV2ToolRef+`","input":{"x":1},"reason":"read","input_json":"{}"}`, 1), nil, ErrInvalidScenarioV2, "input"},
		{"invoke non-object input", strings.Replace(base, `{"type":"close"}`, `{"type":"webmcp_invoke","tool_ref":"`+validScenarioV2ToolRef+`","input_json":"[]","reason":"read"}`, 1), nil, ErrInvalidScenarioV2, "input_json"},
		{"invoke malformed input", strings.Replace(base, `{"type":"close"}`, `{"type":"webmcp_invoke","tool_ref":"`+validScenarioV2ToolRef+`","input_json":"{","reason":"read"}`, 1), nil, ErrInvalidScenarioV2, "input_json"},
		{"invalid tool ref", strings.Replace(base, `{"type":"close"}`, `{"type":"webmcp_invoke","tool_ref":"ref","input_json":"{}","reason":"read"}`, 1), nil, ErrInvalidScenarioV2, "tool_ref"},
		{"missing audio corpus", strings.Replace(base, `{"type":"close"}`, `{"type":"send_audio"}`, 1), nil, ErrInvalidScenarioV2, "corpus_id"},
		{"audio path alias", strings.Replace(base, `{"type":"close"}`, `{"type":"send_audio","path":"audio.wav"}`, 1), scenarioV2Corpus{"audio.wav": true}, ErrInvalidScenarioV2, "path"},
		{"unknown corpus", strings.Replace(base, `{"type":"close"}`, `{"type":"send_audio","corpus_id":"missing"}`, 1), scenarioV2Corpus{}, ErrScenarioV2UnknownCorpus, "corpus_id"},
		{"missing count", strings.Replace(base, `{"type":"no_pending_invocations"}`, `{"type":"browser_count_equals"}`, 1), nil, ErrInvalidScenarioV2, "equals"},
		{"bad count token", strings.Replace(base, `{"type":"no_pending_invocations"}`, `{"type":"browser_count_equals","equals":1.5}`, 1), nil, ErrInvalidScenarioV2, "equals"},
		{"empty operation name", strings.Replace(base, `{"type":"no_pending_invocations"}`, `{"type":"chrome_operation_order","operations":[""]}`, 1), nil, ErrInvalidScenarioV2, "operations"},
		{"bad JSONPath", strings.Replace(base, `{"type":"no_pending_invocations"}`, `{"type":"page_state_equals","path":"state","value":true}`, 1), nil, ErrInvalidScenarioV2, "path"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := LoadScenarioV2([]byte(test.input), "", test.lookup)
			if err == nil || !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want errors.Is(..., %v)", err, test.wantErr)
			}
			var scenarioErr *ScenarioV2Error
			if !errors.As(err, &scenarioErr) || !strings.Contains(scenarioErr.Path, test.path) {
				t.Fatalf("error path = %#v, want substring %q", scenarioErr, test.path)
			}
		})
	}
	if _, err := LoadScenarioV2([]byte(base), "", scenarioV2Corpus{}, scenarioV2Corpus{}); err == nil || !errors.Is(err, ErrInvalidScenarioV2) {
		t.Fatalf("multiple corpus lookups were accepted: %v", err)
	}
	fixtureScenarioPath := filepath.Join(t.TempDir(), "case.json")
	for _, test := range []struct {
		name string
		data string
		path string
	}{
		{"browser", strings.Replace(base, `"id":"case",`, `"id":"case","browser_fixture":"../browser.json",`, 1), "browser_fixture"},
		{"provider", strings.Replace(base, `"id":"case",`, `"id":"case","provider_fixture":"https://fixture.test/provider.json",`, 1), "provider_fixture"},
	} {
		t.Run("resolve_invalid_"+test.name+"_fixture", func(t *testing.T) {
			_, err := LoadScenarioV2([]byte(test.data), fixtureScenarioPath)
			if err == nil || !errors.Is(err, ErrScenarioV2FixturePath) {
				t.Fatalf("fixture path error = %v", err)
			}
			var scenarioErr *ScenarioV2Error
			if !errors.As(err, &scenarioErr) || !strings.Contains(scenarioErr.Path, test.path) {
				t.Fatalf("fixture path error location = %#v", scenarioErr)
			}
		})
	}
}

func TestScenarioV2FixtureResolutionIsContainedAndCanonical(t *testing.T) {
	root := t.TempDir()
	scenarioPath := filepath.Join(root, "nested", "case.json")
	if err := os.MkdirAll(filepath.Dir(scenarioPath), 0o755); err != nil {
		t.Fatalf("create scenario directory: %v", err)
	}
	inside := filepath.Join(filepath.Dir(scenarioPath), "inside.json")
	if err := os.WriteFile(inside, []byte("inside"), 0o600); err != nil {
		t.Fatalf("write inside fixture: %v", err)
	}
	canonicalScenarioDir, err := filepath.EvalSymlinks(filepath.Dir(scenarioPath))
	if err != nil {
		t.Fatalf("canonicalize scenario directory: %v", err)
	}
	canonicalInside := filepath.Join(canonicalScenarioDir, "inside.json")
	if got, err := ResolveScenarioV2FixturePath(scenarioPath, "./inside.json"); err != nil || got != canonicalInside {
		t.Fatalf("safe relative path = %q, %v; want %q", got, err, canonicalInside)
	}

	invalid := []string{
		"../outside.json",
		filepath.Join(string(filepath.Separator), "outside.json"),
		"https://example.test/fixture.json",
		"file:fixture.json",
		"C:\\outside\\fixture.json",
		"$HOME/fixture.json",
		"${SCENARIO_ROOT}/fixture.json",
		"~/fixture.json",
		`..\outside.json`,
	}
	for _, reference := range invalid {
		t.Run("reject_"+strings.ReplaceAll(strings.ReplaceAll(reference, "/", "_"), `\`, "_"), func(t *testing.T) {
			_, err := ResolveScenarioV2FixturePath(scenarioPath, reference)
			if err == nil || !errors.Is(err, ErrScenarioV2FixturePath) {
				t.Fatalf("reference %q error = %v", reference, err)
			}
		})
	}

	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "outside.json")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside fixture: %v", err)
	}
	link := filepath.Join(filepath.Dir(scenarioPath), "outside-link.json")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink test unavailable: %v", err)
	}
	if _, err := ResolveScenarioV2FixturePath(scenarioPath, "outside-link.json"); err == nil || !errors.Is(err, ErrScenarioV2FixturePath) {
		t.Fatalf("external symlink error = %v", err)
	}

	insideTarget := filepath.Join(filepath.Dir(scenarioPath), "inside-target.json")
	if err := os.WriteFile(insideTarget, []byte("target"), 0o600); err != nil {
		t.Fatalf("write inside target: %v", err)
	}
	insideLink := filepath.Join(filepath.Dir(scenarioPath), "inside-link.json")
	if err := os.Symlink(insideTarget, insideLink); err != nil {
		t.Skipf("inside symlink test unavailable: %v", err)
	}
	resolved, err := ResolveScenarioV2FixturePath(scenarioPath, "inside-link.json")
	if err != nil {
		t.Fatalf("resolve contained symlink: %v", err)
	}
	canonicalInsideTarget, err := filepath.EvalSymlinks(insideTarget)
	if err != nil {
		t.Fatalf("canonicalize inside target: %v", err)
	}
	if err != nil || resolved != canonicalInsideTarget {
		t.Fatalf("contained symlink = %q, %v; want %q", resolved, err, canonicalInsideTarget)
	}
}

func TestScenarioV2LegacyScenarioLoaderRemainsByteCompatible(t *testing.T) {
	data := []byte(textScenario)
	before := append([]byte(nil), data...)
	if _, err := Load(data); err != nil {
		t.Fatalf("legacy Load: %v", err)
	}
	if !reflect.DeepEqual(data, before) {
		t.Fatal("legacy scenario bytes changed while loading")
	}
	if _, err := LoadScenarioV2(data, ""); err == nil {
		t.Fatal("v2 loader accepted an unversioned legacy scenario")
	}
}

func TestScenarioV2APIAliasesAndTypedValidation(t *testing.T) {
	root := t.TempDir()
	scenarioPath := filepath.Join(root, "scenario.json")
	fixturePath := filepath.Join(root, "browser.json")
	if err := os.WriteFile(fixturePath, []byte("fixture"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("canonicalize fixture root: %v", err)
	}
	canonicalFixturePath := filepath.Join(canonicalRoot, "browser.json")
	data := []byte(`{"schema_version":"probe.scenario.v2","id":"aliases","browser_fixture":"browser.json","steps":[{"type":"close"}],"expectations":[{"type":"no_pending_invocations"}]}`)
	loaders := []struct {
		name string
		load func() (ScenarioV2, error)
	}{
		{"decode", func() (ScenarioV2, error) { return DecodeScenarioV2(bytes.NewReader(data), scenarioPath) }},
		{"probe alias", func() (ScenarioV2, error) { return LoadProbeScenarioV2(data, scenarioPath) }},
	}
	for _, loader := range loaders {
		t.Run(loader.name, func(t *testing.T) {
			got, err := loader.load()
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got.BrowserFixturePath != canonicalFixturePath || !got.Valid() {
				t.Fatalf("loaded scenario = %#v", got)
			}
			if resolved, err := got.ResolveFixture("browser.json"); err != nil || resolved != canonicalFixturePath {
				t.Fatalf("ResolveFixture = %q, %v", resolved, err)
			}
		})
	}
	fileScenario, err := LoadScenarioV2File(scenarioPath, nil)
	if err == nil {
		t.Fatal("LoadScenarioV2File unexpectedly read a missing scenario")
	}
	if _, err := os.Stat(scenarioPath); !os.IsNotExist(err) {
		t.Fatalf("scenario unexpectedly exists: %v", err)
	}
	_ = fileScenario
	if err := os.WriteFile(scenarioPath, data, 0o600); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
	fileScenario, err = LoadProbeScenarioV2File(scenarioPath)
	if err != nil || !fileScenario.Valid() {
		t.Fatalf("LoadProbeScenarioV2File = %#v, %v", fileScenario, err)
	}
	opened, err := OpenScenarioV2Fixture(scenarioPath, "browser.json")
	if err != nil {
		t.Fatalf("OpenScenarioV2Fixture: %v", err)
	}
	openedData, readErr := io.ReadAll(opened)
	_ = opened.Close()
	if readErr != nil || string(openedData) != "fixture" {
		t.Fatalf("opened fixture = %q, %v", openedData, readErr)
	}
	withoutFixture := ScenarioV2{SchemaVersion: ScenarioV2Version, ID: "typed", Steps: []ScenarioV2Step{{Type: ScenarioV2StepClose}}, Expectations: []ScenarioV2Expectation{{Type: ScenarioV2ExpectationNoPendingInvocations}}}
	if !withoutFixture.Valid() {
		t.Fatal("minimal typed scenario should be valid")
	}
	if _, err := withoutFixture.ResolveFixture("browser.json"); err == nil {
		t.Fatal("ResolveFixture accepted a scenario without a root")
	}
	if _, err := withoutFixture.OpenBrowserFixture(); err == nil {
		t.Fatal("OpenBrowserFixture accepted an absent reference")
	}
	invalid := withoutFixture
	invalid.SchemaVersion = "probe.scenario.v3"
	if invalid.Valid() {
		t.Fatal("invalid version was reported valid")
	}
	if err := withoutFixture.Validate(nil, nil); err == nil {
		t.Fatal("multiple corpus lookups were accepted")
	}
	var nilError *ScenarioV2Error
	if nilError.Error() != ErrInvalidScenarioV2.Error() || !errors.Is(nilError.Unwrap(), ErrInvalidScenarioV2) {
		t.Fatal("nil ScenarioV2Error behavior changed")
	}
	wrapped := wrapScenarioV2Error("outer", newScenarioV2Error("inner", "bad"))
	var wrappedError *ScenarioV2Error
	if !errors.As(wrapped, &wrappedError) || wrappedError.Path != "outer.inner" {
		t.Fatalf("wrapped error = %#v", wrappedError)
	}
}

func TestScenarioV2RejectsMalformedDocumentsAndTypedValues(t *testing.T) {
	base := `{"schema_version":"probe.scenario.v2","id":"case","steps":[{"type":"close"}],"expectations":[{"type":"no_pending_invocations"}]}`
	cases := []struct {
		name  string
		input any
	}{
		{"unsupported input", struct{}{}},
		{"invalid utf8", []byte{0xff}},
		{"array root", `[]`},
		{"malformed root", `{`},
		{"trailing JSON", base + ` {}`},
		{"duplicate root field", `{"schema_version":"probe.scenario.v2","schema_version":"probe.scenario.v2","id":"case","steps":[{"type":"close"}],"expectations":[{"type":"no_pending_invocations"}]}`},
		{"null steps", strings.Replace(base, `"steps":[{"type":"close"}]`, `"steps":null`, 1)},
		{"object steps", strings.Replace(base, `"steps":[{"type":"close"}]`, `"steps":{}`, 1)},
		{"empty steps", strings.Replace(base, `"steps":[{"type":"close"}]`, `"steps":[]`, 1)},
		{"missing steps", strings.Replace(base, `"steps":[{"type":"close"}],`, "", 1)},
		{"null step", strings.Replace(base, `{"type":"close"}`, `null`, 1)},
		{"null expectations", strings.Replace(base, `"expectations":[{"type":"no_pending_invocations"}]`, `"expectations":null`, 1)},
		{"object expectations", strings.Replace(base, `"expectations":[{"type":"no_pending_invocations"}]`, `"expectations":{}`, 1)},
		{"empty expectations", strings.Replace(base, `"expectations":[{"type":"no_pending_invocations"}]`, `"expectations":[]`, 1)},
		{"missing expectations", strings.Replace(base, `,"expectations":[{"type":"no_pending_invocations"}]`, "", 1)},
		{"bad optional string", strings.Replace(base, `{"type":"close"}`, `{"type":"browser_connect","browser_id":1}`, 1)},
		{"bad optional bool", strings.Replace(base, `{"type":"close"}`, `{"type":"browser_discover","eligible_only":1}`, 1)},
		{"bad zero-page bool", strings.Replace(base, `{"type":"close"}`, `{"type":"browser_discover","include_zero_tool_pages":1}`, 1)},
		{"bad activate bool", strings.Replace(base, `{"type":"close"}`, `{"type":"browser_select","target_id":"tab-1","activate":1}`, 1)},
		{"bad optional list bool", strings.Replace(base, `{"type":"close"}`, `{"type":"webmcp_list_tools","refresh":"yes"}`, 1)},
		{"bad schema bool", strings.Replace(base, `{"type":"close"}`, `{"type":"webmcp_list_tools","include_schemas":"yes"}`, 1)},
		{"bad duration", strings.Replace(base, `{"type":"close"}`, `{"type":"sleep_fake","duration_ms":1.5}`, 1)},
		{"null duration", strings.Replace(base, `{"type":"close"}`, `{"type":"sleep_fake","duration_ms":null}`, 1)},
		{"negative duration", strings.Replace(base, `{"type":"close"}`, `{"type":"sleep_fake","duration_ms":-1}`, 1)},
		{"bad schema value", strings.Replace(base, `{"type":"no_pending_invocations"}`, `{"type":"tool_schema_equals","name":"tool","schema":[]}`, 1)},
		{"bad operation list", strings.Replace(base, `{"type":"no_pending_invocations"}`, `{"type":"chrome_operation_order","operations":[1]}`, 1)},
		{"bad method list", strings.Replace(base, `{"type":"no_pending_invocations"}`, `{"type":"generated_cdp_method_order","methods":[null]}`, 1)},
		{"missing allowed chrome operations", strings.Replace(base, `{"type":"no_pending_invocations"}`, `{"type":"no_unexpected_chrome_operations"}`, 1)},
		{"missing allowed cdp methods", strings.Replace(base, `{"type":"no_pending_invocations"}`, `{"type":"no_unexpected_generated_cdp_methods"}`, 1)},
		{"duplicate input JSON", strings.Replace(base, `{"type":"close"}`, `{"type":"webmcp_invoke","tool_ref":"`+validScenarioV2ToolRef+`","input_json":"{\"a\":1,\"a\":2}","reason":"read"}`, 1)},
		{"empty browser fixture", strings.Replace(base, `"id":"case",`, `"id":"case","browser_fixture":"",`, 1)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := LoadScenarioV2(test.input, ""); err == nil {
				t.Fatal("malformed document unexpectedly loaded")
			}
		})
	}

	stepCases := []string{
		`{"type":"browser_select"}`,
		`{"type":"browser_navigate_fixture"}`,
		`{"type":"webmcp_invoke","tool_ref":"` + validScenarioV2ToolRef + `","input_json":"{}"}`,
		`{"type":"webmcp_invoke","input_json":"{}","reason":"read"}`,
		`{"type":"webmcp_invoke","tool_ref":"` + validScenarioV2ToolRef + `","reason":"read"}`,
		`{"type":"webmcp_cancel"}`,
		`{"type":"send_text"}`,
		`{"type":"send_audio"}`,
		`{"type":"interrupt"}`,
		`{"type":"open_tab"}`,
		`{"type":"switch_browser"}`,
		`{"type":"sleep_fake"}`,
	}
	for _, step := range stepCases {
		t.Run("missing_"+strings.ReplaceAll(step[9:len(step)-1], `"`, ""), func(t *testing.T) {
			input := `{"schema_version":"probe.scenario.v2","id":"case","steps":[` + step + `],"expectations":[{"type":"no_pending_invocations"}]}`
			if _, err := LoadScenarioV2(input, ""); err == nil {
				t.Fatalf("step %s unexpectedly loaded", step)
			}
		})
	}
}

func TestScenarioV2ErrorsAndFixtureOpeningStaySafe(t *testing.T) {
	root := t.TempDir()
	scenarioPath := filepath.Join(root, "case.json")
	fixturePath := filepath.Join(root, "browser.json")
	if err := os.WriteFile(fixturePath, []byte("browser"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	data := []byte(`{"schema_version":"probe.scenario.v2","id":"open","browser_fixture":"browser.json","steps":[{"type":"close"}],"expectations":[{"type":"no_pending_invocations"}]}`)
	scenario, err := LoadScenarioV2(data, scenarioPath)
	if err != nil {
		t.Fatalf("load scenario: %v", err)
	}
	if got, err := ResolveScenarioFixturePath(scenarioPath, "browser.json"); err != nil || got != scenario.BrowserFixturePath {
		t.Fatalf("alias fixture resolution = %q, %v; want %q", got, err, scenario.BrowserFixturePath)
	}

	opened, err := OpenScenarioV2Fixture(scenarioPath, "browser.json")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	if err := opened.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}
	if _, err := OpenScenarioV2Fixture(scenarioPath, "missing/fixture.json"); err == nil || !strings.Contains(err.Error(), "open fixture") {
		t.Fatalf("missing fixture error = %v", err)
	}

	missingParent, resolveErr := ResolveScenarioV2FixturePath(scenarioPath, "new/fixture.json")
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("canonicalize root: %v", err)
	}
	if resolveErr != nil || missingParent != filepath.Join(canonicalRoot, "new", "fixture.json") {
		t.Fatalf("missing parent resolution = %q, %v; want %q", missingParent, resolveErr, filepath.Join(canonicalRoot, "new", "fixture.json"))
	}
	if _, err := scenario.ResolveFixture("../outside.json"); err == nil || !errors.Is(err, ErrScenarioV2FixturePath) {
		t.Fatalf("scenario ResolveFixture accepted escape: %v", err)
	}
	unsafeScenario := scenario
	unsafeScenario.BrowserFixture = "../outside.json"
	if _, err := unsafeScenario.OpenBrowserFixture(); err == nil || !errors.Is(err, ErrScenarioV2FixturePath) {
		t.Fatalf("OpenBrowserFixture accepted escape: %v", err)
	}
	if _, err := ResolveScenarioV2FixturePath("", "fixture.json"); err == nil || !errors.Is(err, ErrInvalidScenarioV2) {
		t.Fatalf("empty scenario path was accepted: %v", err)
	}
	if _, err := OpenScenarioV2Fixture("", "fixture.json"); err == nil || !errors.Is(err, ErrInvalidScenarioV2) {
		t.Fatalf("OpenScenarioV2Fixture accepted empty scenario path: %v", err)
	}

	noFile, err := LoadScenarioV2([]byte(`{"schema_version":"probe.scenario.v2","id":"no-file","browser_fixture":"not-created.json","steps":[{"type":"close"}],"expectations":[{"type":"no_pending_invocations"}]}`), scenarioPath)
	if err != nil {
		t.Fatalf("load missing-fixture scenario: %v", err)
	}
	if _, err := noFile.OpenBrowserFixture(); err == nil || !strings.Contains(err.Error(), "open browser_fixture") {
		t.Fatalf("missing browser fixture open error = %v", err)
	}
	if _, err := noFile.OpenProviderFixture(); err == nil || !strings.Contains(err.Error(), "is not configured") {
		t.Fatalf("missing provider fixture error = %v", err)
	}

	if _, err := LoadScenarioV2(data, filepath.Join(root, "does-not-exist", "case.json")); err == nil || !errors.Is(err, ErrInvalidScenarioV2) {
		t.Fatalf("uncanonicalizable scenario directory error = %v", err)
	}
	if _, err := LoadScenarioV2(data, ""); err == nil || !errors.Is(err, ErrInvalidScenarioV2) {
		t.Fatalf("fixture scenario without source path was accepted: %v", err)
	}

	cause := errors.New("private cause")
	plain := wrapScenarioV2Error("outer", cause)
	if !errors.Is(plain, cause) || !strings.Contains(plain.Error(), "outer") || !strings.Contains(plain.Error(), "private cause") {
		t.Fatalf("plain wrapped error = %v", plain)
	}
	withPath := wrapScenarioV2Error("outer", &ScenarioV2Error{Path: "inner", Cause: cause})
	if !errors.Is(withPath, cause) || !strings.Contains(withPath.Error(), "outer.inner") {
		t.Fatalf("path wrapped error = %v", withPath)
	}
	withoutPathWrapped := wrapScenarioV2Error("outer", &ScenarioV2Error{Cause: cause})
	if !errors.Is(withoutPathWrapped, cause) || !strings.Contains(withoutPathWrapped.Error(), "outer") {
		t.Fatalf("unlocated wrapped error = %v", withoutPathWrapped)
	}
	if wrapScenarioV2Error("ignored", nil) != nil {
		t.Fatal("nil error was wrapped")
	}
	withoutPath := &ScenarioV2Error{Cause: cause}
	if !errors.Is(withoutPath, cause) || !strings.Contains(withoutPath.Error(), "private cause") {
		t.Fatalf("unlocated error = %v", withoutPath)
	}
}

func TestScenarioV2TypedValidationRejectsInvalidValues(t *testing.T) {
	minimal := func() ScenarioV2 {
		return ScenarioV2{
			SchemaVersion: ScenarioV2Version,
			ID:            "typed",
			Steps:         []ScenarioV2Step{{Type: ScenarioV2StepClose}},
			Expectations:  []ScenarioV2Expectation{{Type: ScenarioV2ExpectationNoPendingInvocations}},
		}
	}
	tests := []struct {
		name   string
		mutate func(*ScenarioV2)
	}{
		{"empty id", func(s *ScenarioV2) { s.ID = " " }},
		{"missing steps", func(s *ScenarioV2) { s.Steps = nil }},
		{"missing expectations", func(s *ScenarioV2) { s.Expectations = nil }},
		{"fixture without root", func(s *ScenarioV2) { s.BrowserFixture = "browser.json" }},
		{"unknown step", func(s *ScenarioV2) { s.Steps[0].Type = "unknown" }},
		{"select without target", func(s *ScenarioV2) { s.Steps[0] = ScenarioV2Step{Type: ScenarioV2StepBrowserSelect} }},
		{"navigate without target", func(s *ScenarioV2) { s.Steps[0] = ScenarioV2Step{Type: ScenarioV2StepBrowserNavigateFixture} }},
		{"invoke without tool", func(s *ScenarioV2) {
			s.Steps[0] = ScenarioV2Step{Type: ScenarioV2StepWebMCPInvoke, InputJSON: "{}", Reason: "read"}
		}},
		{"invoke without input", func(s *ScenarioV2) {
			s.Steps[0] = ScenarioV2Step{Type: ScenarioV2StepWebMCPInvoke, ToolRef: validScenarioV2ToolRef, Reason: "read"}
		}},
		{"invoke without reason", func(s *ScenarioV2) {
			s.Steps[0] = ScenarioV2Step{Type: ScenarioV2StepWebMCPInvoke, ToolRef: validScenarioV2ToolRef, InputJSON: "{}"}
		}},
		{"cancel without id", func(s *ScenarioV2) { s.Steps[0] = ScenarioV2Step{Type: ScenarioV2StepWebMCPCancel} }},
		{"text without text", func(s *ScenarioV2) { s.Steps[0] = ScenarioV2Step{Type: ScenarioV2StepSendText} }},
		{"audio without corpus", func(s *ScenarioV2) { s.Steps[0] = ScenarioV2Step{Type: ScenarioV2StepSendAudio} }},
		{"interrupt without event", func(s *ScenarioV2) { s.Steps[0] = ScenarioV2Step{Type: ScenarioV2StepInterrupt} }},
		{"open tab without url", func(s *ScenarioV2) { s.Steps[0] = ScenarioV2Step{Type: ScenarioV2StepOpenTab} }},
		{"switch without browser", func(s *ScenarioV2) { s.Steps[0] = ScenarioV2Step{Type: ScenarioV2StepSwitchBrowser} }},
		{"sleep without duration", func(s *ScenarioV2) { s.Steps[0] = ScenarioV2Step{Type: ScenarioV2StepSleepFake} }},
		{"negative duration", func(s *ScenarioV2) {
			s.Steps[0] = ScenarioV2Step{Type: ScenarioV2StepSleepFake, DurationMS: -1, HasDurationMS: true}
		}},
		{"unknown expectation", func(s *ScenarioV2) { s.Expectations[0].Type = "unknown" }},
		{"selected tab without target", func(s *ScenarioV2) {
			s.Expectations[0] = ScenarioV2Expectation{Type: ScenarioV2ExpectationSelectedTabEquals}
		}},
		{"selected origin without origin", func(s *ScenarioV2) {
			s.Expectations[0] = ScenarioV2Expectation{Type: ScenarioV2ExpectationSelectedOriginEquals}
		}},
		{"catalog name missing", func(s *ScenarioV2) {
			s.Expectations[0] = ScenarioV2Expectation{Type: ScenarioV2ExpectationToolCatalogContains}
		}},
		{"input name missing", func(s *ScenarioV2) {
			s.Expectations[0] = ScenarioV2Expectation{Type: ScenarioV2ExpectationToolInputJSONEquals, InputJSON: "{}"}
		}},
		{"input json missing", func(s *ScenarioV2) {
			s.Expectations[0] = ScenarioV2Expectation{Type: ScenarioV2ExpectationToolInputJSONEquals, Name: "read"}
		}},
		{"status missing name", func(s *ScenarioV2) {
			s.Expectations[0] = ScenarioV2Expectation{Type: ScenarioV2ExpectationToolStatusEquals, Status: "completed"}
		}},
		{"status missing value", func(s *ScenarioV2) {
			s.Expectations[0] = ScenarioV2Expectation{Type: ScenarioV2ExpectationToolStatusEquals, Name: "read"}
		}},
		{"schema missing value", func(s *ScenarioV2) {
			s.Expectations[0] = ScenarioV2Expectation{Type: ScenarioV2ExpectationToolSchemaEquals}
		}},
		{"result missing path", func(s *ScenarioV2) {
			s.Expectations[0] = ScenarioV2Expectation{Type: ScenarioV2ExpectationToolResultJSONPathEquals, Value: json.RawMessage(`true`)}
		}},
		{"result missing value", func(s *ScenarioV2) {
			s.Expectations[0] = ScenarioV2Expectation{Type: ScenarioV2ExpectationToolResultJSONPathEquals, Path: "$.value"}
		}},
		{"operation order missing list", func(s *ScenarioV2) {
			s.Expectations[0] = ScenarioV2Expectation{Type: ScenarioV2ExpectationChromeOperationOrder}
		}},
		{"cdp order missing list", func(s *ScenarioV2) {
			s.Expectations[0] = ScenarioV2Expectation{Type: ScenarioV2ExpectationGeneratedCDPMethodOrder}
		}},
		{"transcript missing text", func(s *ScenarioV2) {
			s.Expectations[0] = ScenarioV2Expectation{Type: ScenarioV2ExpectationTranscriptContains}
		}},
		{"negative equals", func(s *ScenarioV2) {
			s.Expectations[0] = ScenarioV2Expectation{Type: ScenarioV2ExpectationBrowserCountEquals, Equals: -1, HasEquals: true}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scenario := minimal()
			test.mutate(&scenario)
			if err := scenario.Validate(); err == nil {
				t.Fatal("invalid typed scenario unexpectedly validated")
			}
		})
	}

	validWithValues := minimal()
	canonicalTypedRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize typed fixture root: %v", err)
	}
	validWithValues.FixtureRoot = canonicalTypedRoot
	validWithValues.BrowserFixture = "browser.json"
	validWithValues.Steps = []ScenarioV2Step{
		{Type: ScenarioV2StepWebMCPInvoke, ToolRef: validScenarioV2ToolRef, InputJSON: "{}", Reason: "read"},
		{Type: ScenarioV2StepSendAudio, CorpusID: "known"},
		{Type: ScenarioV2StepSleepFake, DurationMS: 0, HasDurationMS: true},
	}
	validWithValues.Expectations = []ScenarioV2Expectation{
		{Type: ScenarioV2ExpectationToolInputJSONEquals, Name: "read", InputJSON: "{}"},
		{Type: ScenarioV2ExpectationToolSchemaEquals, Name: "read", Schema: json.RawMessage(`{"type":"object"}`)},
		{Type: ScenarioV2ExpectationToolResultJSONPathEquals, Name: "read", Path: "$.value", Value: json.RawMessage(`null`)},
		{Type: ScenarioV2ExpectationChromeOperationOrder, Operations: []string{}},
		{Type: ScenarioV2ExpectationGeneratedCDPMethodOrder, Methods: []string{}},
		{Type: ScenarioV2ExpectationTranscriptContains, Text: "state"},
	}
	if err := validWithValues.Validate(scenarioV2Corpus{"known": true}); err != nil {
		t.Fatalf("valid typed scenario rejected: %v", err)
	}
	invalidReference := validWithValues
	invalidReference.BrowserFixture = "../outside.json"
	if err := invalidReference.Validate(scenarioV2Corpus{"known": true}); err == nil || !errors.Is(err, ErrScenarioV2FixturePath) {
		t.Fatalf("typed fixture escape was accepted: %v", err)
	}
}
