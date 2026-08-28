package probe

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// ScenarioV2Version is the explicit version marker for browser-aware probe
	// scenarios. It is intentionally not accepted by the legacy Scenario
	// loader, whose aliases and shape are part of its compatibility contract.
	ScenarioV2Version = "probe.scenario.v2"
	// ProbeScenarioV2Version is a descriptive alias for ScenarioV2Version.
	ProbeScenarioV2Version = ScenarioV2Version
)

var (
	// ErrInvalidScenarioV2 identifies a malformed or unsupported v2 scenario.
	ErrInvalidScenarioV2 = errors.New("invalid probe.scenario.v2 scenario")
	// ErrScenarioV2FixturePath identifies a fixture reference that is not a
	// contained relative path or whose resolved target leaves the scenario root.
	ErrScenarioV2FixturePath = errors.New("invalid probe.scenario.v2 fixture path")
	// ErrScenarioV2UnknownCorpus identifies an audio corpus not present in the
	// lookup supplied by the caller.
	ErrScenarioV2UnknownCorpus = errors.New("unknown probe.scenario.v2 audio corpus")
)

// ScenarioV2Error is a safe structural error from the v2 decoder. Path names
// identify a JSON location or fixture reference; values are deliberately not
// included in the error so page data and secrets are not echoed.
type ScenarioV2Error struct {
	Path  string
	Cause error
}

// ScenarioV2ValidationError is a descriptive alias for ScenarioV2Error.
type ScenarioV2ValidationError = ScenarioV2Error

func (e *ScenarioV2Error) Error() string {
	if e == nil {
		return ErrInvalidScenarioV2.Error()
	}
	message := ErrInvalidScenarioV2.Error()
	if e.Path != "" {
		message += " at " + e.Path
	}
	if e.Cause != nil {
		message += ": " + e.Cause.Error()
	}
	return message
}

func (e *ScenarioV2Error) Unwrap() error {
	if e == nil || e.Cause == nil {
		return ErrInvalidScenarioV2
	}
	return errors.Join(ErrInvalidScenarioV2, e.Cause)
}

func newScenarioV2Error(path, format string, args ...any) error {
	return &ScenarioV2Error{Path: path, Cause: fmt.Errorf(format, args...)}
}

func wrapScenarioV2Error(path string, err error) error {
	if err == nil {
		return nil
	}
	var scenarioErr *ScenarioV2Error
	if errors.As(err, &scenarioErr) {
		copyOf := *scenarioErr
		if copyOf.Path == "" {
			copyOf.Path = path
		} else if path != "" {
			copyOf.Path = path + "." + copyOf.Path
		}
		return &copyOf
	}
	return &ScenarioV2Error{Path: path, Cause: err}
}

// ScenarioV2StepType is the exact step vocabulary of probe.scenario.v2.
type ScenarioV2StepType string

const (
	ScenarioV2StepBrowserConnect         ScenarioV2StepType = "browser_connect"
	ScenarioV2StepBrowserDiscover        ScenarioV2StepType = "browser_discover"
	ScenarioV2StepBrowserSelect          ScenarioV2StepType = "browser_select"
	ScenarioV2StepBrowserActivate        ScenarioV2StepType = "browser_activate"
	ScenarioV2StepBrowserDisconnect      ScenarioV2StepType = "browser_disconnect"
	ScenarioV2StepBrowserNavigateFixture ScenarioV2StepType = "browser_navigate_fixture"
	ScenarioV2StepWebMCPWaitReady        ScenarioV2StepType = "webmcp_wait_ready"
	ScenarioV2StepWebMCPListTools        ScenarioV2StepType = "webmcp_list_tools"
	ScenarioV2StepWebMCPInvoke           ScenarioV2StepType = "webmcp_invoke"
	ScenarioV2StepWebMCPCancel           ScenarioV2StepType = "webmcp_cancel"
	ScenarioV2StepSendText               ScenarioV2StepType = "send_text"
	ScenarioV2StepSendAudio              ScenarioV2StepType = "send_audio"
	ScenarioV2StepInterrupt              ScenarioV2StepType = "interrupt"
	ScenarioV2StepCloseTab               ScenarioV2StepType = "close_tab"
	ScenarioV2StepOpenTab                ScenarioV2StepType = "open_tab"
	ScenarioV2StepSwitchBrowser          ScenarioV2StepType = "switch_browser"
	ScenarioV2StepSleepFake              ScenarioV2StepType = "sleep_fake"
	ScenarioV2StepClose                  ScenarioV2StepType = "close"
)

// V2StepType is a shorter alias for ScenarioV2StepType.
type V2StepType = ScenarioV2StepType

const (
	V2StepBrowserConnect         = ScenarioV2StepBrowserConnect
	V2StepBrowserDiscover        = ScenarioV2StepBrowserDiscover
	V2StepBrowserSelect          = ScenarioV2StepBrowserSelect
	V2StepBrowserActivate        = ScenarioV2StepBrowserActivate
	V2StepBrowserDisconnect      = ScenarioV2StepBrowserDisconnect
	V2StepBrowserNavigateFixture = ScenarioV2StepBrowserNavigateFixture
	V2StepWebMCPWaitReady        = ScenarioV2StepWebMCPWaitReady
	V2StepWebMCPListTools        = ScenarioV2StepWebMCPListTools
	V2StepWebMCPInvoke           = ScenarioV2StepWebMCPInvoke
	V2StepWebMCPCancel           = ScenarioV2StepWebMCPCancel
	V2StepSendText               = ScenarioV2StepSendText
	V2StepSendAudio              = ScenarioV2StepSendAudio
	V2StepInterrupt              = ScenarioV2StepInterrupt
	V2StepCloseTab               = ScenarioV2StepCloseTab
	V2StepOpenTab                = ScenarioV2StepOpenTab
	V2StepSwitchBrowser          = ScenarioV2StepSwitchBrowser
	V2StepSleepFake              = ScenarioV2StepSleepFake
	V2StepClose                  = ScenarioV2StepClose
)

// ScenarioV2ExpectationType is the exact expectation vocabulary of
// probe.scenario.v2.
type ScenarioV2ExpectationType string

