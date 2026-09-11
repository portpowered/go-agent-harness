package service

import (
	"encoding/json"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserscenario"
)

type BrowserConversationScenario = browserscenario.BrowserConversationScenario
type BrowserScenario = browserscenario.BrowserScenario
type WebMCPScenario = browserscenario.WebMCPScenario
type WebMCPConversationScenario = browserscenario.WebMCPConversationScenario
type BrowserConversationScenarioError = browserscenario.BrowserConversationScenarioError
type BrowserConversationFixture = browserscenario.BrowserConversationFixture
type BrowserConversationPage = browserscenario.BrowserConversationPage
type BrowserConversationStep = browserscenario.BrowserConversationStep
type BrowserStateTransition = browserscenario.BrowserStateTransition
type BrowserCustomerNavigation = browserscenario.BrowserCustomerNavigation
type BrowserConversationCorrection = browserscenario.BrowserConversationCorrection
type BrowserConversationInterruptTrigger = browserscenario.BrowserConversationInterruptTrigger
type BrowserConversationInterrupt = browserscenario.BrowserConversationInterrupt
type BrowserConversationCancelRequest = browserscenario.BrowserConversationCancelRequest
type BrowserConversationTabStateRequired = browserscenario.BrowserConversationTabStateRequired
type BrowserConversationScenarioValue = browserscenario.BrowserConversationScenarioValue
type BrowserConversationScenarioForSession = browserscenario.BrowserConversationScenarioForSession
type BrowserConversationValidator = browserscenario.BrowserConversationValidator
type BrowserConversationValidatorFunc = browserscenario.BrowserConversationValidatorFunc
type BrowserConversationTurn = browserscenario.BrowserConversationTurn
type BrowserConversationBrokerCall = browserscenario.BrowserConversationBrokerCall
type BrowserConversationInputJSONAttempt = browserscenario.BrowserConversationInputJSONAttempt
type BrowserConversationInputJSONValidity = browserscenario.BrowserConversationInputJSONValidity
type BrowserConversationRecoveryEvidence = browserscenario.BrowserConversationRecoveryEvidence
type BrowserConversationCorrectionEvidence = browserscenario.BrowserConversationCorrectionEvidence
type BrowserConversationOracleSnapshot = browserscenario.BrowserConversationOracleSnapshot
type BrowserConversationMechanicalEvaluation = browserscenario.BrowserConversationMechanicalEvaluation
type BrowserConversationResult = browserscenario.BrowserConversationResult
type BrowserConversationLifecycleEvidence = browserscenario.BrowserConversationLifecycleEvidence
type BrowserConversationCancellationEvidence = browserscenario.BrowserConversationCancellationEvidence
type BrowserConversationValidatorVerdict = browserscenario.BrowserConversationValidatorVerdict
type BrowserConversationValidatorCheck = browserscenario.BrowserConversationValidatorCheck
type BrowserConversationOraclePhase = browserscenario.BrowserConversationOraclePhase
type BrowserConversationBrokerOperation = browserscenario.BrowserConversationBrokerOperation
type BrowserConversationTurnDirection = browserscenario.BrowserConversationTurnDirection
type BrowserConversationLifecycleOutcome = browserscenario.BrowserConversationLifecycleOutcome
type BrowserConversationValidatorStatus = browserscenario.BrowserConversationValidatorStatus
type BrowserConversationReportMetadata = browserscenario.BrowserConversationReportMetadata
type BrowserConversationReport = browserscenario.BrowserConversationReport
type BrowserConversationValidatorInput = browserscenario.BrowserConversationValidatorInput
type BrowserConversationRun = browserscenario.BrowserConversationRun

