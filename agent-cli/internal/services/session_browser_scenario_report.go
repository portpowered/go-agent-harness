package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

const (
	// BrowserConversationReportVersion identifies the stable, sanitized report
	// envelope suitable for attaching to a review or passing to a validator.
	BrowserConversationReportVersion = "webmcp.conversational-report.v1"
	// BrowserConversationValidatorInputVersion identifies the fixed validator
	// input envelope. It is separate from the returned verdict version so a
	// validator cannot silently change the rubric it received.
	BrowserConversationValidatorInputVersion = "webmcp.conversational-validator-input.v1"
	maxBrowserConversationValidatorOutput    = 1 << 20
)

// BrowserConversationReportMetadata contains reproducibility facts that are
// safe to publish. It intentionally has no API-key, cookie, authorization,
// or other credential field.
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

// BrowserConversationReport is the complete sanitized evidence envelope.
// Evidence keeps the raw input_json values as strings so JSON syntax errors
// remain reviewable instead of being converted into a different value.
type BrowserConversationReport struct {
	Version  string                            `json:"version"`
	Metadata BrowserConversationReportMetadata `json:"metadata"`
	Rubric   []string                          `json:"validator_rubric"`
	Evidence BrowserConversationResult         `json:"evidence"`
}

// BrowserConversationValidatorInput is the fixed rubric plus complete
// sanitized evidence sent to an external validator command.
type BrowserConversationValidatorInput struct {
	Version  string                    `json:"version"`
	Rubric   []string                  `json:"rubric"`
	Evidence BrowserConversationResult `json:"evidence"`
}

// BrowserConversationValidatorRubric returns the exact check names required
// from a validator agent. A fresh slice prevents a caller from changing the
// contract for another run.
func BrowserConversationValidatorRubric() []string {
	return append([]string(nil), browserConversationValidatorRubric...)
}

var browserConversationValidatorRubric = []string{
	"claim_grounding",
	"terminal_statuses",
	"page_state_changes",
	"stale_reference_recovery",
	"input_json_validity",
	"correction_grounding",
	"interruption_and_cancel",
	"detach_survival",
}

// ComputeBrowserConversationInputJSONValidity computes the exact measurement
// over the supplied ordered broker trace. Every webmcp_invoke observation is
// an attempt, including its non-terminal and terminal records when both are
// present; the trace remains the source of truth for that distinction.
func ComputeBrowserConversationInputJSONValidity(calls []BrowserConversationBrokerCall) BrowserConversationInputJSONValidity {
	return computeBrowserConversationInputJSONValidity(calls)
}