const (
	ScenarioV2ExpectationBrowserCountEquals              ScenarioV2ExpectationType = "browser_count_equals"
	ScenarioV2ExpectationEligibleTabCountEquals          ScenarioV2ExpectationType = "eligible_tab_count_equals"
	ScenarioV2ExpectationSelectedTabEquals               ScenarioV2ExpectationType = "selected_tab_equals"
	ScenarioV2ExpectationSelectedOriginEquals            ScenarioV2ExpectationType = "selected_origin_equals"
	ScenarioV2ExpectationCatalogGenerationEquals         ScenarioV2ExpectationType = "catalog_generation_equals"
	ScenarioV2ExpectationToolCatalogContains             ScenarioV2ExpectationType = "tool_catalog_contains"
	ScenarioV2ExpectationToolCatalogNotContains          ScenarioV2ExpectationType = "tool_catalog_not_contains"
	ScenarioV2ExpectationToolSchemaEquals                ScenarioV2ExpectationType = "tool_schema_equals"
	ScenarioV2ExpectationToolInvocationCount             ScenarioV2ExpectationType = "tool_invocation_count"
	ScenarioV2ExpectationToolInputJSONEquals             ScenarioV2ExpectationType = "tool_input_json_equals"
	ScenarioV2ExpectationToolResultJSONPathEquals        ScenarioV2ExpectationType = "tool_result_jsonpath_equals"
	ScenarioV2ExpectationToolStatusEquals                ScenarioV2ExpectationType = "tool_status_equals"
	ScenarioV2ExpectationChromeOperationOrder            ScenarioV2ExpectationType = "chrome_operation_order"
	ScenarioV2ExpectationNoUnexpectedChromeOperations    ScenarioV2ExpectationType = "no_unexpected_chrome_operations"
	ScenarioV2ExpectationGeneratedCDPMethodOrder         ScenarioV2ExpectationType = "generated_cdp_method_order"
	ScenarioV2ExpectationNoUnexpectedGeneratedCDPMethods ScenarioV2ExpectationType = "no_unexpected_generated_cdp_methods"
	ScenarioV2ExpectationNoPendingInvocations            ScenarioV2ExpectationType = "no_pending_invocations"
	ScenarioV2ExpectationPageStateEquals                 ScenarioV2ExpectationType = "page_state_equals"
	ScenarioV2ExpectationResponseCanceled                ScenarioV2ExpectationType = "response_canceled"
	ScenarioV2ExpectationAssistantAudioStarted           ScenarioV2ExpectationType = "assistant_audio_started"
	ScenarioV2ExpectationAssistantAudioStopped           ScenarioV2ExpectationType = "assistant_audio_stopped"
	ScenarioV2ExpectationTranscriptContains              ScenarioV2ExpectationType = "transcript_contains"
	ScenarioV2ExpectationApprovalRequested               ScenarioV2ExpectationType = "approval_requested"
	ScenarioV2ExpectationApprovalNotRequested            ScenarioV2ExpectationType = "approval_not_requested"
	ScenarioV2ExpectationStaleToolRejected               ScenarioV2ExpectationType = "stale_tool_rejected"
	ScenarioV2ExpectationBrowserConnectionClosed         ScenarioV2ExpectationType = "browser_connection_closed"
)

// V2ExpectationType is a shorter alias for ScenarioV2ExpectationType.
type V2ExpectationType = ScenarioV2ExpectationType

const (
	V2ExpectationBrowserCountEquals              = ScenarioV2ExpectationBrowserCountEquals
	V2ExpectationEligibleTabCountEquals          = ScenarioV2ExpectationEligibleTabCountEquals
	V2ExpectationSelectedTabEquals               = ScenarioV2ExpectationSelectedTabEquals
	V2ExpectationSelectedOriginEquals            = ScenarioV2ExpectationSelectedOriginEquals
	V2ExpectationCatalogGenerationEquals         = ScenarioV2ExpectationCatalogGenerationEquals
	V2ExpectationToolCatalogContains             = ScenarioV2ExpectationToolCatalogContains
	V2ExpectationToolCatalogNotContains          = ScenarioV2ExpectationToolCatalogNotContains
	V2ExpectationToolSchemaEquals                = ScenarioV2ExpectationToolSchemaEquals
	V2ExpectationToolInvocationCount             = ScenarioV2ExpectationToolInvocationCount
	V2ExpectationToolInputJSONEquals             = ScenarioV2ExpectationToolInputJSONEquals
	V2ExpectationToolResultJSONPathEquals        = ScenarioV2ExpectationToolResultJSONPathEquals
	V2ExpectationToolStatusEquals                = ScenarioV2ExpectationToolStatusEquals
	V2ExpectationChromeOperationOrder            = ScenarioV2ExpectationChromeOperationOrder
	V2ExpectationNoUnexpectedChromeOperations    = ScenarioV2ExpectationNoUnexpectedChromeOperations
	V2ExpectationGeneratedCDPMethodOrder         = ScenarioV2ExpectationGeneratedCDPMethodOrder
	V2ExpectationNoUnexpectedGeneratedCDPMethods = ScenarioV2ExpectationNoUnexpectedGeneratedCDPMethods
	V2ExpectationNoPendingInvocations            = ScenarioV2ExpectationNoPendingInvocations
	V2ExpectationPageStateEquals                 = ScenarioV2ExpectationPageStateEquals
	V2ExpectationResponseCanceled                = ScenarioV2ExpectationResponseCanceled
	V2ExpectationAssistantAudioStarted           = ScenarioV2ExpectationAssistantAudioStarted
	V2ExpectationAssistantAudioStopped           = ScenarioV2ExpectationAssistantAudioStopped
	V2ExpectationTranscriptContains              = ScenarioV2ExpectationTranscriptContains
	V2ExpectationApprovalRequested               = ScenarioV2ExpectationApprovalRequested
	V2ExpectationApprovalNotRequested            = ScenarioV2ExpectationApprovalNotRequested
	V2ExpectationStaleToolRejected               = ScenarioV2ExpectationStaleToolRejected
	V2ExpectationBrowserConnectionClosed         = ScenarioV2ExpectationBrowserConnectionClosed
)

// ScenarioV2 is a validated probe.scenario.v2 document. BrowserFixture and
// ProviderFixture retain the authored relative references. The corresponding
// *Path fields contain cleaned absolute paths under FixtureRoot when a source
// scenario path was supplied.
type ScenarioV2 struct {
	SchemaVersion   string                  `json:"schema_version"`
	ID              string                  `json:"id"`
	Name            string                  `json:"name,omitempty"`
	Description     string                  `json:"description,omitempty"`
	BrowserFixture  string                  `json:"browser_fixture,omitempty"`
	ProviderFixture string                  `json:"provider_fixture,omitempty"`
	Steps           []ScenarioV2Step        `json:"steps"`
	Expectations    []ScenarioV2Expectation `json:"expectations"`

	SourcePath          string `json:"-"`
	FixtureRoot         string `json:"-"`
	BrowserFixturePath  string `json:"-"`
	ProviderFixturePath string `json:"-"`
}

// V2Scenario is a descriptive alias for ScenarioV2.
type V2Scenario = ScenarioV2

