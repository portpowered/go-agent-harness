package agentruntime

// Report compatibility is an adapter for the unchanged session runner and
// its historical tests. Sanitization and bounded command execution are owned
// by go-agent-runtime/services/browserscenario.
// Deprecated: use go-agent-runtime/services/browserscenario through its
// injected service contract instead.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	runtimeBrowser "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserscenario"
	browserScenarioWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserscenario/wire"
)

const (
	BrowserConversationReportVersion         = runtimeBrowser.BrowserConversationReportVersion
	BrowserConversationValidatorInputVersion = runtimeBrowser.BrowserConversationValidatorInputVersion
)

type BrowserConversationReportMetadata struct {
	Command            string   `json:"command,omitempty"`
	Configuration      string   `json:"configuration,omitempty"`
	DependencyBaseline []string `json:"dependency_baseline,omitempty"`
	Provider           string   `json:"provider,omitempty"`
	Model              string   `json:"model,omitempty"`
	BrowserChannel     string   `json:"browser_channel,omitempty"`
	BrowserVersion     string   `json:"browser_version,omitempty"`
	BrowserRevision    string   `json:"browser_revision,omitempty"`
	PR269Status        string   `json:"pr_269_status,omitempty"`
	LaneIBranch        string   `json:"lane_i_branch,omitempty"`
	LaneIPullRequest   string   `json:"lane_i_pull_request,omitempty"`
}

type BrowserConversationReport struct {
	Version  string                            `json:"version"`
	Metadata BrowserConversationReportMetadata `json:"metadata"`
	Rubric   []string                          `json:"validator_rubric"`
	Evidence BrowserConversationResult         `json:"evidence"`
}

type BrowserConversationValidatorInput struct {
	Version  string                    `json:"version"`
	Rubric   []string                  `json:"rubric"`
	Evidence BrowserConversationResult `json:"evidence"`
}

func BrowserConversationValidatorRubric() []string {
	return (runtimeBrowser.BrowserConversationValidatorRubric{}).Values()
}

func ComputeBrowserConversationInputJSONValidity(calls []BrowserConversationBrokerCall) BrowserConversationInputJSONValidity {
	converted := make([]runtimeBrowser.BrowserConversationBrokerCall, 0, len(calls))
	for _, call := range calls {
		converted = append(converted, legacyBrokerCallToRuntime(call))
	}
	return runtimeValidityToLegacy(browserScenarioWire.NewService().ComputeInputJSONValidity(converted))
}

func computeBrowserConversationInputJSONValidity(calls []BrowserConversationBrokerCall) BrowserConversationInputJSONValidity {
	return ComputeBrowserConversationInputJSONValidity(calls)
}

func browserConversationJSONStringObject(value string) bool {
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return false
	}
	if _, ok := decoded.(map[string]any); !ok {
		return false
	}
	var extra any
	return decoder.Decode(&extra) == io.EOF
}

func SanitizeBrowserConversationResult(result BrowserConversationResult) BrowserConversationResult {
	converted, err := legacyResultToRuntime(result)
	if err != nil {
		return result
	}
	clean := browserScenarioWire.NewService().SanitizeResult(converted)
	return runtimeResultToLegacy(clean)
}

func NewBrowserConversationReport(result BrowserConversationResult, metadata BrowserConversationReportMetadata) (BrowserConversationReport, error) {
	converted, err := legacyResultToRuntime(result)
	if err != nil {
		return BrowserConversationReport{}, err
	}
	report, err := browserScenarioWire.NewService().NewReport(converted, legacyMetadataToRuntime(metadata))
	if err != nil {
		return BrowserConversationReport{}, legacyBrowserResultError(err)
	}
	return runtimeReportToLegacy(report), nil
}

func NewBrowserConversationValidatorInput(result BrowserConversationResult) (BrowserConversationValidatorInput, error) {
	converted, err := legacyResultToRuntime(result)
	if err != nil {
		return BrowserConversationValidatorInput{}, err
	}
	input, err := browserScenarioWire.NewService().NewValidatorInput(converted)
	if err != nil {
		return BrowserConversationValidatorInput{}, legacyBrowserResultError(err)
	}
	return runtimeValidatorInputToLegacy(input), nil
}

