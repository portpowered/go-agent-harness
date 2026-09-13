// Package servicetest exposes runtime seams for external acceptance tests.
// Production callers must use the injected service contracts instead.
package servicetest

import (
	"io"
	"time"
)

import webmcp "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"

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

// BrowserConversationLifecycleEvidence preserves the historical test-seam ID
// types while the runtime contract remains host-neutral and accepts opaque
// values. This is a shape-only compatibility adapter; policy lives in the
// runtime service.
type BrowserConversationLifecycleEvidence struct {
	Outcome                   runtimeBrowser.BrowserConversationLifecycleOutcome `json:"outcome"`
	SessionStarted            bool                                               `json:"session_started"`
	SessionTerminated         bool                                               `json:"session_terminated"`
	Detached                  bool                                               `json:"detached"`
	DetachCount               int                                                `json:"detach_count"`
	DetachRequired            bool                                               `json:"detach_required"`
	BrowserClosed             bool                                               `json:"browser_closed"`
	TargetClosed              bool                                               `json:"target_closed"`
	ExternalBrowserID         webmcp.BrowserID                                   `json:"external_browser_id,omitempty"`
	ExternalTargetID          webmcp.TargetID                                    `json:"external_target_id,omitempty"`
	ExternalTabAlive          bool                                               `json:"external_tab_alive"`
	ExternalTabResponsive     bool                                               `json:"external_tab_responsive"`
	ExternalTabAllowsMutation bool                                               `json:"external_tab_allows_mutation"`
	ExternalTabRead           bool                                               `json:"external_tab_read"`
	ExternalTabMutation       bool                                               `json:"external_tab_mutation"`
	Error                     string                                             `json:"error,omitempty"`
}

const BrowserConversationListTools = runtimeBrowser.BrowserConversationListTools
const BrowserConversationOracleAfter = runtimeBrowser.BrowserConversationOracleAfter
const BrowserConversationOracleBefore = runtimeBrowser.BrowserConversationOracleBefore

type BrowserConversationOraclePhase = runtimeBrowser.BrowserConversationOraclePhase

const BrowserConversationOraclePostSession = runtimeBrowser.BrowserConversationOraclePostSession

type BrowserConversationOracleSnapshot = runtimeBrowser.BrowserConversationOracleSnapshot
type BrowserConversationPage = runtimeBrowser.BrowserConversationPage
type BrowserConversationReportMetadata = runtimeBrowser.BrowserConversationReportMetadata

// BrowserConversationResult keeps the test-only servicetest shape compatible
// with the former CLI contract. All behavior is delegated to the runtime
// contract through the conversion method below.
type BrowserConversationResult struct {
	ScenarioID        string                                                 `json:"scenario_id"`
	ScenarioName      string                                                 `json:"scenario_name"`
	Finalized         bool                                                   `json:"finalized"`
	Turns             []runtimeBrowser.BrowserConversationTurn               `json:"turns,omitempty"`
	BrokerCalls       []runtimeBrowser.BrowserConversationBrokerCall         `json:"broker_calls,omitempty"`
	InputJSONValidity runtimeBrowser.BrowserConversationInputJSONValidity    `json:"input_json_validity"`
	Oracles           []runtimeBrowser.BrowserConversationOracleSnapshot     `json:"oracle_snapshots,omitempty"`
	Corrections       []runtimeBrowser.BrowserConversationCorrectionEvidence `json:"corrections,omitempty"`
	Recovery          []runtimeBrowser.BrowserConversationRecoveryEvidence   `json:"recovery,omitempty"`
	Cancellation      runtimeBrowser.BrowserConversationCancellationEvidence `json:"cancellation"`
	Lifecycle         BrowserConversationLifecycleEvidence                   `json:"lifecycle"`
	Mechanical        runtimeBrowser.BrowserConversationMechanicalEvaluation `json:"mechanical"`
	Validator         runtimeBrowser.BrowserConversationValidatorVerdict     `json:"validator"`
}