// ScenarioV2Step is the normalized typed configuration for one v2 step.
// Optional control fields have Has* companions so false and zero remain
// distinguishable from an omitted value.
type ScenarioV2Step struct {
	Type ScenarioV2StepType `json:"type"`

	BrowserID            string `json:"browser_id,omitempty"`
	EndpointID           string `json:"endpoint_id,omitempty"`
	TargetID             string `json:"target_id,omitempty"`
	OriginContains       string `json:"origin_contains,omitempty"`
	EligibleOnly         bool   `json:"eligible_only,omitempty"`
	IncludeZeroToolPages bool   `json:"include_zero_tool_pages,omitempty"`
	Activate             bool   `json:"activate,omitempty"`
	URL                  string `json:"url,omitempty"`
	Fixture              string `json:"fixture,omitempty"`
	FixturePath          string `json:"-"`
	Refresh              bool   `json:"refresh,omitempty"`
	NameContains         string `json:"name_contains,omitempty"`
	IncludeSchemas       bool   `json:"include_schemas,omitempty"`
	FrameID              string `json:"frame_id,omitempty"`
	ToolRef              string `json:"tool_ref,omitempty"`
	InputJSON            string `json:"input_json,omitempty"`
	Reason               string `json:"reason,omitempty"`
	InvocationID         string `json:"invocation_id,omitempty"`
	CorpusID             string `json:"corpus_id,omitempty"`
	Text                 string `json:"text,omitempty"`
	AfterEvent           string `json:"after_event,omitempty"`
	DurationMS           int64  `json:"duration_ms,omitempty"`

	HasEligibleOnly         bool `json:"-"`
	HasIncludeZeroToolPages bool `json:"-"`
	HasActivate             bool `json:"-"`
	HasRefresh              bool `json:"-"`
	HasIncludeSchemas       bool `json:"-"`
	HasDurationMS           bool `json:"-"`
}

// V2Step is a descriptive alias for ScenarioV2Step.
type V2Step = ScenarioV2Step

// ScenarioV2Expectation is the normalized typed configuration for one v2
// expectation. Value and Schema retain JSON tokens as RawMessage so integers
// and page-owned structures are never converted through float64.
type ScenarioV2Expectation struct {
	Type ScenarioV2ExpectationType `json:"type"`

	BrowserID     string          `json:"browser_id,omitempty"`
	TargetID      string          `json:"target_id,omitempty"`
	Origin        string          `json:"origin,omitempty"`
	Generation    int64           `json:"generation,omitempty"`
	Name          string          `json:"name,omitempty"`
	ToolRef       string          `json:"tool_ref,omitempty"`
	Path          string          `json:"path,omitempty"`
	JSONPath      string          `json:"-"`
	Status        string          `json:"status,omitempty"`
	Text          string          `json:"text,omitempty"`
	InputJSON     string          `json:"input_json,omitempty"`
	Value         json.RawMessage `json:"value,omitempty"`
	Schema        json.RawMessage `json:"schema,omitempty"`
	Operations    []string        `json:"operations,omitempty"`
	Methods       []string        `json:"methods,omitempty"`
	Equals        int64           `json:"equals,omitempty"`
	HasGeneration bool            `json:"-"`
	HasEquals     bool            `json:"-"`
}

// V2Expectation is a descriptive alias for ScenarioV2Expectation.
type V2Expectation = ScenarioV2Expectation

type scenarioV2Object map[string]json.RawMessage

var scenarioV2StepFields = map[ScenarioV2StepType]map[string]struct{}{
	ScenarioV2StepBrowserConnect: {
		"browser_id": {}, "endpoint_id": {},
	},
	ScenarioV2StepBrowserDiscover: {
		"browser_id": {}, "origin_contains": {}, "eligible_only": {}, "include_zero_tool_pages": {},
	},
	ScenarioV2StepBrowserSelect: {
		"browser_id": {}, "target_id": {}, "activate": {},
	},
	ScenarioV2StepBrowserActivate: {
		"browser_id": {}, "target_id": {},
	},
	ScenarioV2StepBrowserDisconnect: {
		"browser_id": {},
	},
	ScenarioV2StepBrowserNavigateFixture: {
		"url": {}, "fixture": {},
	},
	ScenarioV2StepWebMCPWaitReady: {},
	ScenarioV2StepWebMCPListTools: {
		"refresh": {}, "name_contains": {}, "include_schemas": {}, "frame_id": {},
	},
	ScenarioV2StepWebMCPInvoke: {
		"tool_ref": {}, "input_json": {}, "reason": {},
	},
	ScenarioV2StepWebMCPCancel: {
		"invocation_id": {}, "reason": {},
	},
	ScenarioV2StepSendText: {
		"text": {},
	},
	ScenarioV2StepSendAudio: {
		"corpus_id": {}, "text": {},
	},
	ScenarioV2StepInterrupt: {
		"after_event": {},
	},
	ScenarioV2StepCloseTab: {
		"browser_id": {}, "target_id": {},
	},
	ScenarioV2StepOpenTab: {
		"browser_id": {}, "url": {},
	},
	ScenarioV2StepSwitchBrowser: {
		"browser_id": {},
	},
	ScenarioV2StepSleepFake: {
		"duration_ms": {},
	},
	ScenarioV2StepClose: {},
}

var scenarioV2ExpectationFields = map[ScenarioV2ExpectationType]map[string]struct{}{
	ScenarioV2ExpectationBrowserCountEquals: {
		"equals": {},
	},
	ScenarioV2ExpectationEligibleTabCountEquals: {
		"equals": {},
	},
	ScenarioV2ExpectationSelectedTabEquals: {
		"target_id": {},
	},
	ScenarioV2ExpectationSelectedOriginEquals: {
		"origin": {},
	},
	ScenarioV2ExpectationCatalogGenerationEquals: {
		"equals": {},
	},
	ScenarioV2ExpectationToolCatalogContains: {
		"name": {},
	},
	ScenarioV2ExpectationToolCatalogNotContains: {
		"name": {},
	},
	ScenarioV2ExpectationToolSchemaEquals: {
		"name": {}, "schema": {},
	},
	ScenarioV2ExpectationToolInvocationCount: {
		"name": {}, "equals": {},
	},
	ScenarioV2ExpectationToolInputJSONEquals: {
		"name": {}, "input_json": {},
	},
	ScenarioV2ExpectationToolResultJSONPathEquals: {
		"name": {}, "path": {}, "value": {},
	},
	ScenarioV2ExpectationToolStatusEquals: {
		"name": {}, "status": {},
	},
	ScenarioV2ExpectationChromeOperationOrder: {
		"operations": {},
	},
	ScenarioV2ExpectationNoUnexpectedChromeOperations: {
		"operations": {},
	},
	ScenarioV2ExpectationGeneratedCDPMethodOrder: {
		"methods": {},
	},
	ScenarioV2ExpectationNoUnexpectedGeneratedCDPMethods: {
		"methods": {},
	},
	ScenarioV2ExpectationNoPendingInvocations: {},
	ScenarioV2ExpectationPageStateEquals: {
		"path": {}, "value": {},
	},
	ScenarioV2ExpectationResponseCanceled:      {},
	ScenarioV2ExpectationAssistantAudioStarted: {},
	ScenarioV2ExpectationAssistantAudioStopped: {},
	ScenarioV2ExpectationTranscriptContains: {
		"text": {},
	},
	ScenarioV2ExpectationApprovalRequested: {
		"tool_ref": {},
	},
	ScenarioV2ExpectationApprovalNotRequested: {
		"tool_ref": {},
	},
	ScenarioV2ExpectationStaleToolRejected: {
		"tool_ref": {},
	},
	ScenarioV2ExpectationBrowserConnectionClosed: {},
}