func MarshalBrowserConversationReport(report BrowserConversationReport) ([]byte, error) {
	return json.MarshalIndent(report, "", "  ")
}

func RenderBrowserConversationReport(result BrowserConversationResult, metadata BrowserConversationReportMetadata) (string, error) {
	report, err := NewBrowserConversationReport(result, metadata)
	if err != nil {
		return "", err
	}
	encoded, err := MarshalBrowserConversationReport(report)
	if err != nil {
		return "", err
	}
	return "## WebMCP conversational customer simulation\n\nSanitized reproducibility metadata and complete evidence:\n\n```json\n" + string(encoded) + "\n```\n", nil
}

func WriteBrowserConversationReport(out io.Writer, result BrowserConversationResult, metadata BrowserConversationReportMetadata) error {
	if out == nil {
		return errors.New("browser conversation report writer is nil")
	}
	report, err := RenderBrowserConversationReport(result, metadata)
	if err != nil {
		return err
	}
	_, err = io.WriteString(out, report)
	return err
}

type BrowserConversationCommandValidator struct {
	Command []string
	Dir     string
	Env     []string
	Timeout time.Duration
}

func NewBrowserConversationCommandValidator(command []string, timeout time.Duration) (*BrowserConversationCommandValidator, error) {
	if _, err := browserScenarioWire.NewService().NewCommandValidator(runtimeBrowser.BrowserConversationValidatorCommand{Command: command, Timeout: timeout}); err != nil {
		return nil, err
	}
	return &BrowserConversationCommandValidator{Command: append([]string(nil), command...), Timeout: timeout}, nil
}

func (v *BrowserConversationCommandValidator) ValidateBrowserConversation(result BrowserConversationResult) (BrowserConversationValidatorVerdict, error) {
	if v == nil {
		return BrowserConversationValidatorVerdict{}, errors.New("browser conversation command validator is nil")
	}
	validator, err := browserScenarioWire.NewService().NewCommandValidator(runtimeBrowser.BrowserConversationValidatorCommand{Command: v.Command, Dir: v.Dir, Env: v.Env, Timeout: v.Timeout})
	if err != nil {
		return BrowserConversationValidatorVerdict{}, err
	}
	converted, err := legacyResultToRuntime(result)
	if err != nil {
		return BrowserConversationValidatorVerdict{}, err
	}
	verdict, err := validator.ValidateBrowserConversation(converted)
	if err != nil {
		return BrowserConversationValidatorVerdict{}, err
	}
	return runtimeVerdictToLegacy(verdict), nil
}

func legacyMetadataToRuntime(metadata BrowserConversationReportMetadata) runtimeBrowser.BrowserConversationReportMetadata {
	return runtimeBrowser.BrowserConversationReportMetadata{
		Command: metadata.Command, Configuration: metadata.Configuration, DependencyBaseline: append([]string(nil), metadata.DependencyBaseline...),
		Provider: metadata.Provider, Model: metadata.Model, BrowserChannel: metadata.BrowserChannel, BrowserVersion: metadata.BrowserVersion,
		BrowserRevision: metadata.BrowserRevision, PR269Status: metadata.PR269Status, LaneIBranch: metadata.LaneIBranch, LaneIPullRequest: metadata.LaneIPullRequest,
	}
}

func legacyResultToRuntime(result BrowserConversationResult) (runtimeBrowser.BrowserConversationResult, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return runtimeBrowser.BrowserConversationResult{}, err
	}
	var converted runtimeBrowser.BrowserConversationResult
	if err := json.Unmarshal(encoded, &converted); err != nil {
		return runtimeBrowser.BrowserConversationResult{}, err
	}
	return converted, nil
}

func runtimeResultToLegacy(result runtimeBrowser.BrowserConversationResult) BrowserConversationResult {
	encoded, err := json.Marshal(result)
	if err != nil {
		return BrowserConversationResult{}
	}
	var converted BrowserConversationResult
	if err := json.Unmarshal(encoded, &converted); err != nil {
		return BrowserConversationResult{}
	}
	return converted
}

