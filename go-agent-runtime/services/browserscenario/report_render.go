package browserscenario

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// Report validates and snapshots one complete result into a report envelope.
func (result BrowserConversationResult) Report(metadata BrowserConversationReportMetadata) (BrowserConversationReport, error) {
	if err := result.Validate(); err != nil {
		return BrowserConversationReport{}, err
	}
	return BrowserConversationReport{
		Version:  BrowserConversationReportVersion,
		Metadata: sanitizeBrowserConversationReportMetadata(metadata),
		Rubric:   BrowserConversationValidatorRubric{}.Values(),
		Evidence: result.Sanitized(),
	}, nil
}

// ValidatorInput creates the exact sanitized payload consumed by a validator.
func (result BrowserConversationResult) ValidatorInput() (BrowserConversationValidatorInput, error) {
	if err := result.Validate(); err != nil {
		return BrowserConversationValidatorInput{}, err
	}
	result.Finalized = true
	return BrowserConversationValidatorInput{
		Version:  BrowserConversationValidatorInputVersion,
		Rubric:   BrowserConversationValidatorRubric{}.Values(),
		Evidence: result.Sanitized(),
	}, nil
}

// Marshal encodes a report as stable indented JSON.
func (report BrowserConversationReport) Marshal() ([]byte, error) {
	return json.MarshalIndent(report, "", "  ")
}

// RenderReport returns review-ready Markdown containing the complete report.
func (result BrowserConversationResult) RenderReport(metadata BrowserConversationReportMetadata) (string, error) {
	report, err := result.Report(metadata)
	if err != nil {
		return "", err
	}
	encoded, err := report.Marshal()
	if err != nil {
		return "", err
	}
	var rendered strings.Builder
	rendered.WriteString("## WebMCP conversational customer simulation\n\nSanitized reproducibility metadata and complete evidence:\n\n```json\n")
	rendered.Write(encoded)
	rendered.WriteString("\n```\n")
	return rendered.String(), nil
}

// WriteReport writes the review-ready Markdown report.
func (result BrowserConversationResult) WriteReport(out io.Writer, metadata BrowserConversationReportMetadata) error {
	if out == nil {
		return errors.New("browser conversation report writer is nil")
	}
	report, err := result.RenderReport(metadata)
	if err != nil {
		return err
	}
	_, err = io.WriteString(out, report)
	return err
}