var scenarioV2RootFields = map[string]struct{}{
	"schema_version": {}, "id": {}, "name": {}, "description": {},
	"browser_fixture": {}, "provider_fixture": {}, "steps": {}, "expectations": {},
}

// LoadScenarioV2 decodes one strict probe.scenario.v2 document. scenarioPath
// supplies the canonical containing directory used for fixture references; it
// may be empty only when the document has no fixture references. An optional
// CorpusLookup makes send_audio corpus identity validation fail before any
// execution. Supplying no lookup preserves the legacy package's ability to
// parse authored scenarios before a runtime-specific corpus is selected.
func LoadScenarioV2(input any, scenarioPath string, lookups ...CorpusLookup) (ScenarioV2, error) {
	if len(lookups) > 1 {
		return ScenarioV2{}, newScenarioV2Error("corpus_lookup", "only one corpus lookup is permitted")
	}
	data, err := readInput(input)
	if err != nil {
		return ScenarioV2{}, newScenarioV2Error("document", "%v", err)
	}
	if !utf8.Valid(data) {
		return ScenarioV2{}, newScenarioV2Error("document", "input is not valid UTF-8")
	}
	root, err := decodeScenarioV2Object(data, "scenario")
	if err != nil {
		return ScenarioV2{}, err
	}
	if err := rejectScenarioV2Fields(root, scenarioV2RootFields, "scenario"); err != nil {
		return ScenarioV2{}, err
	}

	version, err := requiredScenarioV2String(root, "scenario", "schema_version")
	if err != nil {
		return ScenarioV2{}, err
	}
	if version != ScenarioV2Version {
		return ScenarioV2{}, newScenarioV2Error("scenario.schema_version", "unsupported version")
	}
	id, err := requiredScenarioV2String(root, "scenario", "id")
	if err != nil {
		return ScenarioV2{}, err
	}
	name, err := optionalScenarioV2String(root, "scenario", "name")
	if err != nil {
		return ScenarioV2{}, err
	}
	description, err := optionalScenarioV2String(root, "scenario", "description")
	if err != nil {
		return ScenarioV2{}, err
	}
	browserFixture, err := optionalScenarioV2String(root, "scenario", "browser_fixture")
	if err != nil {
		return ScenarioV2{}, err
	}
	providerFixture, err := optionalScenarioV2String(root, "scenario", "provider_fixture")
	if err != nil {
		return ScenarioV2{}, err
	}
	if _, exists := root["browser_fixture"]; exists && strings.TrimSpace(browserFixture) == "" {
		return ScenarioV2{}, &ScenarioV2Error{Path: "scenario.browser_fixture", Cause: ErrScenarioV2FixturePath}
	}
	if _, exists := root["provider_fixture"]; exists && strings.TrimSpace(providerFixture) == "" {
		return ScenarioV2{}, &ScenarioV2Error{Path: "scenario.provider_fixture", Cause: ErrScenarioV2FixturePath}
	}

	var fixtureRoot string
	if (browserFixture != "" || providerFixture != "") && strings.TrimSpace(scenarioPath) == "" {
		return ScenarioV2{}, newScenarioV2Error("scenario", "scenario path is required when fixture references are present")
	}
	if strings.TrimSpace(scenarioPath) != "" {
		fixtureRoot, err = canonicalScenarioV2Dir(scenarioPath)
		if err != nil && (browserFixture != "" || providerFixture != "") {
			return ScenarioV2{}, wrapScenarioV2Error("scenario", err)
		}
	}

	rawSteps, ok := root["steps"]
	if !ok {
		return ScenarioV2{}, newScenarioV2Error("scenario.steps", "required field is missing")
	}
	stepValues, err := scenarioV2Array(rawSteps, "scenario.steps")
	if err != nil {
		return ScenarioV2{}, err
	}
	if len(stepValues) == 0 {
		return ScenarioV2{}, newScenarioV2Error("scenario.steps", "must contain at least one step")
	}

	rawExpectations, ok := root["expectations"]
	if !ok {
		return ScenarioV2{}, newScenarioV2Error("scenario.expectations", "required field is missing")
	}
	expectationValues, err := scenarioV2Array(rawExpectations, "scenario.expectations")
	if err != nil {
		return ScenarioV2{}, err
	}
	if len(expectationValues) == 0 {
		return ScenarioV2{}, newScenarioV2Error("scenario.expectations", "must contain at least one expectation")
	}

	var lookup CorpusLookup
	if len(lookups) == 1 {
		lookup = lookups[0]
	}
	result := ScenarioV2{
		SchemaVersion:   version,
		ID:              id,
		Name:            name,
		Description:     description,
		BrowserFixture:  browserFixture,
		ProviderFixture: providerFixture,
		SourcePath:      scenarioPath,
		FixtureRoot:     fixtureRoot,
		Steps:           make([]ScenarioV2Step, len(stepValues)),
		Expectations:    make([]ScenarioV2Expectation, len(expectationValues)),
	}
	for index, raw := range stepValues {
		result.Steps[index], err = parseScenarioV2Step(raw, index, lookup, fixtureRoot)
		if err != nil {
			return ScenarioV2{}, err
		}
	}
	for index, raw := range expectationValues {
		result.Expectations[index], err = parseScenarioV2Expectation(raw, index)
		if err != nil {
			return ScenarioV2{}, err
		}
	}

	if browserFixture != "" || providerFixture != "" {
		if browserFixture != "" {
			result.BrowserFixturePath, err = resolveScenarioV2FixturePathFromRoot(fixtureRoot, browserFixture)
			if err != nil {
				return ScenarioV2{}, wrapScenarioV2Error("scenario.browser_fixture", err)
			}
		}
		if providerFixture != "" {
			result.ProviderFixturePath, err = resolveScenarioV2FixturePathFromRoot(fixtureRoot, providerFixture)
			if err != nil {
				return ScenarioV2{}, wrapScenarioV2Error("scenario.provider_fixture", err)
			}
		}
	}
	return result, nil
}

// DecodeScenarioV2 is an alias for LoadScenarioV2.
func DecodeScenarioV2(input any, scenarioPath string, lookups ...CorpusLookup) (ScenarioV2, error) {
	return LoadScenarioV2(input, scenarioPath, lookups...)
}

// LoadProbeScenarioV2 is an alias for LoadScenarioV2.
func LoadProbeScenarioV2(input any, scenarioPath string, lookups ...CorpusLookup) (ScenarioV2, error) {
	return LoadScenarioV2(input, scenarioPath, lookups...)
}