func computeBrowserConversationInputJSONValidity(calls []BrowserConversationBrokerCall) BrowserConversationInputJSONValidity {
	measurement := BrowserConversationInputJSONValidity{}
	for _, call := range calls {
		if call.Operation != BrowserConversationInvoke {
			continue
		}
		valid := browserConversationJSONStringObject(call.InputJSON)
		measurement.Attempts = append(measurement.Attempts, BrowserConversationInputJSONAttempt{
			Sequence: call.Sequence, StepID: call.StepID, InvocationID: call.InvocationID,
			ToolRef: call.ToolRef, ToolName: call.ToolName, State: call.State,
			Terminal: call.Terminal, InputJSON: call.InputJSON, ValidObject: valid,
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

// SanitizeBrowserConversationResult returns a defensive report-safe copy.
// Ordinary input_json strings are byte-for-byte unchanged. Credential-shaped
// strings are replaced before a report or validator boundary, and malformed
// raw JSON fields are represented as JSON strings so report encoding cannot
// fail or accidentally emit an invalid document.
func SanitizeBrowserConversationResult(result BrowserConversationResult) BrowserConversationResult {
	validity := computeBrowserConversationInputJSONValidity(result.BrokerCalls)
	clone := cloneBrowserConversationResult(result)
	clone.ScenarioID = sanitizeBrowserConversationReportText(clone.ScenarioID)
	clone.ScenarioName = sanitizeBrowserConversationReportText(clone.ScenarioName)
	for index := range clone.Turns {
		clone.Turns[index].StepID = sanitizeBrowserConversationReportText(clone.Turns[index].StepID)
		clone.Turns[index].ExpectedText = sanitizeBrowserConversationReportText(clone.Turns[index].ExpectedText)
		clone.Turns[index].ObservedText = sanitizeBrowserConversationReportText(clone.Turns[index].ObservedText)
	}
	for index := range clone.BrokerCalls {
		clone.BrokerCalls[index].StepID = sanitizeBrowserConversationReportText(clone.BrokerCalls[index].StepID)
		clone.BrokerCalls[index].ToolName = sanitizeBrowserConversationReportText(clone.BrokerCalls[index].ToolName)
		clone.BrokerCalls[index].InputJSON = sanitizeBrowserConversationInputJSON(clone.BrokerCalls[index].InputJSON)
		clone.BrokerCalls[index].ErrorCode = sanitizeBrowserConversationReportText(clone.BrokerCalls[index].ErrorCode)
		clone.BrokerCalls[index].Output = sanitizeBrowserConversationRawJSON(clone.BrokerCalls[index].Output)
	}
	for index := range clone.Oracles {
		clone.Oracles[index].StepID = sanitizeBrowserConversationReportText(clone.Oracles[index].StepID)
		clone.Oracles[index].PageID = sanitizeBrowserConversationReportText(clone.Oracles[index].PageID)
		clone.Oracles[index].State = sanitizeBrowserConversationRawJSON(clone.Oracles[index].State)
	}
	for index := range clone.Corrections {
		correction := &clone.Corrections[index]
		correction.StepID = sanitizeBrowserConversationReportText(correction.StepID)
		correction.TargetStepID = sanitizeBrowserConversationReportText(correction.TargetStepID)
		correction.TargetUtterance = sanitizeBrowserConversationReportText(correction.TargetUtterance)
		correction.CorrectionUtterance = sanitizeBrowserConversationReportText(correction.CorrectionUtterance)
		correction.OriginalToolName = sanitizeBrowserConversationReportText(correction.OriginalToolName)
		correction.CorrectionToolName = sanitizeBrowserConversationReportText(correction.CorrectionToolName)
		correction.OriginalAssistantText = sanitizeBrowserConversationReportText(correction.OriginalAssistantText)
		correction.CorrectionAssistantText = sanitizeBrowserConversationReportText(correction.CorrectionAssistantText)
		correction.OriginalBefore = sanitizeBrowserConversationRawJSON(correction.OriginalBefore)
		correction.OriginalAfter = sanitizeBrowserConversationRawJSON(correction.OriginalAfter)
		correction.CorrectionBefore = sanitizeBrowserConversationRawJSON(correction.CorrectionBefore)
		correction.CorrectionAfter = sanitizeBrowserConversationRawJSON(correction.CorrectionAfter)
	}
	for index := range clone.Recovery {
		recovery := &clone.Recovery[index]
		recovery.StepID = sanitizeBrowserConversationReportText(recovery.StepID)
		recovery.FromPageID = sanitizeBrowserConversationReportText(recovery.FromPageID)
		recovery.ToPageID = sanitizeBrowserConversationReportText(recovery.ToPageID)
		recovery.StaleErrorCode = sanitizeBrowserConversationReportText(recovery.StaleErrorCode)
	}
	clone.Cancellation.Reason = sanitizeBrowserConversationReportText(clone.Cancellation.Reason)
	clone.Cancellation.InterruptedStepID = sanitizeBrowserConversationReportText(clone.Cancellation.InterruptedStepID)
	clone.Cancellation.CancelStepID = sanitizeBrowserConversationReportText(clone.Cancellation.CancelStepID)
	clone.Lifecycle.Error = sanitizeBrowserConversationReportText(clone.Lifecycle.Error)
	clone.Mechanical.Failures = sanitizeBrowserConversationReportTexts(clone.Mechanical.Failures)
	clone.Validator.Summary = sanitizeBrowserConversationReportText(clone.Validator.Summary)
	for index := range clone.Validator.Checks {
		clone.Validator.Checks[index].Name = sanitizeBrowserConversationReportText(clone.Validator.Checks[index].Name)
		clone.Validator.Checks[index].Detail = sanitizeBrowserConversationReportText(clone.Validator.Checks[index].Detail)
	}
	clone.InputJSONValidity = sanitizeBrowserConversationInputJSONValidity(validity)
	return clone
}

func sanitizeBrowserConversationInputJSONValidity(validity BrowserConversationInputJSONValidity) BrowserConversationInputJSONValidity {
	validity.Attempts = append([]BrowserConversationInputJSONAttempt(nil), validity.Attempts...)
	for index := range validity.Attempts {
		validity.Attempts[index].InputJSON = sanitizeBrowserConversationInputJSON(validity.Attempts[index].InputJSON)
		validity.Attempts[index].StepID = sanitizeBrowserConversationReportText(validity.Attempts[index].StepID)
		validity.Attempts[index].ToolName = sanitizeBrowserConversationReportText(validity.Attempts[index].ToolName)
	}
	return validity
}

// NewBrowserConversationReport validates and snapshots one complete result
// into the report contract.
func NewBrowserConversationReport(result BrowserConversationResult, metadata BrowserConversationReportMetadata) (BrowserConversationReport, error) {
	if err := result.Validate(); err != nil {
		return BrowserConversationReport{}, err
	}
	metadata = sanitizeBrowserConversationReportMetadata(metadata)
	return BrowserConversationReport{
		Version:  BrowserConversationReportVersion,
		Metadata: metadata,
		Rubric:   BrowserConversationValidatorRubric(),
		Evidence: SanitizeBrowserConversationResult(result),
	}, nil
}

// NewBrowserConversationValidatorInput creates the exact sanitized payload
// consumed by a validator agent.
func NewBrowserConversationValidatorInput(result BrowserConversationResult) (BrowserConversationValidatorInput, error) {
	if err := result.Validate(); err != nil {
		return BrowserConversationValidatorInput{}, err
	}
	result.Finalized = true
	return BrowserConversationValidatorInput{
		Version:  BrowserConversationValidatorInputVersion,
		Rubric:   BrowserConversationValidatorRubric(),
		Evidence: SanitizeBrowserConversationResult(result),
	}, nil
}

// MarshalBrowserConversationReport encodes a report as stable indented JSON.
func MarshalBrowserConversationReport(report BrowserConversationReport) ([]byte, error) {
	return json.MarshalIndent(report, "", "  ")
}

// RenderBrowserConversationReport returns review-ready Markdown containing the
// complete machine-readable report. The evidence is fenced so arbitrary
// transcript text cannot become Markdown structure.
func RenderBrowserConversationReport(result BrowserConversationResult, metadata BrowserConversationReportMetadata) (string, error) {
	report, err := NewBrowserConversationReport(result, metadata)
	if err != nil {
		return "", err
	}
	encoded, err := MarshalBrowserConversationReport(report)
	if err != nil {
		return "", err
	}
	var rendered strings.Builder
	rendered.WriteString("## WebMCP conversational customer simulation\n\n")
	rendered.WriteString("Sanitized reproducibility metadata and complete evidence:\n\n```json\n")
	rendered.Write(encoded)
	rendered.WriteString("\n```\n")
	return rendered.String(), nil
}

// WriteBrowserConversationReport writes the review-ready Markdown report.
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

// BrowserConversationCommandValidator invokes a fixed external validator
// process with the complete sanitized report on stdin. It is bounded and
// never includes the report in an error message or command-line argument.
type BrowserConversationCommandValidator struct {
	Command []string
	Dir     string
	Env     []string
	Timeout time.Duration
}

// NewBrowserConversationCommandValidator validates and copies a command
// boundary before it is used as a BrowserConversationValidator.
func NewBrowserConversationCommandValidator(command []string, timeout time.Duration) (*BrowserConversationCommandValidator, error) {
	if err := validateBrowserConversationValidatorCommand(command, timeout); err != nil {
		return nil, err
	}
	return &BrowserConversationCommandValidator{Command: append([]string(nil), command...), Timeout: timeout}, nil
}

// ValidateBrowserConversation implements BrowserConversationValidator.
func (v *BrowserConversationCommandValidator) ValidateBrowserConversation(result BrowserConversationResult) (BrowserConversationValidatorVerdict, error) {
	if v == nil {
		return BrowserConversationValidatorVerdict{}, errors.New("browser conversation command validator is nil")
	}
	if err := validateBrowserConversationValidatorCommand(v.Command, v.Timeout); err != nil {
		return BrowserConversationValidatorVerdict{}, err
	}
	input, err := NewBrowserConversationValidatorInput(result)
	if err != nil {
		return BrowserConversationValidatorVerdict{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return BrowserConversationValidatorVerdict{}, errors.New("encode validator input")
	}

	ctx, cancel := context.WithTimeout(context.Background(), v.Timeout)
	defer cancel()
	command := exec.CommandContext(ctx, v.Command[0], v.Command[1:]...)
	command.Dir = v.Dir
	if v.Env != nil {
		command.Env = append([]string(nil), v.Env...)
	}
	var stdout browserConversationBoundedBuffer
	stdout.limit = maxBrowserConversationValidatorOutput
	command.Stdin = bytes.NewReader(payload)
	command.Stdout = &stdout
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return BrowserConversationValidatorVerdict{}, errors.New("validator command timed out")
		}
		return BrowserConversationValidatorVerdict{}, errors.New("validator command failed")
	}
	if stdout.truncated {
		return BrowserConversationValidatorVerdict{}, errors.New("validator command output exceeded bound")
	}
	verdict, err := decodeBrowserConversationValidatorVerdict(stdout.Bytes())
	if err != nil {
		return BrowserConversationValidatorVerdict{}, err
	}
	if err := validateBrowserConversationValidatorVerdict(verdict); err != nil {
		return BrowserConversationValidatorVerdict{}, err
	}
	return verdict, nil
}

func validateBrowserConversationValidatorCommand(command []string, timeout time.Duration) error {
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return errors.New("validator command is required")
	}
	if timeout <= 0 {
		return errors.New("validator command timeout must be positive")
	}
	for _, part := range command {
		if browserConversationContainsCredentialMarker(part) {
			return errors.New("validator command must not contain credential-shaped arguments")
		}
	}
	return nil
}

func decodeBrowserConversationValidatorVerdict(payload []byte) (BrowserConversationValidatorVerdict, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var verdict BrowserConversationValidatorVerdict
	if err := decoder.Decode(&verdict); err != nil {
		return BrowserConversationValidatorVerdict{}, errors.New("decode validator verdict")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return BrowserConversationValidatorVerdict{}, errors.New("validator verdict must contain one JSON object")
	}
	return verdict, nil
}

func validateBrowserConversationValidatorVerdict(verdict BrowserConversationValidatorVerdict) error {
	if verdict.Version != "" && verdict.Version != BrowserConversationValidatorVersion {
		return fmt.Errorf("validator verdict version must be %q", BrowserConversationValidatorVersion)
	}
	if verdict.Status == "" {
		return errors.New("validator verdict status is required")
	}
	if verdict.Status != BrowserConversationValidatorPass && verdict.Status != BrowserConversationValidatorFail && verdict.Status != BrowserConversationValidatorNotRun {
		return errors.New("validator verdict status is unsupported")
	}
	if verdict.Status == BrowserConversationValidatorPass && !verdict.Passed {
		return errors.New("validator pass status contradicted passed=false")
	}
	if verdict.Status == BrowserConversationValidatorFail && verdict.Passed {
		return errors.New("validator fail status contradicted passed=true")
	}
	if verdict.Status == BrowserConversationValidatorNotRun {
		return nil
	}
	wanted := make(map[string]struct{}, len(browserConversationValidatorRubric))
	for _, name := range browserConversationValidatorRubric {
		wanted[name] = struct{}{}
	}
	seen := make(map[string]struct{}, len(verdict.Checks))
	for _, check := range verdict.Checks {
		if _, ok := wanted[check.Name]; !ok {
			return fmt.Errorf("validator verdict contains unsupported check %q", check.Name)
		}
		if _, duplicate := seen[check.Name]; duplicate {
			return fmt.Errorf("validator verdict repeats check %q", check.Name)
		}
		seen[check.Name] = struct{}{}
	}
	if len(seen) != len(wanted) {
		return errors.New("validator verdict did not cover the fixed rubric")
	}
	return nil
}

type browserConversationBoundedBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *browserConversationBoundedBuffer) Write(value []byte) (int, error) {
	if b.limit <= 0 {
		return len(value), nil
	}
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(value), nil
	}
	if len(value) > remaining {
		_, _ = b.Buffer.Write(value[:remaining])
		b.truncated = true
		return len(value), nil
	}
	_, err := b.Buffer.Write(value)
	return len(value), err
}

