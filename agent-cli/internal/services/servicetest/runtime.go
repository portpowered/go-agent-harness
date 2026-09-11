// Package servicetest exposes runtime seams for external acceptance tests.
// Production callers must use the injected service contracts instead.
package servicetest

import (
	"io"
	"time"
)

import sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"

import serviceDevices "github.com/portpowered/go-agent-harness/agent-cli/internal/services/devices"

import impl "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime"

import runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"

import runtimeBrowser "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserscenario"

import browserScenarioWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserscenario/wire"

import runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"

const BrowserConversationAssistantTurn = runtimeBrowser.BrowserConversationAssistantTurn

type BrowserConversationBrokerCall = runtimeBrowser.BrowserConversationBrokerCall

const BrowserConversationCancel = runtimeBrowser.BrowserConversationCancel

type BrowserConversationCancelRequest = runtimeBrowser.BrowserConversationCancelRequest
type BrowserConversationCancellationEvidence = runtimeBrowser.BrowserConversationCancellationEvidence
type BrowserConversationCorrection = runtimeBrowser.BrowserConversationCorrection

const BrowserConversationCustomerNavigate = runtimeBrowser.BrowserConversationCustomerNavigate
const BrowserConversationCustomerTurn = runtimeBrowser.BrowserConversationCustomerTurn

type BrowserConversationFixture = runtimeBrowser.BrowserConversationFixture
type BrowserConversationInterrupt = runtimeBrowser.BrowserConversationInterrupt

const BrowserConversationInvoke = runtimeBrowser.BrowserConversationInvoke
const BrowserConversationLifecycleCanceled = runtimeBrowser.BrowserConversationLifecycleCanceled

type BrowserConversationLifecycleEvidence = runtimeBrowser.BrowserConversationLifecycleEvidence

const BrowserConversationListTools = runtimeBrowser.BrowserConversationListTools
const BrowserConversationOracleAfter = runtimeBrowser.BrowserConversationOracleAfter
const BrowserConversationOracleBefore = runtimeBrowser.BrowserConversationOracleBefore

type BrowserConversationOraclePhase = runtimeBrowser.BrowserConversationOraclePhase

const BrowserConversationOraclePostSession = runtimeBrowser.BrowserConversationOraclePostSession

type BrowserConversationOracleSnapshot = runtimeBrowser.BrowserConversationOracleSnapshot
type BrowserConversationPage = runtimeBrowser.BrowserConversationPage
type BrowserConversationReportMetadata = runtimeBrowser.BrowserConversationReportMetadata
type BrowserConversationResult = runtimeBrowser.BrowserConversationResult
type BrowserConversationScenario = runtimeBrowser.BrowserConversationScenario

const BrowserConversationScenarioVersion = runtimeBrowser.BrowserConversationScenarioVersion

type BrowserConversationStep = runtimeBrowser.BrowserConversationStep
type BrowserConversationTabStateRequired = runtimeBrowser.BrowserConversationTabStateRequired
type BrowserConversationTurn = runtimeBrowser.BrowserConversationTurn

const BrowserConversationValidatorNotRun = runtimeBrowser.BrowserConversationValidatorNotRun

type BrowserConversationValidatorVerdict = runtimeBrowser.BrowserConversationValidatorVerdict

const BrowserConversationValidatorVersion = runtimeBrowser.BrowserConversationValidatorVersion

type BrowserCustomerNavigation = runtimeBrowser.BrowserCustomerNavigation

const BrowserInterruptOnInFlightInvocation = runtimeBrowser.BrowserInterruptOnInFlightInvocation

type BrowserStateTransition = runtimeBrowser.BrowserStateTransition

func ComputeBrowserConversationInputJSONValidity(calls []runtimeBrowser.BrowserConversationBrokerCall) runtimeBrowser.BrowserConversationInputJSONValidity {
	return browserScenarioWire.NewService().ComputeInputJSONValidity(calls)
}

const DefaultOpenAIRealtimeModel = impl.DefaultOpenAIRealtimeModel

func DeriveBrowserConversationCorrections(scenario runtimeBrowser.BrowserConversationScenario, result runtimeBrowser.BrowserConversationResult) []runtimeBrowser.BrowserConversationCorrectionEvidence {
	return browserScenarioWire.NewService().DeriveCorrections(scenario, result)
}

func DeriveBrowserConversationRecovery(scenario runtimeBrowser.BrowserConversationScenario, result runtimeBrowser.BrowserConversationResult) []runtimeBrowser.BrowserConversationRecoveryEvidence {
	return browserScenarioWire.NewService().DeriveRecovery(scenario, result)
}

var ErrInvalidOpenAIRealtimeVoice = sessioncontract.ErrInvalidOpenAIRealtimeVoice
var ErrRTCSessionMediaUnavailable = impl.ErrRTCSessionMediaUnavailable
var ErrRoomLaunchPathConflict = runtimeRooms.ErrLaunchPathConflict
var ErrRoomReplayBundleIncomplete = impl.ErrRoomReplayBundleIncomplete
var ErrRoomReplaySourceConflict = impl.ErrRoomReplaySourceConflict
var ErrSessionAudioInputConflict = serviceDevices.ErrSessionAudioInputConflict
var ErrSessionAudioOutputConflict = serviceDevices.ErrSessionAudioOutputConflict
var ErrSessionAudioInTurnBargeRequiresSequence = impl.ErrSessionAudioInTurnBargeRequiresSequence
var ErrSessionAudioResponseIncomplete = impl.ErrSessionAudioResponseIncomplete
var ErrSessionImageContinuationIncomplete = impl.ErrSessionImageContinuationIncomplete
var ErrSessionScheduledAudioIncomplete = runtimeSession.ErrLiveScheduledAudioIncomplete
var ErrSessionUnresolvedToolResults = sessioncontract.ErrSessionUnresolvedToolResults