func (value BrowserConversationLifecycleEvidence) runtime() runtimeBrowser.BrowserConversationLifecycleEvidence {
	return runtimeBrowser.BrowserConversationLifecycleEvidence{
		Outcome:                   value.Outcome,
		SessionStarted:            value.SessionStarted,
		SessionTerminated:         value.SessionTerminated,
		Detached:                  value.Detached,
		DetachCount:               value.DetachCount,
		DetachRequired:            value.DetachRequired,
		BrowserClosed:             value.BrowserClosed,
		TargetClosed:              value.TargetClosed,
		ExternalBrowserID:         value.ExternalBrowserID,
		ExternalTargetID:          value.ExternalTargetID,
		ExternalTabAlive:          value.ExternalTabAlive,
		ExternalTabResponsive:     value.ExternalTabResponsive,
		ExternalTabAllowsMutation: value.ExternalTabAllowsMutation,
		ExternalTabRead:           value.ExternalTabRead,
		ExternalTabMutation:       value.ExternalTabMutation,
		Error:                     value.Error,
	}
}

func (value BrowserConversationResult) runtime() runtimeBrowser.BrowserConversationResult {
	return runtimeBrowser.BrowserConversationResult{
		ScenarioID:        value.ScenarioID,
		ScenarioName:      value.ScenarioName,
		Finalized:         value.Finalized,
		Turns:             value.Turns,
		BrokerCalls:       value.BrokerCalls,
		InputJSONValidity: value.InputJSONValidity,
		Oracles:           value.Oracles,
		Corrections:       value.Corrections,
		Recovery:          value.Recovery,
		Cancellation:      value.Cancellation,
		Lifecycle:         value.Lifecycle.runtime(),
		Mechanical:        value.Mechanical,
		Validator:         value.Validator,
	}
}

func (value BrowserConversationResult) Validate() error {
	return value.runtime().Validate()
}

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

func DeriveBrowserConversationCorrections(scenario runtimeBrowser.BrowserConversationScenario, result BrowserConversationResult) []runtimeBrowser.BrowserConversationCorrectionEvidence {
	return browserScenarioWire.NewService().DeriveCorrections(scenario, result.runtime())
}

func DeriveBrowserConversationRecovery(scenario runtimeBrowser.BrowserConversationScenario, result BrowserConversationResult) []runtimeBrowser.BrowserConversationRecoveryEvidence {
	return browserScenarioWire.NewService().DeriveRecovery(scenario, result.runtime())
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

func EvaluateBrowserConversation(scenario runtimeBrowser.BrowserConversationScenario, result BrowserConversationResult, rootErr error) (runtimeBrowser.BrowserConversationMechanicalEvaluation, error) {
	return browserScenarioWire.NewService().Evaluate(scenario, result.runtime(), rootErr)
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

func (validator *BrowserConversationCommandValidator) ValidateBrowserConversation(result BrowserConversationResult) (runtimeBrowser.BrowserConversationValidatorVerdict, error) {
	if validator == nil {
		return runtimeBrowser.BrowserConversationValidatorVerdict{}, runtimeBrowser.ErrBrowserConversationValidatorCommand
	}
	serviceValidator, err := browserScenarioWire.NewService().NewCommandValidator(runtimeBrowser.BrowserConversationValidatorCommand{
		Command: validator.Command, Dir: validator.Dir, Env: validator.Env, Timeout: validator.Timeout,
	})
	if err != nil {
		return runtimeBrowser.BrowserConversationValidatorVerdict{}, err
	}
	return serviceValidator.ValidateBrowserConversation(result.runtime())
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

func RenderBrowserConversationReport(result BrowserConversationResult, metadata runtimeBrowser.BrowserConversationReportMetadata) (string, error) {
	return browserScenarioWire.NewService().RenderReport(result.runtime(), metadata)
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

func WriteBrowserConversationReport(out io.Writer, result BrowserConversationResult, metadata runtimeBrowser.BrowserConversationReportMetadata) error {
	return browserScenarioWire.NewService().WriteReport(out, result.runtime(), metadata)
}