func sanitizeBrowserConversationReportMetadata(metadata BrowserConversationReportMetadata) BrowserConversationReportMetadata {
	metadata.Command = sanitizeBrowserConversationReportText(metadata.Command)
	metadata.Configuration = sanitizeBrowserConversationReportText(metadata.Configuration)
	metadata.Provider = sanitizeBrowserConversationReportText(metadata.Provider)
	metadata.Model = sanitizeBrowserConversationReportText(metadata.Model)
	metadata.BrowserChannel = sanitizeBrowserConversationReportText(metadata.BrowserChannel)
	metadata.BrowserVersion = sanitizeBrowserConversationReportText(metadata.BrowserVersion)
	metadata.BrowserRevision = sanitizeBrowserConversationReportText(metadata.BrowserRevision)
	metadata.PR269Status = sanitizeBrowserConversationReportText(metadata.PR269Status)
	metadata.LaneIBranch = sanitizeBrowserConversationReportText(metadata.LaneIBranch)
	metadata.LaneIPullRequest = sanitizeBrowserConversationReportText(metadata.LaneIPullRequest)
	metadata.DependencyBaseline = sanitizeBrowserConversationReportTexts(metadata.DependencyBaseline)
	return metadata
}

func sanitizeBrowserConversationReportTexts(values []string) []string {
	if values == nil {
		return nil
	}
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = sanitizeBrowserConversationReportText(value)
	}
	return result
}

func sanitizeBrowserConversationInputJSON(value string) string {
	if browserConversationContainsCredentialMarker(value) {
		return "[redacted]"
	}
	return value
}

func sanitizeBrowserConversationRawJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return nil
	}
	if browserConversationContainsCredentialMarker(string(value)) {
		return json.RawMessage(`"[redacted]"`)
	}
	if json.Valid(value) {
		return append(json.RawMessage(nil), value...)
	}
	encoded, err := json.Marshal(string(value))
	if err != nil {
		return json.RawMessage(`"[invalid_json]"`)
	}
	return encoded
}

func sanitizeBrowserConversationReportText(value string) string {
	if browserConversationContainsCredentialMarker(value) {
		return "[redacted]"
	}
	var builder strings.Builder
	for _, char := range value {
		switch char {
		case '\n', '\r', '\t':
			builder.WriteByte(' ')
		default:
			if char < 0x20 || char == 0x7f {
				builder.WriteByte(' ')
			} else {
				builder.WriteRune(char)
			}
		}
	}
	return builder.String()
}

func browserConversationContainsCredentialMarker(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{
		"authorization:", "bearer ", "api_key", "api-key", "access_token",
		"refresh_token", "client_secret", "password", "-----begin ", "sk-",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