const (
	BrowserConversationScenarioVersion         = browserscenario.BrowserConversationScenarioVersion
	BrowserConversationReportVersion           = browserscenario.BrowserConversationReportVersion
	BrowserConversationValidatorInputVersion   = browserscenario.BrowserConversationValidatorInputVersion
	BrowserInterruptOnInFlightInvocation       = browserscenario.BrowserInterruptOnInFlightInvocation
	BrowserConversationCustomerTurn            = browserscenario.BrowserConversationCustomerTurn
	BrowserConversationAssistantTurn           = browserscenario.BrowserConversationAssistantTurn
	BrowserConversationListTools               = browserscenario.BrowserConversationListTools
	BrowserConversationInvoke                  = browserscenario.BrowserConversationInvoke
	BrowserConversationCancel                  = browserscenario.BrowserConversationCancel
	BrowserConversationSelectPage              = browserscenario.BrowserConversationSelectPage
	BrowserConversationWaitReady               = browserscenario.BrowserConversationWaitReady
	BrowserConversationCustomerNavigate        = browserscenario.BrowserConversationCustomerNavigate
	BrowserConversationOracleBefore            = browserscenario.BrowserConversationOracleBefore
	BrowserConversationOracleAfter             = browserscenario.BrowserConversationOracleAfter
	BrowserConversationOraclePostSession       = browserscenario.BrowserConversationOraclePostSession
	BrowserConversationLifecycleCanceled       = browserscenario.BrowserConversationLifecycleCanceled
	BrowserConversationLifecycleCompleted      = browserscenario.BrowserConversationLifecycleCompleted
	BrowserConversationValidatorVersion        = browserscenario.BrowserConversationValidatorVersion
	BrowserConversationValidatorPass           = browserscenario.BrowserConversationValidatorPass
	BrowserConversationValidatorFail           = browserscenario.BrowserConversationValidatorFail
	BrowserConversationValidatorNotRun         = browserscenario.BrowserConversationValidatorNotRun
	ErrBrowserConversationSession              = browserscenario.ErrBrowserConversationSession
	ErrBrowserConversationValidatorStart       = browserscenario.ErrBrowserConversationValidatorStart
	ErrBrowserConversationValidatorFailed      = browserscenario.ErrBrowserConversationValidatorFailed
	ErrBrowserConversationValidatorTimeout     = browserscenario.ErrBrowserConversationValidatorTimeout
	ErrInvalidBrowserConversationScenario      = browserscenario.ErrInvalidBrowserConversationScenario
	ErrInvalidBrowserConversationResult        = browserscenario.ErrInvalidBrowserConversationResult
	ErrBrowserConversationRunFinalized         = browserscenario.ErrBrowserConversationRunFinalized
	ErrBrowserConversationDuplicateObservation = browserscenario.ErrBrowserConversationDuplicateObservation
	ErrBrowserConversationValidatorCommand     = browserscenario.ErrBrowserConversationValidatorCommand
	ErrBrowserConversationValidatorOutput      = browserscenario.ErrBrowserConversationValidatorOutput
	ErrBrowserConversationValidatorVerdict     = browserscenario.ErrBrowserConversationValidatorVerdict
)

// Service is the private implementation of the host-neutral browser
// conversation contract. It carries no process-wide state.
type Service struct{}

func New() *Service { return &Service{} }

func (*Service) AdmitScenario(scenario browserscenario.BrowserConversationScenario) (browserscenario.BrowserConversationScenario, error) {
	return scenario.Admit()
}

func (*Service) ScheduleAudioInputs(scenario browserscenario.BrowserConversationScenario, audio map[string][]byte) ([]browserscenario.ScheduledAudioInput, error) {
	return scenario.ScheduleAudioInputs(audio)
}

func (*Service) NewScenarioValue(scenario browserscenario.BrowserConversationScenario) (browserscenario.BrowserConversationScenarioForSession, error) {
	return scenario.ScenarioValue()
}

func (*Service) NewRun(scenario browserscenario.BrowserConversationScenario) (*browserscenario.BrowserConversationRun, error) {
	return scenario.NewRun()
}

func (*Service) ComputeInputJSONValidity(calls []browserscenario.BrowserConversationBrokerCall) browserscenario.BrowserConversationInputJSONValidity {
	return browserscenario.BrowserConversationTrace(calls).InputJSONValidity()
}

func (*Service) SanitizeResult(result browserscenario.BrowserConversationResult) browserscenario.BrowserConversationResult {
	return result.Sanitized()
}

func (*Service) NewReport(result browserscenario.BrowserConversationResult, metadata browserscenario.BrowserConversationReportMetadata) (browserscenario.BrowserConversationReport, error) {
	return result.Report(metadata)
}

func (*Service) NewValidatorInput(result browserscenario.BrowserConversationResult) (browserscenario.BrowserConversationValidatorInput, error) {
	return result.ValidatorInput()
}

func (*Service) RenderReport(result browserscenario.BrowserConversationResult, metadata browserscenario.BrowserConversationReportMetadata) (string, error) {
	return result.RenderReport(metadata)
}

func (*Service) WriteReport(out io.Writer, result browserscenario.BrowserConversationResult, metadata browserscenario.BrowserConversationReportMetadata) error {
	return result.WriteReport(out, metadata)
}

func (*Service) DeriveCorrections(scenario browserscenario.BrowserConversationScenario, result browserscenario.BrowserConversationResult) []browserscenario.BrowserConversationCorrectionEvidence {
	return deriveBrowserConversationCorrections(scenario, result)
}

func (*Service) DeriveRecovery(scenario browserscenario.BrowserConversationScenario, result browserscenario.BrowserConversationResult) []browserscenario.BrowserConversationRecoveryEvidence {
	return deriveBrowserConversationRecovery(scenario, result)
}

func (*Service) Evaluate(scenario browserscenario.BrowserConversationScenario, result browserscenario.BrowserConversationResult, rootErr error) (browserscenario.BrowserConversationMechanicalEvaluation, error) {
	return EvaluateBrowserConversation(scenario, result, rootErr)
}

func (*Service) ValidateJSONObject(path string, raw json.RawMessage) error {
	return (browserscenario.BrowserConversationScenario{}).ValidateJSONObject(path, raw)
}

func (*Service) NewCommandValidator(config browserscenario.BrowserConversationValidatorCommand) (browserscenario.BrowserConversationValidator, error) {
	validator, err := NewCommandValidator(config.Command, config.Timeout)
	if err != nil {
		return nil, err
	}
	command := validator.(*commandValidator)
	command.Dir = config.Dir
	command.Env = append([]string(nil), config.Env...)
	return command, nil
}

var _ browserscenario.Service = (*Service)(nil)
