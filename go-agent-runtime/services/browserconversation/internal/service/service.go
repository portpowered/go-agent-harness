package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation"
)

type BrowserConversationScenario = browserconversation.BrowserConversationScenario
type BrowserScenario = browserconversation.BrowserScenario
type WebMCPScenario = browserconversation.WebMCPScenario
type WebMCPConversationScenario = browserconversation.WebMCPConversationScenario
type BrowserConversationScenarioError = browserconversation.BrowserConversationScenarioError
type BrowserConversationFixture = browserconversation.BrowserConversationFixture
type BrowserConversationPage = browserconversation.BrowserConversationPage
type BrowserConversationStep = browserconversation.BrowserConversationStep
type BrowserStateTransition = browserconversation.BrowserStateTransition
type BrowserCustomerNavigation = browserconversation.BrowserCustomerNavigation
type BrowserConversationCorrection = browserconversation.BrowserConversationCorrection
type BrowserConversationInterruptTrigger = browserconversation.BrowserConversationInterruptTrigger
type BrowserConversationInterrupt = browserconversation.BrowserConversationInterrupt
type BrowserConversationCancelRequest = browserconversation.BrowserConversationCancelRequest
type BrowserConversationTabStateRequired = browserconversation.BrowserConversationTabStateRequired
type BrowserConversationScenarioValue = browserconversation.BrowserConversationScenarioValue
type BrowserConversationScenarioForSession = browserconversation.BrowserConversationScenarioForSession
type BrowserConversationValidator = browserconversation.BrowserConversationValidator
type BrowserConversationValidatorFunc = browserconversation.BrowserConversationValidatorFunc
type BrowserConversationTurn = browserconversation.BrowserConversationTurn
type BrowserConversationBrokerCall = browserconversation.BrowserConversationBrokerCall
type BrowserConversationInputJSONAttempt = browserconversation.BrowserConversationInputJSONAttempt
type BrowserConversationInputJSONValidity = browserconversation.BrowserConversationInputJSONValidity
type BrowserConversationRecoveryEvidence = browserconversation.BrowserConversationRecoveryEvidence
type BrowserConversationCorrectionEvidence = browserconversation.BrowserConversationCorrectionEvidence
type BrowserConversationOracleSnapshot = browserconversation.BrowserConversationOracleSnapshot
type BrowserConversationMechanicalEvaluation = browserconversation.BrowserConversationMechanicalEvaluation
type BrowserConversationResult = browserconversation.BrowserConversationResult
type BrowserConversationLifecycleEvidence = browserconversation.BrowserConversationLifecycleEvidence
type BrowserConversationCancellationEvidence = browserconversation.BrowserConversationCancellationEvidence
type BrowserConversationValidatorVerdict = browserconversation.BrowserConversationValidatorVerdict
type BrowserConversationValidatorCheck = browserconversation.BrowserConversationValidatorCheck
type BrowserConversationOraclePhase = browserconversation.BrowserConversationOraclePhase
type BrowserConversationBrokerOperation = browserconversation.BrowserConversationBrokerOperation
type BrowserConversationTurnDirection = browserconversation.BrowserConversationTurnDirection
type BrowserConversationLifecycleOutcome = browserconversation.BrowserConversationLifecycleOutcome
type BrowserConversationValidatorStatus = browserconversation.BrowserConversationValidatorStatus
type BrowserConversationReportMetadata = browserconversation.BrowserConversationReportMetadata
type BrowserConversationReport = browserconversation.BrowserConversationReport
type BrowserConversationValidatorInput = browserconversation.BrowserConversationValidatorInput
type BrowserConversationRun = browserconversation.BrowserConversationRun

const (
	BrowserConversationScenarioVersion         = browserconversation.BrowserConversationScenarioVersion
	BrowserConversationReportVersion           = browserconversation.BrowserConversationReportVersion
	BrowserConversationValidatorInputVersion   = browserconversation.BrowserConversationValidatorInputVersion
	BrowserInterruptOnInFlightInvocation       = browserconversation.BrowserInterruptOnInFlightInvocation
	BrowserConversationCustomerTurn            = browserconversation.BrowserConversationCustomerTurn
	BrowserConversationAssistantTurn           = browserconversation.BrowserConversationAssistantTurn
	BrowserConversationListTools               = browserconversation.BrowserConversationListTools
	BrowserConversationInvoke                  = browserconversation.BrowserConversationInvoke
	BrowserConversationCancel                  = browserconversation.BrowserConversationCancel
	BrowserConversationSelectPage              = browserconversation.BrowserConversationSelectPage
	BrowserConversationWaitReady               = browserconversation.BrowserConversationWaitReady
	BrowserConversationCustomerNavigate        = browserconversation.BrowserConversationCustomerNavigate
	BrowserConversationOracleBefore            = browserconversation.BrowserConversationOracleBefore
	BrowserConversationOracleAfter             = browserconversation.BrowserConversationOracleAfter
	BrowserConversationOraclePostSession       = browserconversation.BrowserConversationOraclePostSession
	BrowserConversationLifecycleCanceled       = browserconversation.BrowserConversationLifecycleCanceled
	BrowserConversationLifecycleCompleted      = browserconversation.BrowserConversationLifecycleCompleted
	BrowserConversationValidatorVersion        = browserconversation.BrowserConversationValidatorVersion
	BrowserConversationValidatorPass           = browserconversation.BrowserConversationValidatorPass
	BrowserConversationValidatorFail           = browserconversation.BrowserConversationValidatorFail
	BrowserConversationValidatorNotRun         = browserconversation.BrowserConversationValidatorNotRun
	ErrBrowserConversationSession              = browserconversation.ErrBrowserConversationSession
	ErrBrowserConversationValidatorStart       = browserconversation.ErrBrowserConversationValidatorStart
	ErrBrowserConversationValidatorFailed      = browserconversation.ErrBrowserConversationValidatorFailed
	ErrBrowserConversationValidatorTimeout     = browserconversation.ErrBrowserConversationValidatorTimeout
	ErrInvalidBrowserConversationScenario      = browserconversation.ErrInvalidBrowserConversationScenario
	ErrInvalidBrowserConversationResult        = browserconversation.ErrInvalidBrowserConversationResult
	ErrBrowserConversationRunFinalized         = browserconversation.ErrBrowserConversationRunFinalized
	ErrBrowserConversationDuplicateObservation = browserconversation.ErrBrowserConversationDuplicateObservation
	ErrBrowserConversationValidatorCommand     = browserconversation.ErrBrowserConversationValidatorCommand
	ErrBrowserConversationValidatorOutput      = browserconversation.ErrBrowserConversationValidatorOutput
	ErrBrowserConversationValidatorVerdict     = browserconversation.ErrBrowserConversationValidatorVerdict
)

