package input

import (
	"testing"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateMimeType_Supported(t *testing.T) {
	err := ValidateMimeType("image/png", "gpt-4o", []string{"image/png", "image/jpeg"})
	assert.NoError(t, err)
}

func TestValidateMimeType_Unsupported(t *testing.T) {
	err := ValidateMimeType("image/webp", "gpt-4o", []string{"image/png", "image/jpeg"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gpt-4o")
	assert.Contains(t, err.Error(), "image/webp")
	assert.Contains(t, err.Error(), "image/png")
	assert.Contains(t, err.Error(), "image/jpeg")
}

func TestValidateMimeType_NilList(t *testing.T) {
	err := ValidateMimeType("image/webp", "gpt-4o", nil)
	assert.NoError(t, err)
}

func TestValidateMimeType_EmptyList(t *testing.T) {
	err := ValidateMimeType("image/webp", "gpt-4o", []string{})
	assert.NoError(t, err)
}

func TestValidateMimeType_ErrorFormat(t *testing.T) {
	err := ValidateMimeType("image/tiff", "claude-3-opus", []string{"image/png", "image/jpeg", "image/gif"})
	require.Error(t, err)
	expected := "Model 'claude-3-opus' does not support input type 'image/tiff'. Supported types: image/png, image/jpeg, image/gif Tip: Convert with: convert input.tiff output.png"
	assert.Equal(t, expected, err.Error())
}

func TestValidateContentPartsMimeTypes_AllSupported(t *testing.T) {
	parts := []messages.ContentPart{
		messages.ImagePart{MediaType: "image/png"},
		messages.ImagePart{MediaType: "image/jpeg"},
	}
	err := ValidateContentPartsMimeTypes(parts, "gpt-4o", []string{"image/png", "image/jpeg"})
	assert.NoError(t, err)
}

func TestValidateContentPartsMimeTypes_OneUnsupported(t *testing.T) {
	parts := []messages.ContentPart{
		messages.ImagePart{MediaType: "image/png"},
		messages.ImagePart{MediaType: "image/webp"},
	}
	err := ValidateContentPartsMimeTypes(parts, "test-model", []string{"image/png", "image/jpeg"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "image/webp")
	assert.Contains(t, err.Error(), "test-model")
}

func TestValidateContentPartsMimeTypes_NilSupportedTypes(t *testing.T) {
	parts := []messages.ContentPart{
		messages.ImagePart{MediaType: "image/webp"},
	}
	err := ValidateContentPartsMimeTypes(parts, "gpt-4o", nil)
	assert.NoError(t, err)
}

func TestValidateContentPartsMimeTypes_TextPartsIgnored(t *testing.T) {
	parts := []messages.ContentPart{
		messages.TextPart{Text: "hello"},
		messages.ImagePart{MediaType: "image/png"},
	}
	err := ValidateContentPartsMimeTypes(parts, "gpt-4o", []string{"image/png"})
	assert.NoError(t, err)
}

func TestValidateContentPartsMimeTypes_AllPartTypes(t *testing.T) {
	parts := []messages.ContentPart{
		messages.ImagePart{MediaType: "image/png"},
		messages.AudioPart{MediaType: "audio/mpeg"},
		messages.VideoPart{MediaType: "video/mp4"},
		messages.FilePart{MediaType: "application/pdf"},
	}
	supported := []string{"image/png", "audio/mpeg", "video/mp4", "application/pdf"}
	err := ValidateContentPartsMimeTypes(parts, "gemini-2.0", supported)
	assert.NoError(t, err)
}

func TestValidateContentPartsMimeTypes_EmptyParts(t *testing.T) {
	err := ValidateContentPartsMimeTypes(nil, "gpt-4o", []string{"image/png"})
	assert.NoError(t, err)
}

func TestValidateContentPartsMimeTypes_EmptyMediaType(t *testing.T) {
	// Parts with empty MediaType should be skipped (not rejected).
	parts := []messages.ContentPart{
		messages.ImagePart{MediaType: ""},
	}
	err := ValidateContentPartsMimeTypes(parts, "gpt-4o", []string{"image/png"})
	assert.NoError(t, err)
}

func TestConversionHint_WebPToPNG(t *testing.T) {
	err := ValidateMimeType("image/webp", "test-model", []string{"image/png", "image/jpeg"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Tip: Convert with: convert input.webp output.png")
}

func TestConversionHint_WebPToJPEG(t *testing.T) {
	// When only JPEG is supported (not PNG), suggest JPEG conversion.
	err := ValidateMimeType("image/webp", "test-model", []string{"image/jpeg"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Tip: Convert with: convert input.webp output.jpg")
}

func TestConversionHint_TIFFToPNG(t *testing.T) {
	err := ValidateMimeType("image/tiff", "test-model", []string{"image/png"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Tip: Convert with: convert input.tiff output.png")
}

func TestConversionHint_NoHintForUnknownPair(t *testing.T) {
	// audio/wav has no known conversion hints — no tip should appear.
	err := ValidateMimeType("audio/wav", "test-model", []string{"image/png"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "Tip:")
}

func TestConversionHint_NoHintWhenTargetNotSupported(t *testing.T) {
	// WebP has hints to PNG and JPEG, but neither is in the supported list.
	err := ValidateMimeType("image/webp", "test-model", []string{"image/gif"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "Tip:")
}

func TestConversionHint_PNGToWebP(t *testing.T) {
	err := ValidateMimeType("image/png", "test-model", []string{"image/webp"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Tip: Convert with: convert input.png output.webp")
}

func TestConversionHint_JPEGToWebP(t *testing.T) {
	err := ValidateMimeType("image/jpeg", "test-model", []string{"image/webp"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Tip: Convert with: convert input.jpg output.webp")
}
