package gateway

import "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"

// Re-export session types from models so gateway consumers can use them
// without importing the models package directly.

type AudioFormat = models.AudioFormat
type SampleRate = models.SampleRate
type SessionModality = models.SessionModality
type TurnDetectionConfig = models.TurnDetectionConfig
type InputAudioTranscriptionConfig = models.InputAudioTranscriptionConfig
type SessionConfig = models.SessionConfig
type SessionEventType = models.SessionEventType
type SessionEvent = models.SessionEvent

// Re-export audio format constants.
const (
	AudioFormatPCM16    = models.AudioFormatPCM16
	AudioFormatG711Ulaw = models.AudioFormatG711Ulaw
	AudioFormatG711Alaw = models.AudioFormatG711Alaw
)

// Re-export sample rate constants.
const (
	SampleRate8000  = models.SampleRate8000
	SampleRate16000 = models.SampleRate16000
	SampleRate24000 = models.SampleRate24000
)

// Re-export session modality constants.
const (
	SessionModalityText  = models.SessionModalityText
	SessionModalityAudio = models.SessionModalityAudio
)

// DefaultInputAudioTranscriptionModel is the default OpenAI customer-audio
// transcription model exposed through the gateway package.
const DefaultInputAudioTranscriptionModel = models.DefaultInputAudioTranscriptionModel

// Re-export client-to-server event type constants.
const (
	SessionEventSessionUpdate          = models.SessionEventSessionUpdate
	SessionEventInputAudioBufferAppend = models.SessionEventInputAudioBufferAppend
	SessionEventInputAudioBufferCommit = models.SessionEventInputAudioBufferCommit
	SessionEventInputAudioBufferClear  = models.SessionEventInputAudioBufferClear
	SessionEventResponseCreate         = models.SessionEventResponseCreate
	SessionEventResponseCancel         = models.SessionEventResponseCancel
)

// Re-export server-to-client event type constants.
const (
	SessionEventSessionCreated                     = models.SessionEventSessionCreated
	SessionEventSessionUpdated                     = models.SessionEventSessionUpdated
	SessionEventSessionClosed                      = models.SessionEventSessionClosed
	SessionEventInputAudioBufferSpeechStarted      = models.SessionEventInputAudioBufferSpeechStarted
	SessionEventInputAudioBufferSpeechStopped      = models.SessionEventInputAudioBufferSpeechStopped
	SessionEventResponseCreated                    = models.SessionEventResponseCreated
	SessionEventResponseDone                       = models.SessionEventResponseDone
	SessionEventResponseOutputAudioDelta           = models.SessionEventResponseOutputAudioDelta
	SessionEventResponseOutputAudioDone            = models.SessionEventResponseOutputAudioDone
	SessionEventResponseOutputAudioTranscriptDelta = models.SessionEventResponseOutputAudioTranscriptDelta
	SessionEventResponseOutputAudioTranscriptDone  = models.SessionEventResponseOutputAudioTranscriptDone
	SessionEventResponseTextDelta                  = models.SessionEventResponseTextDelta
	SessionEventResponseTextDone                   = models.SessionEventResponseTextDone
	SessionEventResponseFunctionCallArgumentsDelta = models.SessionEventResponseFunctionCallArgumentsDelta
	SessionEventResponseFunctionCallArgumentsDone  = models.SessionEventResponseFunctionCallArgumentsDone
	SessionEventResponseOutputItemAdded            = models.SessionEventResponseOutputItemAdded
	SessionEventError                              = models.SessionEventError
)

// Re-export event constructors.
var (
	NewAudioBufferAppendEvent = models.NewAudioBufferAppendEvent
	NewAudioBufferCommitEvent = models.NewAudioBufferCommitEvent
	NewAudioBufferClearEvent  = models.NewAudioBufferClearEvent
	NewResponseCreateEvent    = models.NewResponseCreateEvent
	NewResponseCancelEvent    = models.NewResponseCancelEvent
	NewSessionUpdateEvent     = models.NewSessionUpdateEvent
)
