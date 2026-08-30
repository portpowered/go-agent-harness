package models

import "encoding/json"

// AudioFormat represents an audio encoding format for session-based inference.
type AudioFormat string

const (
	AudioFormatPCM16    AudioFormat = "pcm16"
	AudioFormatG711Ulaw AudioFormat = "g711_ulaw"
	AudioFormatG711Alaw AudioFormat = "g711_alaw"
)

// SampleRate represents audio sample rate in Hz.
type SampleRate int

const (
	SampleRate8000  SampleRate = 8000
	SampleRate16000 SampleRate = 16000
	SampleRate24000 SampleRate = 24000
)

// SessionModality represents a Realtime session input/output modality.
type SessionModality string

const (
	SessionModalityText  SessionModality = "text"
	SessionModalityAudio SessionModality = "audio"
)

// DefaultInputAudioTranscriptionModel is the OpenAI Realtime transcription
// model used for live customer-audio sessions when no opt-out is selected.
const DefaultInputAudioTranscriptionModel = "gpt-live-transcribe"

// InputAudioTranscriptionConfig is the typed policy for transcribing
// customer audio. A pointer on SessionConfig distinguishes an omitted policy
// from an explicitly disabled one while keeping the provider wire mapping
// provider-specific.
type InputAudioTranscriptionConfig struct {
	// Enabled controls whether customer audio transcription is requested.
	Enabled bool `json:"enabled"`
	// Model identifies the transcription model when Enabled is true.
	Model string `json:"model,omitempty"`
}

// TurnDetectionConfig configures server-side voice activity detection.
type TurnDetectionConfig struct {
	// Type is the turn detection mode (e.g. "server_vad").
	Type string `json:"type"`
	// Threshold is the VAD activation threshold (0.0–1.0).
	Threshold float64 `json:"threshold,omitempty"`
	// PrefixPaddingMs is milliseconds of audio to include before speech start.
	PrefixPaddingMs int `json:"prefix_padding_ms,omitempty"`
	// SilenceDurationMs is milliseconds of silence before speech is considered ended.
	SilenceDurationMs int `json:"silence_duration_ms,omitempty"`
	// CreateResponse controls whether server-side VAD owns response creation.
	// A pointer preserves the provider default when the field is unspecified and
	// allows callers to explicitly send false for client-owned turn boundaries.
	CreateResponse *bool `json:"create_response,omitempty"`
}

// SessionConfig is a gateway-owned configuration surface for establishing a
// provider session. It is not part of the loop-owned shared message contract.
type SessionConfig struct {
	// Model is the model ID for the session (e.g. "grok-3-mini").
	Model string `json:"model"`
	// Modalities are the requested Realtime output modalities (e.g. ["text", "audio"]).
	Modalities []SessionModality `json:"modalities,omitempty"`
	// Voice is the voice ID for audio output (e.g. "Eve", "Rex").
	Voice string `json:"voice,omitempty"`
	// Instructions are system-level instructions for the session.
	Instructions string `json:"instructions,omitempty"`
	// InputAudioFormat is the encoding for client-to-server audio.
	InputAudioFormat AudioFormat `json:"input_audio_format,omitempty"`
	// OutputAudioFormat is the encoding for server-to-client audio.
	OutputAudioFormat AudioFormat `json:"output_audio_format,omitempty"`
	// InputAudioSampleRate is the sample rate for client-to-server audio.
	InputAudioSampleRate SampleRate `json:"input_audio_sample_rate,omitempty"`
	// OutputAudioSampleRate is the sample rate for server-to-client audio.
	OutputAudioSampleRate SampleRate `json:"output_audio_sample_rate,omitempty"`
	// Tools are the tool definitions available during the session.
	Tools []ToolDefinition `json:"tools,omitempty"`
	// TurnDetection configures voice activity detection.
	TurnDetection *TurnDetectionConfig `json:"turn_detection,omitempty"`
	// InputAudioTranscription carries the resolved customer-audio transcription
	// policy. Providers translate it into their own session-update schema.
	InputAudioTranscription *InputAudioTranscriptionConfig `json:"input_audio_transcription,omitempty"`
	// Config holds provider-specific parameters as raw JSON.
	Config json.RawMessage `json:"config,omitempty"`
}

// SessionEventType identifies the kind of event in a bidirectional session stream.
// These follow OpenAI Realtime API conventions (which Grok also uses).
type SessionEventType string

// Client-to-server event types.
const (
	// SessionEventSessionUpdate requests a session configuration change.
	SessionEventSessionUpdate SessionEventType = "session.update"
	// SessionEventInputAudioBufferAppend sends audio data to the server.
	SessionEventInputAudioBufferAppend SessionEventType = "input_audio_buffer.append"
	// SessionEventInputAudioBufferCommit signals the end of an audio segment.
	SessionEventInputAudioBufferCommit SessionEventType = "input_audio_buffer.commit"
	// SessionEventInputAudioBufferClear discards buffered audio on the server.
	SessionEventInputAudioBufferClear SessionEventType = "input_audio_buffer.clear"
	// SessionEventInputAudioBufferCommitted confirms a committed user audio
	// buffer and names the conversation item the server created for it.
	SessionEventInputAudioBufferCommitted SessionEventType = "input_audio_buffer.committed"
	// SessionEventResponseCreate triggers a model response.
	SessionEventResponseCreate SessionEventType = "response.create"
	// SessionEventResponseCancel cancels an in-progress response.
	SessionEventResponseCancel SessionEventType = "response.cancel"
)

