package service

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
)

func validatorRubricValues() []string {
	return []string{
		"claim_grounding", "terminal_statuses", "page_state_changes",
		"stale_reference_recovery", "input_json_validity", "correction_grounding",
		"interruption_and_cancel", "detach_survival",
	}
}

func computeBrowserConversationInputJSONValidity(calls []BrowserConversationBrokerCall) BrowserConversationInputJSONValidity {
	measurement := BrowserConversationInputJSONValidity{}
	for _, call := range calls {
		if call.Operation != BrowserConversationInvoke {
			continue
		}
		valid := browserConversationJSONStringObject(call.InputJSON)
		measurement.Attempts = append(measurement.Attempts, BrowserConversationInputJSONAttempt{
			Sequence: call.Sequence, StepID: call.StepID, InvocationID: cloneBrowserConversationOpaque(call.InvocationID),
			ToolRef: cloneBrowserConversationOpaque(call.ToolRef), ToolName: call.ToolName,
			State: cloneBrowserConversationOpaque(call.State), Terminal: call.Terminal,
			InputJSON: call.InputJSON, ValidObject: valid,
		})
		measurement.TotalAttempts++
		if valid {
			measurement.ValidObjectStrings++
		}
	}
	if measurement.TotalAttempts > 0 {
		measurement.Percentage = float64(measurement.ValidObjectStrings) * 100 / float64(measurement.TotalAttempts)
	}
	return measurement
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

func newReport(result BrowserConversationResult, metadata BrowserConversationReportMetadata) (BrowserConversationReport, error) {
	if err := validateResult(result); err != nil {
		return BrowserConversationReport{}, err
	}
	return BrowserConversationReport{
		Version:  BrowserConversationReportVersion,
		Metadata: sanitizeBrowserConversationReportMetadata(metadata),
		Rubric:   validatorRubricValues(),
		Evidence: sanitizeBrowserConversationResult(result),
	}, nil
}

func newValidatorInput(result BrowserConversationResult) (BrowserConversationValidatorInput, error) {
	if err := validateResult(result); err != nil {
		return BrowserConversationValidatorInput{}, err
	}
	result.Finalized = true
	return BrowserConversationValidatorInput{
		Version:  BrowserConversationValidatorInputVersion,
		Rubric:   validatorRubricValues(),
		Evidence: sanitizeBrowserConversationResult(result),
	}, nil
}

func renderReport(result BrowserConversationResult, metadata BrowserConversationReportMetadata) (string, error) {
	report, err := newReport(result, metadata)
	if err != nil {
		return "", err
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", err
	}
	var rendered strings.Builder
	rendered.WriteString("## WebMCP conversational customer simulation\n\nSanitized reproducibility metadata and complete evidence:\n\n```json\n")
	rendered.Write(encoded)
	rendered.WriteString("\n```\n")
	return rendered.String(), nil
}

func writeReport(out io.Writer, result BrowserConversationResult, metadata BrowserConversationReportMetadata) error {
	if out == nil {
		return errors.New("browser conversation report writer is nil")
	}
	report, err := renderReport(result, metadata)
	if err != nil {
		return err
	}
	_, err = io.WriteString(out, report)
	return err
}
