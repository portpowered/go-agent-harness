package messages

// Audio, voice-activity, and transcript stream values and their constructors live in this file.

// AudioStartValue is the value for AUDIO.START (inner type "audio_start").
type AudioStartValue struct {
	Type string `json:"type"` // "audio_start"
}

func (*AudioStartValue) streamMessageValue() {}

// NewAudioStartValue returns a value for AUDIO.START.
func NewAudioStartValue() *AudioStartValue { return &AudioStartValue{Type: "audio_start"} }

// AudioDeltaValue is the value for AUDIO.DELTA (inner type "delta_audio").
type AudioDeltaValue struct {
	Type      string `json:"type"`                 // "delta_audio"
	Content   []byte `json:"content"`              // incremental audio bytes (PCM 16kHz in memory)
	MediaType string `json:"media_type,omitempty"` // provider audio format when known
}

func (*AudioDeltaValue) streamMessageValue() {}

// NewAudioDeltaValue returns a value for AUDIO.DELTA.
// Content should be raw PCM bytes (16kHz) in memory; encoding (OPUS, base64) is the caller's concern.
func NewAudioDeltaValue(content []byte) *AudioDeltaValue {
	return &AudioDeltaValue{Type: "delta_audio", Content: content}
}

// NewAudioDeltaValueWithMediaType returns an AUDIO.DELTA value with format metadata.
func NewAudioDeltaValueWithMediaType(content []byte, mediaType string) *AudioDeltaValue {
	return &AudioDeltaValue{Type: "delta_audio", Content: content, MediaType: mediaType}
}

// AudioEndValue is the value for AUDIO.END (inner type "audio_end").
type AudioEndValue struct {
	Type string `json:"type"` // "audio_end"
}

func (*AudioEndValue) streamMessageValue() {}

// NewAudioEndValue returns a value for AUDIO.END.
func NewAudioEndValue() *AudioEndValue { return &AudioEndValue{Type: "audio_end"} }

// VADSpeechStartedValue is the value for VAD.SPEECH_STARTED.
// VAD events are session-level signals with no payload beyond the signal itself.
type VADSpeechStartedValue struct {
	Type string `json:"type"` // "vad_speech_started"
}

func (*VADSpeechStartedValue) streamMessageValue() {}

// NewVADSpeechStartedValue returns a value for VAD.SPEECH_STARTED.
func NewVADSpeechStartedValue() *VADSpeechStartedValue {
	return &VADSpeechStartedValue{Type: "vad_speech_started"}
}

// VADSpeechStoppedValue is the value for VAD.SPEECH_STOPPED.
type VADSpeechStoppedValue struct {
	Type string `json:"type"` // "vad_speech_stopped"
}

func (*VADSpeechStoppedValue) streamMessageValue() {}

// NewVADSpeechStoppedValue returns a value for VAD.SPEECH_STOPPED.
func NewVADSpeechStoppedValue() *VADSpeechStoppedValue {
	return &VADSpeechStoppedValue{Type: "vad_speech_stopped"}
}

// TranscriptStartValue is the value for TRANSCRIPT.START.
type TranscriptStartValue struct {
	Type string `json:"type"` // "transcript_start"
}

func (*TranscriptStartValue) streamMessageValue() {}

// NewTranscriptStartValue returns a value for TRANSCRIPT.START.
func NewTranscriptStartValue() *TranscriptStartValue {
	return &TranscriptStartValue{Type: "transcript_start"}
}

// TranscriptDeltaValue is the value for TRANSCRIPT.DELTA.
type TranscriptDeltaValue struct {
	Type string `json:"type"` // "transcript_delta"
	Text string `json:"text"` // incremental ASR transcript text
}

func (*TranscriptDeltaValue) streamMessageValue() {}

// NewTranscriptDeltaValue returns a value for TRANSCRIPT.DELTA.
func NewTranscriptDeltaValue(text string) *TranscriptDeltaValue {
	return &TranscriptDeltaValue{Type: "transcript_delta", Text: text}
}

// TranscriptEndValue is the value for TRANSCRIPT.END.
type TranscriptEndValue struct {
	Type     string `json:"type"`      // "transcript_end"
	FullText string `json:"full_text"` // complete accumulated transcript
}

func (*TranscriptEndValue) streamMessageValue() {}

// NewTranscriptEndValue returns a value for TRANSCRIPT.END.
func NewTranscriptEndValue(fullText string) *TranscriptEndValue {
	return &TranscriptEndValue{Type: "transcript_end", FullText: fullText}
}
