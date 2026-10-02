package gateway

import (
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

// Re-export session types from models so gateway consumers can use them
// without importing the models package directly.

type AudioFormat = models.AudioFormat
type SampleRate = models.SampleRate

// DefaultInputAudioTranscriptionModel is the default OpenAI customer-audio
// transcription model exposed through the gateway package.
const DefaultInputAudioTranscriptionModel = models.DefaultInputAudioTranscriptionModel