func legacyBrokerCallToRuntime(call BrowserConversationBrokerCall) runtimeBrowser.BrowserConversationBrokerCall {
	encoded, err := json.Marshal(call)
	if err != nil {
		return runtimeBrowser.BrowserConversationBrokerCall{}
	}
	var converted runtimeBrowser.BrowserConversationBrokerCall
	if err := json.Unmarshal(encoded, &converted); err != nil {
		return runtimeBrowser.BrowserConversationBrokerCall{}
	}
	return converted
}

func runtimeValidityToLegacy(value runtimeBrowser.BrowserConversationInputJSONValidity) BrowserConversationInputJSONValidity {
	encoded, err := json.Marshal(value)
	if err != nil {
		return BrowserConversationInputJSONValidity{}
	}
	var converted BrowserConversationJSONValidityCompat
	if err := json.Unmarshal(encoded, &converted); err != nil {
		return BrowserConversationInputJSONValidity{}
	}
	return converted.value()
}

type BrowserConversationJSONValidityCompat struct {
	ValidObjectStrings int                                    `json:"valid_object_strings"`
	TotalAttempts      int                                    `json:"total_attempts"`
	Percentage         float64                                `json:"percentage"`
	Attempts           []BrowserConversationJSONAttemptCompat `json:"attempts,omitempty"`
}

type BrowserConversationJSONAttemptCompat struct {
	Sequence     uint64 `json:"sequence"`
	StepID       string `json:"step_id,omitempty"`
	InvocationID any    `json:"invocation_id,omitempty"`
	ToolRef      any    `json:"tool_ref,omitempty"`
	ToolName     string `json:"tool_name,omitempty"`
	State        any    `json:"state,omitempty"`
	Terminal     bool   `json:"terminal"`
	InputJSON    string `json:"input_json"`
	ValidObject  bool   `json:"valid_object"`
}

func (v BrowserConversationJSONValidityCompat) value() BrowserConversationInputJSONValidity {
	result := BrowserConversationInputJSONValidity{ValidObjectStrings: v.ValidObjectStrings, TotalAttempts: v.TotalAttempts, Percentage: v.Percentage}
	for _, attempt := range v.Attempts {
		encoded, err := json.Marshal(attempt)
		if err != nil {
			continue
		}
		var converted BrowserConversationInputJSONAttempt
		if err := json.Unmarshal(encoded, &converted); err != nil {
			continue
		}
		result.Attempts = append(result.Attempts, converted)
	}
	return result
}

func runtimeReportToLegacy(report runtimeBrowser.BrowserConversationReport) BrowserConversationReport {
	encoded, err := json.Marshal(report)
	if err != nil {
		return BrowserConversationReport{}
	}
	var converted BrowserConversationReport
	if err := json.Unmarshal(encoded, &converted); err != nil {
		return BrowserConversationReport{}
	}
	return converted
}

func runtimeValidatorInputToLegacy(input runtimeBrowser.BrowserConversationValidatorInput) BrowserConversationValidatorInput {
	encoded, err := json.Marshal(input)
	if err != nil {
		return BrowserConversationValidatorInput{}
	}
	var converted BrowserConversationValidatorInput
	if err := json.Unmarshal(encoded, &converted); err != nil {
		return BrowserConversationValidatorInput{}
	}
	return converted
}

func runtimeVerdictToLegacy(verdict runtimeBrowser.BrowserConversationValidatorVerdict) BrowserConversationValidatorVerdict {
	encoded, err := json.Marshal(verdict)
	if err != nil {
		return BrowserConversationValidatorVerdict{}
	}
	var converted BrowserConversationValidatorVerdict
	if err := json.Unmarshal(encoded, &converted); err != nil {
		return BrowserConversationValidatorVerdict{}
	}
	return converted
}

func legacyBrowserResultError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, runtimeBrowser.ErrInvalidBrowserConversationResult) {
		return fmt.Errorf("%w: %w", ErrInvalidBrowserConversationResult, err)
	}
	return err
}

var _ BrowserConversationValidator = (*BrowserConversationCommandValidator)(nil)
