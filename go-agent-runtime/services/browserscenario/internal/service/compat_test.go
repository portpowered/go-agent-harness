package service

import (
	"encoding/json"
	"io"
	"strings"
	"time"

	browserscenario "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserscenario"
)

func NewBrowserConversationScenario(scenario BrowserConversationScenario) (BrowserConversationScenario, error) {
	return scenario.Admit()
}

func NewBrowserScenario(scenario BrowserScenario) (BrowserScenario, error) {
	return scenario.Admit()
}

func NewWebMCPConversationScenario(scenario WebMCPConversationScenario) (WebMCPConversationScenario, error) {
	return scenario.Admit()
}

func NewBrowserConversationScenarioValue(scenario BrowserConversationScenario) (BrowserConversationScenarioValue, error) {
	return scenario.ScenarioValue()
}

func NewBrowserConversationRun(scenario BrowserConversationScenario) (*BrowserConversationRun, error) {
	return scenario.NewRun()
}

func NewBrowserScenarioRun(scenario BrowserScenario) (*BrowserConversationRun, error) {
	return scenario.NewRun()
}

func BrowserConversationValidatorRubric() []string {
	return (browserscenario.BrowserConversationValidatorRubric{}).Values()
}

func ComputeBrowserConversationInputJSONValidity(calls []BrowserConversationBrokerCall) BrowserConversationInputJSONValidity {
	return browserscenario.BrowserConversationTrace(calls).InputJSONValidity()
}

func SanitizeBrowserConversationResult(result BrowserConversationResult) BrowserConversationResult {
	return result.Sanitized()
}

func NewBrowserConversationReport(result BrowserConversationResult, metadata BrowserConversationReportMetadata) (BrowserConversationReport, error) {
	return result.Report(metadata)
}

func NewBrowserConversationValidatorInput(result BrowserConversationResult) (BrowserConversationValidatorInput, error) {
	return result.ValidatorInput()
}

func MarshalBrowserConversationReport(report BrowserConversationReport) ([]byte, error) {
	return report.Marshal()
}

func RenderBrowserConversationReport(result BrowserConversationResult, metadata BrowserConversationReportMetadata) (string, error) {
	return result.RenderReport(metadata)
}

func WriteBrowserConversationReport(out io.Writer, result BrowserConversationResult, metadata BrowserConversationReportMetadata) error {
	return result.WriteReport(out, metadata)
}

func NewBrowserConversationCommandValidator(command []string, timeout time.Duration) (*commandValidator, error) {
	validator, err := NewCommandValidator(command, timeout)
	if err != nil {
		return nil, err
	}
	return validator.(*commandValidator), nil
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