func EvaluateBrowserConversation(scenario runtimeBrowser.BrowserConversationScenario, result runtimeBrowser.BrowserConversationResult, rootErr error) (runtimeBrowser.BrowserConversationMechanicalEvaluation, error) {
	return browserScenarioWire.NewService().Evaluate(scenario, result, rootErr)
}

type InvalidOpenAIRealtimeVoiceError = sessioncontract.InvalidOpenAIRealtimeVoiceError

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

func (validator *BrowserConversationCommandValidator) ValidateBrowserConversation(result runtimeBrowser.BrowserConversationResult) (runtimeBrowser.BrowserConversationValidatorVerdict, error) {
	if validator == nil {
		return runtimeBrowser.BrowserConversationValidatorVerdict{}, runtimeBrowser.ErrBrowserConversationValidatorCommand
	}
	serviceValidator, err := browserScenarioWire.NewService().NewCommandValidator(runtimeBrowser.BrowserConversationValidatorCommand{
		Command: validator.Command, Dir: validator.Dir, Env: validator.Env, Timeout: validator.Timeout,
	})
	if err != nil {
		return runtimeBrowser.BrowserConversationValidatorVerdict{}, err
	}
	return serviceValidator.ValidateBrowserConversation(result)
}

var NewOpenAIRealtimeSessionInferencerWithOptions = impl.NewOpenAIRealtimeSessionInferencerWithOptions
var NewOpenAIRealtimeSessionInferencerWithToolsAndOptions = impl.NewOpenAIRealtimeSessionInferencerWithToolsAndOptions
var NewGrokSessionInferencer = impl.NewGrokSessionInferencer
var NewGrokSessionInferencerWithOptions = impl.NewGrokSessionInferencerWithOptions

const ParticipantTerminationEnded = impl.ParticipantTerminationEnded
const ParticipantTerminationError = impl.ParticipantTerminationError

type RTCMediaEndpoints = impl.RTCMediaEndpoints
type RTCMediaSession = impl.RTCMediaSession
type RTCDeviceBindingRequest = impl.RTCDeviceBindingRequest
type RTCDeviceBindingError = impl.RTCDeviceBindingError

var PrepareRTCDeviceBindings = impl.PrepareRTCDeviceBindings
var ValidateSessionAudioDeviceConflicts = serviceDevices.ValidateSessionAudioDeviceConflicts

func RenderBrowserConversationReport(result runtimeBrowser.BrowserConversationResult, metadata runtimeBrowser.BrowserConversationReportMetadata) (string, error) {
	return browserScenarioWire.NewService().RenderReport(result, metadata)
}

var RunSession = impl.RunSession
var RunSessionWithInstructions = impl.RunSessionWithInstructions
var RunSessionWithMaxDuration = impl.RunSessionWithMaxDuration
var RunSessionWithMaxDurationClock = impl.RunSessionWithMaxDurationClock

type ScheduledAudioInput = impl.ScheduledAudioInput
type SelfPlayRunOptions = impl.SelfPlayRunOptions
type SessionAudioInTurnBargeError = impl.SessionAudioInTurnBargeError
type SessionAudioInput = impl.SessionAudioInput
type SessionTextSeed = impl.SessionTextSeed

const SessionDiagnosticEventFailure = impl.SessionDiagnosticEventFailure
const SessionDiagnosticEventMetrics = impl.SessionDiagnosticEventMetrics
const SessionDiagnosticEventToolCall = impl.SessionDiagnosticEventToolCall
const SessionDiagnosticEventTurn = impl.SessionDiagnosticEventTurn
const SessionDiagnosticFieldPendingToolContinuationCount = impl.SessionDiagnosticFieldPendingToolContinuationCount
const SessionDiagnosticFieldPendingToolContinuationIDs = impl.SessionDiagnosticFieldPendingToolContinuationIDs
const SessionDiagnosticFieldUnresolvedToolCallIDs = impl.SessionDiagnosticFieldUnresolvedToolCallIDs
const SessionDiagnosticFieldUnresolvedToolResultCount = impl.SessionDiagnosticFieldUnresolvedToolResultCount

type SessionDiagnosticRecord = impl.SessionDiagnosticRecord
type SessionDurationTimer = impl.SessionDurationTimer
type SessionImageContinuationError = impl.SessionImageContinuationError

const SessionMaxDurationReason = impl.SessionMaxDurationReason

type SessionRTCComponents = impl.SessionRTCComponents
type SessionRTCDataPlane = impl.SessionRTCDataPlane
type SessionRunOptions = impl.SessionRunOptions
type SessionRuntimeSelection = impl.SessionRuntimeSelection
type SessionScheduledAudioIncompleteError = runtimeSession.LiveScheduledAudioIncompleteError

const SessionSilentProviderTimeoutClassification = impl.SessionSilentProviderTimeoutClassification

type SessionToolContinuationError = impl.SessionToolContinuationError
type SessionToolDiagnostic = impl.SessionToolDiagnostic

const SessionTransportWebRTC = impl.SessionTransportWebRTC

type SessionUnresolvedToolResultsError = sessioncontract.SessionUnresolvedToolResultsError

func WriteBrowserConversationReport(out io.Writer, result runtimeBrowser.BrowserConversationResult, metadata runtimeBrowser.BrowserConversationReportMetadata) error {
	return browserScenarioWire.NewService().WriteReport(out, result, metadata)
}
