package policy

import (
	"encoding/json"
	"io"
)

const (
	InvocationCompleted = browserConversationInvocationCompleted
	InvocationCanceled  = browserConversationInvocationCanceled
	MaxSafeTextBytes    = browserConversationMaxSafeTextBytes
	StaleToolRef        = browserConversationStaleToolRef
)

func ValidateScenario(scenario BrowserConversationScenario) error {
	return validateScenario(scenario)
}

func ParseScenarioJSON(data []byte) (BrowserConversationScenario, error) {
	return parseScenarioJSON(data)
}

func AdmitScenario(scenario BrowserConversationScenario) (BrowserConversationScenario, error) {
	return admitScenario(scenario)
}

func ScheduleAudioInputs(scenario BrowserConversationScenario, audio map[string][]byte) ([]ScheduledAudioInput, error) {
	return scheduleAudioInputs(scenario, audio)
}

func ComputeInputJSONValidity(calls []BrowserConversationBrokerCall) BrowserConversationInputJSONValidity {
	return computeBrowserConversationInputJSONValidity(calls)
}

func SanitizeResult(result BrowserConversationResult) BrowserConversationResult {
	return sanitizeBrowserConversationResult(result)
}

func NewReport(result BrowserConversationResult, metadata BrowserConversationReportMetadata) (BrowserConversationReport, error) {
	return newReport(result, metadata)
}

func NewValidatorInput(result BrowserConversationResult) (BrowserConversationValidatorInput, error) {
	return newValidatorInput(result)
}

func RenderReport(result BrowserConversationResult, metadata BrowserConversationReportMetadata) (string, error) {
	return renderReport(result, metadata)
}

func WriteReport(out io.Writer, result BrowserConversationResult, metadata BrowserConversationReportMetadata) error {
	return writeReport(out, result, metadata)
}

func ValidateJSONObject(path string, raw json.RawMessage) error {
	return validateJSONObject(path, raw)
}

func ValidateResult(result BrowserConversationResult) error {
	return validateResult(result)
}

func ValidatorRubricValues() []string {
	return validatorRubricValues()
}

func SafeText(value string) string {
	return safeBrowserConversationText(value)
}

func OpaqueString(value any) string {
	return browserConversationOpaqueString(value)
}

func OpaqueLen(value any) int {
	return browserConversationOpaqueLen(value)
}

func OpaqueEqual(left, right any) bool {
	return opaqueEqual(left, right)
}

func CloneOpaque(value any) any {
	return cloneBrowserConversationOpaque(value)
}

func CloneResult(value BrowserConversationResult) BrowserConversationResult {
	return cloneBrowserConversationResult(value)
}

func CloneTurn(value BrowserConversationTurn) BrowserConversationTurn {
	return cloneBrowserConversationTurn(value)
}

func CloneBrokerCall(value BrowserConversationBrokerCall) BrowserConversationBrokerCall {
	return cloneBrowserConversationBrokerCall(value)
}

func CloneOracleSnapshot(value BrowserConversationOracleSnapshot) BrowserConversationOracleSnapshot {
	return cloneBrowserConversationOracleSnapshot(value)
}

func CloneCorrections(value []BrowserConversationCorrectionEvidence) []BrowserConversationCorrectionEvidence {
	return cloneBrowserConversationCorrections(value)
}

func CloneRecoveries(value []BrowserConversationRecoveryEvidence) []BrowserConversationRecoveryEvidence {
	return cloneBrowserConversationRecoveries(value)
}

func CloneMechanicalEvaluation(value BrowserConversationMechanicalEvaluation) BrowserConversationMechanicalEvaluation {
	return cloneBrowserConversationMechanicalEvaluation(value)
}

func CloneValidatorVerdict(value BrowserConversationValidatorVerdict) BrowserConversationValidatorVerdict {
	return cloneBrowserConversationValidatorVerdict(value)
}

func ObservationError(path, format string, args ...any) error {
	return browserConversationObservationError(path, format, args...)
}

func BrokerOperationValid(operation BrowserConversationBrokerOperation) bool {
	return browserConversationBrokerOperationValid(operation)
}

func InvocationStateTerminal(state any) bool {
	return browserConversationInvocationStateTerminal(state)
}

func OraclePhaseValid(phase BrowserConversationOraclePhase) bool {
	return browserConversationOraclePhaseValid(phase)
}

func ValidateCorrectionStates(path string, correction BrowserConversationCorrectionEvidence) error {
	return validateBrowserConversationCorrectionStates(path, correction)
}

func ValidateRecoveryIdentity(path string, recovery BrowserConversationRecoveryEvidence) error {
	return validateBrowserConversationRecoveryIdentity(path, recovery)
}

func ValidatorStatusValid(status BrowserConversationValidatorStatus) bool {
	return browserConversationValidatorStatusValid(status)
}

func ValidateValidatorVerdict(verdict BrowserConversationValidatorVerdict) error {
	return validateBrowserConversationValidatorVerdict(verdict)
}
