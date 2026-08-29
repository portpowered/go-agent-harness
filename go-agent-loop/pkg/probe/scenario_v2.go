package probe

import (
	"encoding/json"
	"errors"
	"fmt"
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
