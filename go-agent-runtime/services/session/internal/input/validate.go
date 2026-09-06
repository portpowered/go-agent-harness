package input

import (
	"fmt"
	"slices"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// conversionHint describes a possible format conversion for a rejected MIME type.
type conversionHint struct {
	targetMime     string
	convertCommand string
}

// conversionHints returns owned suggestions without a mutable process registry.
func conversionHints(rejectedMime string) []conversionHint {
	switch rejectedMime {
	case "image/webp":
		return []conversionHint{
			{targetMime: "image/png", convertCommand: "convert input.webp output.png"},
			{targetMime: "image/jpeg", convertCommand: "convert input.webp output.jpg"},
		}
	case "image/png":
		return []conversionHint{{targetMime: "image/webp", convertCommand: "convert input.png output.webp"}}
	case "image/jpeg":
		return []conversionHint{{targetMime: "image/webp", convertCommand: "convert input.jpg output.webp"}}
	case "image/tiff":
		return []conversionHint{{targetMime: "image/png", convertCommand: "convert input.tiff output.png"}}
	default:
		return nil
	}
}

// findConversionHint returns a tip string if a known conversion exists from the
// rejected MIME type to one of the supported types. Returns empty string otherwise.
func findConversionHint(rejectedMime string, supportedTypes []string) string {
	hints := conversionHints(rejectedMime)
	supported := make(map[string]bool, len(supportedTypes))
	for _, t := range supportedTypes {
		supported[t] = true
	}
	for _, h := range hints {
		if supported[h.targetMime] {
			return fmt.Sprintf(" Tip: Convert with: %s", h.convertCommand)
		}
	}
	return ""
}

// ValidateMimeType checks whether mimeType is accepted by the given model.
// When supportedTypes is nil or empty all types are accepted (backward compatible).
// Returns an error containing the model name, rejected type, supported list,
// and a conversion hint when a known conversion path exists.
func ValidateMimeType(mimeType, modelName string, supportedTypes []string) error {
	if len(supportedTypes) == 0 {
		return nil
	}
	if slices.Contains(supportedTypes, mimeType) {
		return nil
	}
	hint := findConversionHint(mimeType, supportedTypes)
	return fmt.Errorf(
		"model %q does not support input type %q. supported types: %s%s",
		modelName, mimeType, strings.Join(supportedTypes, ", "), hint,
	)
}

// ValidateContentPartsMimeTypes checks every ContentPart that carries a MediaType
// against the model's supported MIME types. Returns the first validation error,
// or nil when all parts are accepted.
func ValidateContentPartsMimeTypes(parts []messages.ContentPart, modelName string, supportedTypes []string) error {
	if len(supportedTypes) == 0 {
		return nil
	}
	for _, p := range parts {
		var mediaType string
		switch v := p.(type) {
		case messages.ImagePart:
			mediaType = v.MediaType
		case messages.AudioPart:
			mediaType = v.MediaType
		case messages.VideoPart:
			mediaType = v.MediaType
		case messages.FilePart:
			mediaType = v.MediaType
		default:
			continue
		}
		if mediaType == "" {
			continue
		}
		if err := ValidateMimeType(mediaType, modelName, supportedTypes); err != nil {
			return err
		}
	}
	return nil
}