// Server-to-client event types.
const (
	// SessionEventSessionCreated is sent when the session is first established.
	SessionEventSessionCreated SessionEventType = "session.created"
	// SessionEventSessionUpdated confirms a session configuration update.
	SessionEventSessionUpdated SessionEventType = "session.updated"
	// SessionEventSessionClosed signals the provider closed the session.
	SessionEventSessionClosed SessionEventType = "session.closed"
	// SessionEventInputAudioBufferSpeechStarted signals VAD detected speech start.
	SessionEventInputAudioBufferSpeechStarted SessionEventType = "input_audio_buffer.speech_started"
	// SessionEventInputAudioBufferSpeechStopped signals VAD detected speech end.
	SessionEventInputAudioBufferSpeechStopped SessionEventType = "input_audio_buffer.speech_stopped"
	// SessionEventConversationItemInputAudioTranscriptionDelta delivers an
	// incremental transcript of the user's committed audio input.
	SessionEventConversationItemInputAudioTranscriptionDelta SessionEventType = "conversation.item.input_audio_transcription.delta"
	// SessionEventConversationItemInputAudioTranscriptionCompleted signals
	// that the user's committed audio transcript is complete.
	SessionEventConversationItemInputAudioTranscriptionCompleted SessionEventType = "conversation.item.input_audio_transcription.completed"
	// SessionEventResponseCreated confirms a response is being generated.
	SessionEventResponseCreated SessionEventType = "response.created"
	// SessionEventResponseDone signals a response is complete.
	SessionEventResponseDone SessionEventType = "response.done"
	// SessionEventResponseOutputAudioDelta delivers an audio chunk from the model.
	SessionEventResponseOutputAudioDelta SessionEventType = "response.output_audio.delta"
	// SessionEventResponseOutputAudioDone signals the audio stream for a response is complete.
	SessionEventResponseOutputAudioDone SessionEventType = "response.output_audio.done"
	// SessionEventResponseOutputAudioTranscriptDelta delivers a transcript chunk.
	SessionEventResponseOutputAudioTranscriptDelta SessionEventType = "response.output_audio_transcript.delta"
	// SessionEventResponseOutputAudioTranscriptDone signals transcript is complete.
	SessionEventResponseOutputAudioTranscriptDone SessionEventType = "response.output_audio_transcript.done"
	// SessionEventResponseTextDelta delivers a text chunk from the model.
	SessionEventResponseTextDelta SessionEventType = "response.output_text.delta"
	// SessionEventResponseTextDone signals the text stream for a response is complete.
	SessionEventResponseTextDone SessionEventType = "response.output_text.done"
	// SessionEventResponseFunctionCallArgumentsDelta delivers incremental function call arguments.
	SessionEventResponseFunctionCallArgumentsDelta SessionEventType = "response.function_call_arguments.delta"
	// SessionEventResponseFunctionCallArgumentsDone signals function call arguments are complete.
	SessionEventResponseFunctionCallArgumentsDone SessionEventType = "response.function_call_arguments.done"
	// SessionEventResponseOutputItemAdded signals a response output item has started.
	SessionEventResponseOutputItemAdded SessionEventType = "response.output_item.added"
	// SessionEventError signals an error from the server.
	SessionEventError SessionEventType = "error"
)

// SessionEvent is a single event in a bidirectional session stream.
// It uses a discriminated union pattern: the Type field determines the schema of Data.
type SessionEvent struct {
	// Type identifies the event kind (see SessionEventType constants).
	Type SessionEventType `json:"type"`
	// Data is the event payload, whose schema depends on Type.
	Data json.RawMessage `json:"data,omitempty"`
}

// Typed constructors for common client-to-server events.

// NewAudioBufferAppendEvent creates an event that sends base64-encoded audio to the server.
func NewAudioBufferAppendEvent(audioBase64 string) SessionEvent {
	data, _ := json.Marshal(map[string]string{"audio": audioBase64})
	return SessionEvent{Type: SessionEventInputAudioBufferAppend, Data: data}
}

// NewAudioBufferCommitEvent creates an event that commits the current audio buffer.
func NewAudioBufferCommitEvent() SessionEvent {
	return SessionEvent{Type: SessionEventInputAudioBufferCommit}
}

// NewAudioBufferClearEvent creates an event that clears the audio buffer.
func NewAudioBufferClearEvent() SessionEvent {
	return SessionEvent{Type: SessionEventInputAudioBufferClear}
}

// NewResponseCreateEvent creates an event that triggers a model response.
func NewResponseCreateEvent() SessionEvent {
	return SessionEvent{Type: SessionEventResponseCreate}
}

// NewResponseCreateEventWithInstructions creates a response request carrying
// optional provider instructions. Empty instructions preserve the legacy
// response.create event shape used for ordinary continuations.
func NewResponseCreateEventWithInstructions(instructions string) SessionEvent {
	if instructions == "" {
		return NewResponseCreateEvent()
	}
	data, _ := json.Marshal(map[string]any{
		"response": map[string]string{
			"instructions": instructions,
		},
	})
	return SessionEvent{Type: SessionEventResponseCreate, Data: data}
}

// NewResponseCancelEvent creates an event that cancels an in-progress response.
func NewResponseCancelEvent() SessionEvent {
	return SessionEvent{Type: SessionEventResponseCancel}
}

// NewSessionUpdateEvent creates an event that updates the session configuration.
func NewSessionUpdateEvent(config json.RawMessage) SessionEvent {
	return SessionEvent{Type: SessionEventSessionUpdate, Data: config}
}
