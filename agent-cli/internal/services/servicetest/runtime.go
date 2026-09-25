// Package servicetest exposes public session contract names for acceptance
// tests. It aliases only runtime service contracts; session execution in tests
// goes through the same composed live service as the CLI.
package servicetest

import (
	sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	serviceDevices "github.com/portpowered/go-agent-harness/agent-cli/internal/services/devices"
	audioio "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	runtimeProviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay"
	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const DefaultOpenAIRealtimeModel = runtimeProviders.OpenAIRealtimeDefaultModel

var ErrInvalidOpenAIRealtimeVoice = sessioncontract.ErrInvalidOpenAIRealtimeVoice
var ErrRoomLaunchPathConflict = runtimeRooms.ErrLaunchPathConflict
var ErrRoomReplayBundleIncomplete = roomreplay.ErrRoomReplayBundleIncomplete
var ErrRoomReplaySourceConflict = runtimeRooms.ErrReplaySourceConflict
var ErrSessionAudioInputConflict = serviceDevices.ErrSessionAudioInputConflict
var ErrSessionAudioOutputConflict = serviceDevices.ErrSessionAudioOutputConflict
var ErrSessionAudioInTurnBargeRequiresSequence = sessioncontract.ErrSessionAudioInTurnBargeRequiresSequence
var ErrSessionAudioResponseIncomplete = runtimeSession.ErrLiveAudioResponseIncomplete
var ErrSessionImageContinuationIncomplete = runtimeSession.ErrLiveImageContinuationIncomplete
var ErrSessionScheduledAudioIncomplete = runtimeSession.ErrLiveScheduledAudioIncomplete
var ErrSessionUnresolvedToolResults = sessioncontract.ErrSessionUnresolvedToolResults

func ValidateSessionAudioDeviceConflicts(audioInFile, audioOutFile, audioInDevice, audioOutDevice bool) error {
	return serviceDevices.ValidateSessionAudioDeviceConflicts(audioInFile, audioOutFile, audioInDevice, audioOutDevice)
}

type InvalidOpenAIRealtimeVoiceError = sessioncontract.InvalidOpenAIRealtimeVoiceError
type RTCMediaEndpoints = sharedaudio.MediaEndpoints
type RTCMediaSession = sharedaudio.MediaSession
type ScheduledAudioInput = audioio.ScheduledAudioInput
type SessionAudioInTurnBargeError = sessioncontract.SessionAudioInTurnBargeError
type SessionDiagnosticRecord = sessiontrace.DiagnosticRecord
type SessionDurationTimer = sessionduration.Timer
type SessionDurationClock = sessionduration.TimerScheduler
type SessionImageContinuationError = runtimeSession.LiveImageContinuationError
type SessionScheduledAudioIncompleteError = runtimeSession.LiveScheduledAudioIncompleteError
type SessionToolContinuationError = runtimeSession.LiveToolContinuationError
type SessionToolDiagnostic = sessiontrace.ToolDiagnostic
type SessionUnresolvedToolResultsError = sessioncontract.SessionUnresolvedToolResultsError

const SessionMaxDurationReason = sessionterminal.MaxDurationReason
const SessionSilentProviderTimeoutClassification = sessiontrace.SilentProviderTimeoutClassification
const SessionDiagnosticEventFailure = sessiontrace.SessionDiagnosticEventFailure
const SessionDiagnosticEventMetrics = sessiontrace.SessionDiagnosticEventMetrics
const SessionDiagnosticEventToolCall = sessiontrace.SessionDiagnosticEventToolCall
const SessionDiagnosticEventTurn = sessiontrace.SessionDiagnosticEventTurn
const SessionDiagnosticFieldPendingToolContinuationCount = sessiontrace.SessionDiagnosticFieldPendingToolContinuationCount
const SessionDiagnosticFieldPendingToolContinuationIDs = sessiontrace.SessionDiagnosticFieldPendingToolContinuationIDs
const SessionDiagnosticFieldUnresolvedToolCallIDs = sessiontrace.SessionDiagnosticFieldUnresolvedToolCallIDs
const SessionDiagnosticFieldUnresolvedToolResultCount = sessiontrace.SessionDiagnosticFieldUnresolvedToolResultCount