// LoadScenarioV2File reads and validates a v2 scenario from disk. Referenced
// fixtures are resolved under the scenario's canonical containing directory;
// they are not opened until a caller explicitly asks for one.
func LoadScenarioV2File(path string, lookups ...CorpusLookup) (ScenarioV2, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ScenarioV2{}, fmt.Errorf("read probe.scenario.v2 %q: %w", path, err)
	}
	return LoadScenarioV2(data, path, lookups...)
}

// LoadProbeScenarioV2File is an alias for LoadScenarioV2File.
func LoadProbeScenarioV2File(path string, lookups ...CorpusLookup) (ScenarioV2, error) {
	return LoadScenarioV2File(path, lookups...)
}

// Validate checks a typed ScenarioV2 constructed by a caller. Documents
// decoded by LoadScenarioV2 have already passed the stricter unknown-field
// checks; this method protects the public typed seam from invalid values.
func (s ScenarioV2) Validate(lookups ...CorpusLookup) error {
	if len(lookups) > 1 {
		return newScenarioV2Error("corpus_lookup", "only one corpus lookup is permitted")
	}
	if s.SchemaVersion != ScenarioV2Version {
		return newScenarioV2Error("schema_version", "unsupported version")
	}
	if strings.TrimSpace(s.ID) == "" {
		return newScenarioV2Error("id", "must not be empty")
	}
	if len(s.Steps) == 0 {
		return newScenarioV2Error("steps", "must contain at least one step")
	}
	if len(s.Expectations) == 0 {
		return newScenarioV2Error("expectations", "must contain at least one expectation")
	}
	if s.BrowserFixture != "" || s.ProviderFixture != "" {
		if s.FixtureRoot == "" {
			return newScenarioV2Error("fixture", "scenario has no canonical fixture root")
		}
		for fieldName, reference := range map[string]string{
			"browser_fixture":  s.BrowserFixture,
			"provider_fixture": s.ProviderFixture,
		} {
			if reference == "" {
				continue
			}
			if _, err := resolveScenarioV2FixturePathFromRoot(s.FixtureRoot, reference); err != nil {
				return wrapScenarioV2Error(fieldName, err)
			}
		}
	}
	var lookup CorpusLookup
	if len(lookups) == 1 {
		lookup = lookups[0]
	}
	for index, step := range s.Steps {
		if err := validateTypedScenarioV2Step(step, index, lookup); err != nil {
			return err
		}
	}
	for index, expectation := range s.Expectations {
		if err := validateTypedScenarioV2Expectation(expectation, index); err != nil {
			return err
		}
	}
	return nil
}

// Valid reports whether a typed v2 scenario passes Validate.
func (s ScenarioV2) Valid(lookups ...CorpusLookup) bool { return s.Validate(lookups...) == nil }

// ResolveFixture resolves one authored reference using the scenario's
// canonical root. It is useful for callers that have additional fixture-like
// files but must retain the same containment policy.
func (s ScenarioV2) ResolveFixture(reference string) (string, error) {
	if s.FixtureRoot == "" {
		return "", newScenarioV2Error("fixture", "scenario has no canonical fixture root")
	}
	return resolveScenarioV2FixturePathFromRoot(s.FixtureRoot, reference)
}

// OpenBrowserFixture performs the containment check again immediately before
// opening the browser fixture. This prevents a syntactically safe reference
// from becoming an unsafe open after a symlink is introduced or changed.
func (s ScenarioV2) OpenBrowserFixture() (io.ReadCloser, error) {
	return s.openFixture(s.BrowserFixture, "browser_fixture")
}

// OpenProviderFixture performs the containment check again immediately before
// opening the provider fixture.
func (s ScenarioV2) OpenProviderFixture() (io.ReadCloser, error) {
	return s.openFixture(s.ProviderFixture, "provider_fixture")
}

func (s ScenarioV2) openFixture(reference, fieldName string) (io.ReadCloser, error) {
	if reference == "" {
		return nil, newScenarioV2Error(fieldName, "is not configured")
	}
	path, err := s.ResolveFixture(reference)
	if err != nil {
		return nil, wrapScenarioV2Error(fieldName, err)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s %q: %w", fieldName, path, err)
	}
	// Re-evaluate the path after opening. A changed symlink must never make a
	// caller believe an out-of-root target was opened as a contained fixture.
	resolved, resolveErr := resolveScenarioV2FixturePathFromRoot(s.FixtureRoot, reference)
	if resolveErr != nil || resolved != path {
		_ = file.Close()
		if resolveErr != nil {
			return nil, wrapScenarioV2Error(fieldName, resolveErr)
		}
		return nil, newScenarioV2Error(fieldName, "fixture target changed during open")
	}
	return file, nil
}

// ResolveScenarioV2FixturePath resolves reference relative to the canonical
// containing directory of scenarioPath. It validates and resolves the path,
// but does not open or parse the target.
func ResolveScenarioV2FixturePath(scenarioPath, reference string) (string, error) {
	root, err := canonicalScenarioV2Dir(scenarioPath)
	if err != nil {
		return "", wrapScenarioV2Error("scenario", err)
	}
	return resolveScenarioV2FixturePathFromRoot(root, reference)
}

// ResolveScenarioFixturePath is a descriptive alias for
// ResolveScenarioV2FixturePath.
func ResolveScenarioFixturePath(scenarioPath, reference string) (string, error) {
	return ResolveScenarioV2FixturePath(scenarioPath, reference)
}

// OpenScenarioV2Fixture resolves and opens one contained fixture reference.
func OpenScenarioV2Fixture(scenarioPath, reference string) (io.ReadCloser, error) {
	path, err := ResolveScenarioV2FixturePath(scenarioPath, reference)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open fixture %q: %w", path, err)
	}
	resolved, resolveErr := ResolveScenarioV2FixturePath(scenarioPath, reference)
	if resolveErr != nil || resolved != path {
		_ = file.Close()
		if resolveErr != nil {
			return nil, resolveErr
		}
		return nil, newScenarioV2Error("fixture", "fixture target changed during open")
	}
	return file, nil
}

func decodeScenarioV2Object(data []byte, location string) (scenarioV2Object, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, newScenarioV2Error(location, "malformed JSON: %v", err)
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return nil, newScenarioV2Error(location, "must be a JSON object")
	}
	result := make(scenarioV2Object)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, newScenarioV2Error(location, "malformed JSON: %v", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, newScenarioV2Error(location, "object key must be a string")
		}
		if _, exists := result[key]; exists {
			return nil, newScenarioV2Error(location+"."+key, "duplicate field")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, newScenarioV2Error(location+"."+key, "malformed JSON: %v", err)
		}
		result[key] = cloneScenarioV2Raw(raw)
	}
	end, err := decoder.Token()
	if err != nil {
		return nil, newScenarioV2Error(location, "malformed JSON: %v", err)
	}
	if delim, ok := end.(json.Delim); !ok || delim != '}' {
		return nil, newScenarioV2Error(location, "malformed JSON object")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, newScenarioV2Error(location, "trailing JSON document")
		}
		return nil, newScenarioV2Error(location, "malformed trailing JSON: %v", err)
	}
	return result, nil
}

