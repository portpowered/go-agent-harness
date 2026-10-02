// Package servicetest exposes public session contract names for acceptance
// tests. It aliases only runtime service contracts; session execution in tests
// goes through the same composed live service as the CLI.
package servicetest

import (
	sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeProviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const DefaultOpenAIRealtimeModel = runtimeProviders.OpenAIRealtimeDefaultModel

var ErrInvalidOpenAIRealtimeVoice = sessioncontract.ErrInvalidOpenAIRealtimeVoice
var ErrSessionAudioInTurnBargeRequiresSequence = sessioncontract.ErrSessionAudioInTurnBargeRequiresSequence
var ErrSessionAudioResponseIncomplete = runtimeSession.ErrLiveAudioResponseIncomplete
var ErrSessionImageContinuationIncomplete = runtimeSession.ErrLiveImageContinuationIncomplete
var ErrSessionScheduledAudioIncomplete = runtimeSession.ErrLiveScheduledAudioIncomplete
var ErrSessionUnresolvedToolResults = sessioncontract.ErrSessionUnresolvedToolResults

type InvalidOpenAIRealtimeVoiceError = sessioncontract.InvalidOpenAIRealtimeVoiceError
type RTCMediaEndpoints = sharedaudio.MediaEndpoints
type RTCMediaSession = sharedaudio.MediaSession
type SessionAudioInTurnBargeError = sessioncontract.SessionAudioInTurnBargeError
type SessionImageContinuationError = runtimeSession.LiveImageContinuationError
type SessionScheduledAudioIncompleteError = runtimeSession.LiveScheduledAudioIncompleteError
type SessionToolDiagnostic = sessiontrace.ToolDiagnostic
type SessionUnresolvedToolResultsError = sessioncontract.SessionUnresolvedToolResultsError

// SessionMaxDurationReason is the stable terminal reason the live runtime
// records when --max-duration ends a session.
const SessionMaxDurationReason messages.TerminalReason = "max_duration"
const SessionSilentProviderTimeoutClassification = sessiontrace.SilentProviderTimeoutClassification