// Service is the private implementation of the host-neutral browser
// conversation contract. It carries no process-wide state.
type Service struct{}

func New() *Service { return &Service{} }

func (*Service) Run(ctx context.Context, request browserconversation.RunRequest) (browserconversation.BrowserConversationResult, error) {
	return runBrowserConversation(ctx, request)
}

func (*Service) ValidateScenario(scenario browserconversation.BrowserConversationScenario) (browserconversation.BrowserConversationScenario, error) {
	return scenario.Admit()
}

func (*Service) NewRecorder(request browserconversation.RecordingRequest) (browserconversation.Recorder, error) {
	return newRecorder(request)
}

func (*Service) AdmitScenario(scenario browserconversation.BrowserConversationScenario) (browserconversation.BrowserConversationScenario, error) {
	return scenario.Admit()
}

func (*Service) ScheduleAudioInputs(scenario browserconversation.BrowserConversationScenario, audio map[string][]byte) ([]browserconversation.ScheduledAudioInput, error) {
	return scenario.ScheduleAudioInputs(audio)
}

func (*Service) NewScenarioValue(scenario browserconversation.BrowserConversationScenario) (browserconversation.BrowserConversationScenarioForSession, error) {
	return scenario.ScenarioValue()
}

func (*Service) NewRun(scenario browserconversation.BrowserConversationScenario) (*browserconversation.BrowserConversationRun, error) {
	return scenario.NewRun()
}

func (*Service) ComputeInputJSONValidity(calls []browserconversation.BrowserConversationBrokerCall) browserconversation.BrowserConversationInputJSONValidity {
	return browserconversation.BrowserConversationTrace(calls).InputJSONValidity()
}

func (*Service) SanitizeResult(result browserconversation.BrowserConversationResult) browserconversation.BrowserConversationResult {
	return result.Sanitized()
}

func (*Service) NewReport(result browserconversation.BrowserConversationResult, metadata browserconversation.BrowserConversationReportMetadata) (browserconversation.BrowserConversationReport, error) {
	return result.Report(metadata)
}

func (*Service) Report(result browserconversation.BrowserConversationResult, metadata browserconversation.BrowserConversationReportMetadata) (browserconversation.BrowserConversationReport, error) {
	return result.Report(metadata)
}

func (*Service) NewValidatorInput(result browserconversation.BrowserConversationResult) (browserconversation.BrowserConversationValidatorInput, error) {
	return result.ValidatorInput()
}

func (*Service) RenderReport(result browserconversation.BrowserConversationResult, metadata browserconversation.BrowserConversationReportMetadata) (string, error) {
	return result.RenderReport(metadata)
}

func (*Service) WriteReport(out io.Writer, result browserconversation.BrowserConversationResult, metadata browserconversation.BrowserConversationReportMetadata) error {
	return result.WriteReport(out, metadata)
}

func (*Service) DeriveCorrections(scenario browserconversation.BrowserConversationScenario, result browserconversation.BrowserConversationResult) []browserconversation.BrowserConversationCorrectionEvidence {
	return deriveBrowserConversationCorrections(scenario, result)
}

func (*Service) DeriveRecovery(scenario browserconversation.BrowserConversationScenario, result browserconversation.BrowserConversationResult) []browserconversation.BrowserConversationRecoveryEvidence {
	return deriveBrowserConversationRecovery(scenario, result)
}

func (*Service) Evaluate(scenario browserconversation.BrowserConversationScenario, result browserconversation.BrowserConversationResult, rootErr error) (browserconversation.BrowserConversationMechanicalEvaluation, error) {
	return EvaluateBrowserConversation(scenario, result, rootErr)
}

func (*Service) ValidateJSONObject(path string, raw json.RawMessage) error {
	return (browserconversation.BrowserConversationScenario{}).ValidateJSONObject(path, raw)
}

func (*Service) NewCommandValidator(config browserconversation.BrowserConversationValidatorCommand) (browserconversation.BrowserConversationValidator, error) {
	validator, err := NewCommandValidator(config.Command, config.Timeout)
	if err != nil {
		return nil, err
	}
	command, ok := validator.(*commandValidator)
	if !ok {
		return nil, errors.New("command validator has unexpected implementation")
	}
	command.Dir = config.Dir
	command.Env = append([]string(nil), config.Env...)
	return command, nil
}

var _ browserconversation.Service = (*Service)(nil)