func rejectScenarioV2Fields(value scenarioV2Object, allowed map[string]struct{}, location string) error {
	unknown := make([]string, 0)
	for key := range value {
		if _, ok := allowed[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return newScenarioV2Error(location+"."+unknown[0], "unknown field")
}

func scenarioV2VariantFields(fields map[string]struct{}) map[string]struct{} {
	allowed := make(map[string]struct{}, len(fields)+1)
	allowed["type"] = struct{}{}
	for fieldName := range fields {
		allowed[fieldName] = struct{}{}
	}
	return allowed
}

func scenarioV2Array(raw json.RawMessage, location string) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, newScenarioV2Error(location, "must be a JSON array")
	}
	var values []json.RawMessage
	if err := json.Unmarshal(trimmed, &values); err != nil || values == nil {
		if err == nil {
			err = errors.New("must be a JSON array")
		}
		return nil, newScenarioV2Error(location, "%v", err)
	}
	for index := range values {
		values[index] = cloneScenarioV2Raw(values[index])
	}
	return values, nil
}

func requiredScenarioV2String(value scenarioV2Object, location, fieldName string) (string, error) {
	raw, ok := value[fieldName]
	if !ok {
		return "", newScenarioV2Error(location+"."+fieldName, "required field is missing")
	}
	result, err := scenarioV2String(raw, location+"."+fieldName)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(result) == "" {
		return "", newScenarioV2Error(location+"."+fieldName, "must not be empty")
	}
	return result, nil
}

func optionalScenarioV2String(value scenarioV2Object, location, fieldName string) (string, error) {
	raw, ok := value[fieldName]
	if !ok {
		return "", nil
	}
	return scenarioV2String(raw, location+"."+fieldName)
}

func scenarioV2String(raw json.RawMessage, location string) (string, error) {
	var result string
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &result) != nil {
		return "", newScenarioV2Error(location, "must be a string")
	}
	if !utf8.ValidString(result) {
		return "", newScenarioV2Error(location, "must be valid UTF-8")
	}
	return result, nil
}

func optionalScenarioV2JSON(value scenarioV2Object, location, fieldName string, objectOnly bool) (json.RawMessage, bool, error) {
	raw, ok := value[fieldName]
	if !ok {
		return nil, false, nil
	}
	result, err := scenarioV2JSON(raw, location+"."+fieldName, objectOnly)
	return result, true, err
}

func scenarioV2JSON(raw json.RawMessage, location string, objectOnly bool) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return nil, newScenarioV2Error(location, "must be a JSON value")
	}
	if objectOnly {
		var object map[string]json.RawMessage
		if json.Unmarshal(trimmed, &object) != nil || object == nil {
			return nil, newScenarioV2Error(location, "must be a JSON object")
		}
	}
	return cloneScenarioV2Raw(trimmed), nil
}

func optionalScenarioV2Bool(value scenarioV2Object, location, fieldName string) (bool, bool, error) {
	raw, ok := value[fieldName]
	if !ok {
		return false, false, nil
	}
	result, err := scenarioV2Bool(raw, location+"."+fieldName)
	return result, true, err
}

func scenarioV2Bool(raw json.RawMessage, location string) (bool, error) {
	var result bool
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &result) != nil {
		return false, newScenarioV2Error(location, "must be a boolean")
	}
	return result, nil
}

func scenarioV2Int(raw json.RawMessage, location string) (int64, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return 0, newScenarioV2Error(location, "must be an integer")
	}
	value, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return 0, newScenarioV2Error(location, "must be an integer")
	}
	return value, nil
}

func optionalScenarioV2StringArray(value scenarioV2Object, location, fieldName string) ([]string, bool, error) {
	raw, ok := value[fieldName]
	if !ok {
		return nil, false, nil
	}
	result, err := scenarioV2StringArray(raw, location+"."+fieldName)
	return result, true, err
}

func scenarioV2StringArray(raw json.RawMessage, location string) ([]string, error) {
	values, err := scenarioV2Array(raw, location)
	if err != nil {
		return nil, err
	}
	result := make([]string, len(values))
	for index, value := range values {
		result[index], err = scenarioV2String(value, fmt.Sprintf("%s[%d]", location, index))
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(result[index]) == "" {
			return nil, newScenarioV2Error(fmt.Sprintf("%s[%d]", location, index), "must not be empty")
		}
	}
	return result, nil
}

func parseScenarioV2Step(raw json.RawMessage, index int, lookup CorpusLookup, fixtureRoot string) (ScenarioV2Step, error) {
	location := fmt.Sprintf("scenario.steps[%d]", index)
	value, err := decodeScenarioV2Object(raw, location)
	if err != nil {
		return ScenarioV2Step{}, err
	}
	typeName, err := requiredScenarioV2String(value, location, "type")
	if err != nil {
		return ScenarioV2Step{}, err
	}
	stepType := ScenarioV2StepType(typeName)
	allowed, ok := scenarioV2StepFields[stepType]
	if !ok {
		return ScenarioV2Step{}, newScenarioV2Error(location+".type", "unknown step variant")
	}
	if err := rejectScenarioV2Fields(value, scenarioV2VariantFields(allowed), location); err != nil {
		return ScenarioV2Step{}, err
	}
	step := ScenarioV2Step{Type: stepType}
	for fieldName, destination := range map[string]*string{
		"browser_id": &step.BrowserID, "endpoint_id": &step.EndpointID,
		"target_id": &step.TargetID, "origin_contains": &step.OriginContains,
		"url": &step.URL, "fixture": &step.Fixture, "name_contains": &step.NameContains,
		"frame_id": &step.FrameID, "tool_ref": &step.ToolRef, "input_json": &step.InputJSON,
		"reason": &step.Reason, "invocation_id": &step.InvocationID, "corpus_id": &step.CorpusID,
		"text": &step.Text, "after_event": &step.AfterEvent,
	} {
		if value[fieldName] == nil {
			continue
		}
		parsed, parseErr := scenarioV2String(value[fieldName], location+"."+fieldName)
		if parseErr != nil {
			return ScenarioV2Step{}, parseErr
		}
		*destination = parsed
	}
	var has bool
	if step.EligibleOnly, has, err = optionalScenarioV2Bool(value, location, "eligible_only"); err != nil {
		return ScenarioV2Step{}, err
	}
	step.HasEligibleOnly = has
	if step.IncludeZeroToolPages, has, err = optionalScenarioV2Bool(value, location, "include_zero_tool_pages"); err != nil {
		return ScenarioV2Step{}, err
	}
	step.HasIncludeZeroToolPages = has
	if step.Activate, has, err = optionalScenarioV2Bool(value, location, "activate"); err != nil {
		return ScenarioV2Step{}, err
	}
	step.HasActivate = has
	if step.Refresh, has, err = optionalScenarioV2Bool(value, location, "refresh"); err != nil {
		return ScenarioV2Step{}, err
	}
	step.HasRefresh = has
	if step.IncludeSchemas, has, err = optionalScenarioV2Bool(value, location, "include_schemas"); err != nil {
		return ScenarioV2Step{}, err
	}
	step.HasIncludeSchemas = has
	if rawDuration, exists := value["duration_ms"]; exists {
		step.DurationMS, err = scenarioV2Int(rawDuration, location+".duration_ms")
		if err != nil {
			return ScenarioV2Step{}, err
		}
		if step.DurationMS < 0 {
			return ScenarioV2Step{}, newScenarioV2Error(location+".duration_ms", "must not be negative")
		}
		step.HasDurationMS = true
	}
	if err := validateScenarioV2StepRequiredFields(step, location, lookup); err != nil {
		return ScenarioV2Step{}, err
	}
	if step.Fixture != "" {
		if fixtureRoot == "" {
			return ScenarioV2Step{}, newScenarioV2Error(location+".fixture", "scenario path is required for fixture references")
		}
		step.FixturePath, err = resolveScenarioV2FixturePathFromRoot(fixtureRoot, step.Fixture)
		if err != nil {
			return ScenarioV2Step{}, wrapScenarioV2Error(location+".fixture", err)
		}
	}
	return step, nil
}

