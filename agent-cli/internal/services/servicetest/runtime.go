// Package servicetest exposes non-browser runtime seams for acceptance tests.
// Browser-conversation tests import the public browserconversation contract.
package servicetest

import (
	"context"
	"io"
	"time"

	sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	serviceDevices "github.com/portpowered/go-agent-harness/agent-cli/internal/services/devices"
	impl "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime"
	audioio "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const DefaultOpenAIRealtimeModel = impl.DefaultOpenAIRealtimeModel

var ErrInvalidOpenAIRealtimeVoice = sessioncontract.ErrInvalidOpenAIRealtimeVoice
var ErrRoomLaunchPathConflict = runtimeRooms.ErrLaunchPathConflict
var ErrRoomReplayBundleIncomplete = impl.ErrRoomReplayBundleIncomplete
var ErrRoomReplaySourceConflict = impl.ErrRoomReplaySourceConflict
var ErrSessionAudioInputConflict = serviceDevices.ErrSessionAudioInputConflict
var ErrSessionAudioOutputConflict = serviceDevices.ErrSessionAudioOutputConflict
var ErrSessionAudioInTurnBargeRequiresSequence = impl.ErrSessionAudioInTurnBargeRequiresSequence
var ErrSessionAudioResponseIncomplete = impl.ErrSessionAudioResponseIncomplete
var ErrSessionImageContinuationIncomplete = runtimeSession.ErrLiveImageContinuationIncomplete
var ErrSessionScheduledAudioIncomplete = runtimeSession.ErrLiveScheduledAudioIncomplete
var ErrSessionUnresolvedToolResults = sessioncontract.ErrSessionUnresolvedToolResults
var NewOpenAIRealtimeSessionInferencerWithOptions = impl.NewOpenAIRealtimeSessionInferencerWithOptions
var NewOpenAIRealtimeSessionInferencerWithToolsAndOptions = impl.NewOpenAIRealtimeSessionInferencerWithToolsAndOptions
var NewGrokSessionInferencer = impl.NewGrokSessionInferencer
var NewGrokSessionInferencerWithOptions = impl.NewGrokSessionInferencerWithOptions

func ValidateSessionAudioDeviceConflicts(audioInFile, audioOutFile, audioInDevice, audioOutDevice bool) error {
	return serviceDevices.ValidateSessionAudioDeviceConflicts(audioInFile, audioOutFile, audioInDevice, audioOutDevice)
}

func RunSession(ctx context.Context, out io.Writer, opts SessionRunOptions) error {
	return impl.RunSession(ctx, out, opts)
}

func RunSessionWithInstructions(ctx context.Context, out io.Writer, opts SessionRunOptions, systemPrompt string) error {
	return impl.RunSessionWithInstructions(ctx, out, opts, systemPrompt)
}

func RunSessionWithMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration) error {
	return impl.RunSessionWithMaxDuration(ctx, out, opts, maxDuration)
}

func RunSessionWithMaxDurationClock(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration, durationClock SessionDurationClock) error {
	return impl.RunSessionWithMaxDurationClock(ctx, out, opts, maxDuration, durationClock)
}

const ParticipantTerminationEnded = impl.ParticipantTerminationEnded
const ParticipantTerminationError = impl.ParticipantTerminationError

type InvalidOpenAIRealtimeVoiceError = sessioncontract.InvalidOpenAIRealtimeVoiceError
type RTCMediaEndpoints = sharedaudio.MediaEndpoints
type RTCMediaSession = sharedaudio.MediaSession
type ScheduledAudioInput = audioio.ScheduledAudioInput
type SelfPlayRunOptions = impl.SelfPlayRunOptions
type SessionAudioInTurnBargeError = sessioncontract.SessionAudioInTurnBargeError
type SessionTextSeed = impl.SessionTextSeed
type SessionDiagnosticRecord = impl.SessionDiagnosticRecord
type SessionDurationTimer = impl.SessionDurationTimer
type SessionDurationClock = impl.SessionDurationClock
type SessionImageContinuationError = runtimeSession.LiveImageContinuationError
type SessionRTCComponents = impl.SessionRTCComponents
type SessionRTCDataPlane = impl.SessionRTCDataPlane
type SessionRunOptions = impl.SessionRunOptions
type SessionRuntimeSelection = impl.SessionRuntimeSelection
type SessionScheduledAudioIncompleteError = runtimeSession.LiveScheduledAudioIncompleteError
type SessionToolContinuationError = impl.SessionToolContinuationError
type SessionToolDiagnostic = impl.SessionToolDiagnostic
type SessionUnresolvedToolResultsError = sessioncontract.SessionUnresolvedToolResultsError

const SessionMaxDurationReason = impl.SessionMaxDurationReason
const SessionSilentProviderTimeoutClassification = impl.SessionSilentProviderTimeoutClassification
const SessionTransportWebRTC = impl.SessionTransportWebRTC
const SessionDiagnosticEventFailure = impl.SessionDiagnosticEventFailure
const SessionDiagnosticEventMetrics = impl.SessionDiagnosticEventMetrics
const SessionDiagnosticEventToolCall = impl.SessionDiagnosticEventToolCall
const SessionDiagnosticEventTurn = impl.SessionDiagnosticEventTurn
const SessionDiagnosticFieldPendingToolContinuationCount = impl.SessionDiagnosticFieldPendingToolContinuationCount
const SessionDiagnosticFieldPendingToolContinuationIDs = impl.SessionDiagnosticFieldPendingToolContinuationIDs
const SessionDiagnosticFieldUnresolvedToolCallIDs = impl.SessionDiagnosticFieldUnresolvedToolCallIDs
const SessionDiagnosticFieldUnresolvedToolResultCount = impl.SessionDiagnosticFieldUnresolvedToolResultCount
