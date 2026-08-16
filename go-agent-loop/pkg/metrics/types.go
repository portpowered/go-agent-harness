package metrics

import "fmt"

// Direction identifies whether an observed stream moved into or out of the
// agent.
type Direction string

const (
	// DirectionInput identifies bytes entering the agent.
	DirectionInput Direction = "input"
	// DirectionOutput identifies bytes leaving the agent.
	DirectionOutput Direction = "output"

	// Input is a concise alias for DirectionInput.
	Input Direction = DirectionInput
	// Output is a concise alias for DirectionOutput.
	Output Direction = DirectionOutput
)

// Valid reports whether d is one of the directions supported by this package.
func (d Direction) Valid() bool {
	switch d {
	case DirectionInput, DirectionOutput:
		return true
	default:
		return false
	}
}

// Modality identifies the kind of stream represented by an observation.
type Modality string

const (
	// ModalityAudio identifies encoded or raw audio bytes.
	ModalityAudio Modality = "audio"
	// ModalityText identifies text or transcript bytes.
	ModalityText Modality = "text"
	// ModalityImage identifies image input bytes.
	ModalityImage Modality = "image"

	// Audio is a concise alias for ModalityAudio.
	Audio Modality = ModalityAudio
	// Text is a concise alias for ModalityText.
	Text Modality = ModalityText
	// Image is a concise alias for ModalityImage.
	Image Modality = ModalityImage
)

// Valid reports whether m is one of the modalities supported by this package.
func (m Modality) Valid() bool {
	switch m {
	case ModalityAudio, ModalityText, ModalityImage:
		return true
	default:
		return false
	}
}

// SupportedDirections returns the supported directions in deterministic order.
func SupportedDirections() []Direction {
	return []Direction{DirectionInput, DirectionOutput}
}

// SupportedModalities returns the supported modalities in deterministic order.
func SupportedModalities() []Modality {
	return []Modality{ModalityAudio, ModalityText, ModalityImage}
}

// SeriesKey identifies one direction-and-modality metric series.
type SeriesKey struct {
	Direction Direction
	Modality  Modality
}

// String returns a stable human-readable series key.
func (k SeriesKey) String() string {
	return fmt.Sprintf("%s/%s", k.Direction, k.Modality)
}

func orderedSeriesKeys() []SeriesKey {
	keys := make([]SeriesKey, 0, len(SupportedDirections())*len(SupportedModalities()))
	for _, direction := range SupportedDirections() {
		for _, modality := range SupportedModalities() {
			keys = append(keys, SeriesKey{Direction: direction, Modality: modality})
		}
	}
	return keys
}