func validateScenarioV2StepRequiredFields(step ScenarioV2Step, location string, lookup CorpusLookup) error {
	nonEmpty := func(value, fieldName string) error {
		if strings.TrimSpace(value) == "" {
			return newScenarioV2Error(location+"."+fieldName, "required field is missing")
		}
		return nil
	}
	if step.ToolRef != "" {
		if err := validateScenarioV2ToolRef(step.ToolRef, location+".tool_ref"); err != nil {
			return err
		}
	}
	if step.HasDurationMS && step.DurationMS < 0 {
		return newScenarioV2Error(location+".duration_ms", "must not be negative")
	}
	switch step.Type {
	case ScenarioV2StepBrowserSelect:
		return nonEmpty(step.TargetID, "target_id")
	case ScenarioV2StepBrowserNavigateFixture:
		if step.URL == "" && step.Fixture == "" {
			return newScenarioV2Error(location, "url or fixture is required")
		}
		if step.URL != "" && step.Fixture != "" {
			return newScenarioV2Error(location, "url and fixture are mutually exclusive")
		}
	case ScenarioV2StepWebMCPInvoke:
		if err := nonEmpty(step.ToolRef, "tool_ref"); err != nil {
			return err
		}
		if err := nonEmpty(step.InputJSON, "input_json"); err != nil {
			return err
		}
		if err := nonEmpty(step.Reason, "reason"); err != nil {
			return err
		}
		if _, err := decodeScenarioV2Object([]byte(step.InputJSON), location+".input_json"); err != nil {
			return newScenarioV2Error(location+".input_json", "must contain a JSON object")
		}
	case ScenarioV2StepWebMCPCancel:
		return nonEmpty(step.InvocationID, "invocation_id")
	case ScenarioV2StepSendText:
		return nonEmpty(step.Text, "text")
	case ScenarioV2StepSendAudio:
		if err := nonEmpty(step.CorpusID, "corpus_id"); err != nil {
			return err
		}
		if lookup != nil && !lookup.Has(step.CorpusID) {
			return &ScenarioV2Error{Path: location + ".corpus_id", Cause: errors.Join(ErrScenarioV2UnknownCorpus, fmt.Errorf("corpus is not registered"))}
		}
	case ScenarioV2StepInterrupt:
		return nonEmpty(step.AfterEvent, "after_event")
	case ScenarioV2StepOpenTab:
		return nonEmpty(step.URL, "url")
	case ScenarioV2StepSwitchBrowser:
		return nonEmpty(step.BrowserID, "browser_id")
	case ScenarioV2StepSleepFake:
		if !step.HasDurationMS {
			return newScenarioV2Error(location+".duration_ms", "required field is missing")
		}
	}
	return nil
}

func parseScenarioV2Expectation(raw json.RawMessage, index int) (ScenarioV2Expectation, error) {
	location := fmt.Sprintf("scenario.expectations[%d]", index)
	value, err := decodeScenarioV2Object(raw, location)
	if err != nil {
		return ScenarioV2Expectation{}, err
	}
	typeName, err := requiredScenarioV2String(value, location, "type")
	if err != nil {
		return ScenarioV2Expectation{}, err
	}
	expectationType := ScenarioV2ExpectationType(typeName)
	allowed, ok := scenarioV2ExpectationFields[expectationType]
	if !ok {
		return ScenarioV2Expectation{}, newScenarioV2Error(location+".type", "unknown expectation variant")
	}
	if err := rejectScenarioV2Fields(value, scenarioV2VariantFields(allowed), location); err != nil {
		return ScenarioV2Expectation{}, err
	}
	expectation := ScenarioV2Expectation{Type: expectationType}
	for fieldName, destination := range map[string]*string{
		"browser_id": &expectation.BrowserID, "target_id": &expectation.TargetID,
		"origin": &expectation.Origin, "name": &expectation.Name, "tool_ref": &expectation.ToolRef,
		"path": &expectation.Path, "status": &expectation.Status, "text": &expectation.Text,
	} {
		if value[fieldName] == nil {
			continue
		}
		parsed, parseErr := scenarioV2String(value[fieldName], location+"."+fieldName)
		if parseErr != nil {
			return ScenarioV2Expectation{}, parseErr
		}
		*destination = parsed
	}
	expectation.JSONPath = expectation.Path
	if expectation.Type == ScenarioV2ExpectationToolResultJSONPathEquals || expectation.Type == ScenarioV2ExpectationPageStateEquals {
		if strings.TrimSpace(expectation.Path) == "" {
			return ScenarioV2Expectation{}, newScenarioV2Error(location+".path", "must not be empty")
		}
		if !strings.HasPrefix(expectation.Path, "$") {
			return ScenarioV2Expectation{}, newScenarioV2Error(location+".path", "must be a JSONPath beginning with $")
		}
	}
	if rawValue, exists, valueErr := optionalScenarioV2JSON(value, location, "value", false); valueErr != nil {
		return ScenarioV2Expectation{}, valueErr
	} else if exists {
		expectation.Value = rawValue
	}
	if rawSchema, exists, schemaErr := optionalScenarioV2JSON(value, location, "schema", true); schemaErr != nil {
		return ScenarioV2Expectation{}, schemaErr
	} else if exists {
		expectation.Schema = rawSchema
	}
	if expectation.Type == ScenarioV2ExpectationToolSchemaEquals && len(expectation.Schema) == 0 {
		return ScenarioV2Expectation{}, newScenarioV2Error(location+".schema", "required field is missing")
	}
	if expectation.Type == ScenarioV2ExpectationToolResultJSONPathEquals || expectation.Type == ScenarioV2ExpectationPageStateEquals {
		if len(expectation.Value) == 0 {
			return ScenarioV2Expectation{}, newScenarioV2Error(location+".value", "required field is missing")
		}
	}
	if rawInput, exists := value["input_json"]; exists {
		expectation.InputJSON, err = scenarioV2String(rawInput, location+".input_json")
		if err != nil {
			return ScenarioV2Expectation{}, err
		}
		if _, err := decodeScenarioV2Object([]byte(expectation.InputJSON), location+".input_json"); err != nil {
			return ScenarioV2Expectation{}, newScenarioV2Error(location+".input_json", "must contain a JSON object")
		}
	}
	if rawOperations, exists, arrayErr := optionalScenarioV2StringArray(value, location, "operations"); arrayErr != nil {
		return ScenarioV2Expectation{}, arrayErr
	} else if exists {
		expectation.Operations = rawOperations
	}
	if rawMethods, exists, arrayErr := optionalScenarioV2StringArray(value, location, "methods"); arrayErr != nil {
		return ScenarioV2Expectation{}, arrayErr
	} else if exists {
		expectation.Methods = rawMethods
	}
	if rawEquals, exists := value["equals"]; exists {
		expectation.Equals, err = scenarioV2Int(rawEquals, location+".equals")
		if err != nil {
			return ScenarioV2Expectation{}, err
		}
		if expectation.Equals < 0 {
			return ScenarioV2Expectation{}, newScenarioV2Error(location+".equals", "must not be negative")
		}
		expectation.HasEquals = true
	}
	if err := validateScenarioV2ExpectationRequiredFields(expectation, location); err != nil {
		return ScenarioV2Expectation{}, err
	}
	return expectation, nil
}

func validateScenarioV2ExpectationRequiredFields(expectation ScenarioV2Expectation, location string) error {
	nonEmpty := func(value, fieldName string) error {
		if strings.TrimSpace(value) == "" {
			return newScenarioV2Error(location+"."+fieldName, "required field is missing")
		}
		return nil
	}
	if expectation.ToolRef != "" {
		if err := validateScenarioV2ToolRef(expectation.ToolRef, location+".tool_ref"); err != nil {
			return err
		}
	}
	if expectation.HasEquals && expectation.Equals < 0 {
		return newScenarioV2Error(location+".equals", "must not be negative")
	}
	if expectation.Type == ScenarioV2ExpectationBrowserCountEquals ||
		expectation.Type == ScenarioV2ExpectationEligibleTabCountEquals ||
		expectation.Type == ScenarioV2ExpectationCatalogGenerationEquals ||
		expectation.Type == ScenarioV2ExpectationToolInvocationCount {
		if !expectation.HasEquals {
			return newScenarioV2Error(location+".equals", "required field is missing")
		}
	}
	switch expectation.Type {
	case ScenarioV2ExpectationSelectedTabEquals:
		return nonEmpty(expectation.TargetID, "target_id")
	case ScenarioV2ExpectationSelectedOriginEquals:
		return nonEmpty(expectation.Origin, "origin")
	case ScenarioV2ExpectationToolCatalogContains, ScenarioV2ExpectationToolCatalogNotContains,
		ScenarioV2ExpectationToolInvocationCount:
		return nonEmpty(expectation.Name, "name")
	case ScenarioV2ExpectationToolInputJSONEquals:
		if err := nonEmpty(expectation.Name, "name"); err != nil {
			return err
		}
		if err := nonEmpty(expectation.InputJSON, "input_json"); err != nil {
			return err
		}
		if _, err := decodeScenarioV2Object([]byte(expectation.InputJSON), location+".input_json"); err != nil {
			return newScenarioV2Error(location+".input_json", "must contain a JSON object")
		}
	case ScenarioV2ExpectationToolStatusEquals:
		if err := nonEmpty(expectation.Name, "name"); err != nil {
			return err
		}
		return nonEmpty(expectation.Status, "status")
	case ScenarioV2ExpectationToolSchemaEquals:
		if err := nonEmpty(expectation.Name, "name"); err != nil {
			return err
		}
		if len(expectation.Schema) == 0 {
			return newScenarioV2Error(location+".schema", "required field is missing")
		}
		if _, err := scenarioV2JSON(expectation.Schema, location+".schema", true); err != nil {
			return err
		}
	case ScenarioV2ExpectationToolResultJSONPathEquals, ScenarioV2ExpectationPageStateEquals:
		if strings.TrimSpace(expectation.Path) == "" || !strings.HasPrefix(expectation.Path, "$") {
			return newScenarioV2Error(location+".path", "must be a JSONPath beginning with $")
		}
		if len(expectation.Value) == 0 {
			return newScenarioV2Error(location+".value", "required field is missing")
		}
		if _, err := scenarioV2JSON(expectation.Value, location+".value", false); err != nil {
			return err
		}
	case ScenarioV2ExpectationChromeOperationOrder, ScenarioV2ExpectationNoUnexpectedChromeOperations:
		if expectation.Operations == nil {
			return newScenarioV2Error(location+".operations", "required field is missing")
		}
	case ScenarioV2ExpectationGeneratedCDPMethodOrder, ScenarioV2ExpectationNoUnexpectedGeneratedCDPMethods:
		if expectation.Methods == nil {
			return newScenarioV2Error(location+".methods", "required field is missing")
		}
	case ScenarioV2ExpectationTranscriptContains:
		return nonEmpty(expectation.Text, "text")
	case ScenarioV2ExpectationStaleToolRejected:
		if expectation.ToolRef != "" {
			return nil
		}
	}
	return nil
}

func validateScenarioV2ToolRef(value, location string) error {
	const prefix = "webmcp.tool-ref.v1:"
	const tokenLength = 22
	if !strings.HasPrefix(value, prefix) || len(value)-len(prefix) != tokenLength {
		return newScenarioV2Error(location, "must use the webmcp.tool-ref.v1 grammar")
	}
	for _, character := range value[len(prefix):] {
		if !((character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-') {
			return newScenarioV2Error(location, "must use the webmcp.tool-ref.v1 grammar")
		}
	}
	return nil
}

func validateTypedScenarioV2Step(step ScenarioV2Step, index int, lookup CorpusLookup) error {
	if _, ok := scenarioV2StepFields[step.Type]; !ok {
		return newScenarioV2Error(fmt.Sprintf("steps[%d].type", index), "unknown step variant")
	}
	return validateScenarioV2StepRequiredFields(step, fmt.Sprintf("steps[%d]", index), lookup)
}

func validateTypedScenarioV2Expectation(expectation ScenarioV2Expectation, index int) error {
	if _, ok := scenarioV2ExpectationFields[expectation.Type]; !ok {
		return newScenarioV2Error(fmt.Sprintf("expectations[%d].type", index), "unknown expectation variant")
	}
	return validateScenarioV2ExpectationRequiredFields(expectation, fmt.Sprintf("expectations[%d]", index))
}

func cloneScenarioV2Raw(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}
