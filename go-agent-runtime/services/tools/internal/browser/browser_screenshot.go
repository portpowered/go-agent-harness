package browser

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"mime"
	"strings"

	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

// ValidatePageScreenshot enforces the tools-owned capture boundary before a
// host can project image bytes to a model. Browser adapters remain responsible
// for obtaining the bytes and target identity.
func ValidatePageScreenshot(screenshot public.PageScreenshot) (public.ValidatedPageScreenshot, error) {
	if err := validateScreenshotIdentity(screenshot); err != nil {
		return public.ValidatedPageScreenshot{}, err
	}
	mimeType, err := screenshotMIMEType(screenshot.MIMEType)
	if err != nil {
		return public.ValidatedPageScreenshot{}, err
	}
	decoded, format, err := decodeScreenshot(screenshot.Bytes)
	if err != nil {
		return public.ValidatedPageScreenshot{}, err
	}
	if err := validateScreenshotFormat(mimeType, format); err != nil {
		return public.ValidatedPageScreenshot{}, err
	}
	width, height, err := screenshotDimensions(screenshot, decoded)
	if err != nil {
		return public.ValidatedPageScreenshot{}, err
	}
	return public.ValidatedPageScreenshot{
		BrowserID: strings.TrimSpace(screenshot.BrowserID),
		TargetID:  strings.TrimSpace(screenshot.TargetID),
		MIMEType:  mimeType,
		Bytes:     append([]byte(nil), screenshot.Bytes...),
		Width:     width,
		Height:    height,
	}, nil
}

func validateScreenshotIdentity(screenshot public.PageScreenshot) error {
	if strings.TrimSpace(screenshot.BrowserID) == "" || strings.TrimSpace(screenshot.TargetID) == "" {
		return invalidScreenshot("missing_target_identity", nil)
	}
	if len(screenshot.Bytes) == 0 {
		return invalidScreenshot("empty_capture", nil)
	}
	return nil
}

func screenshotMIMEType(raw string) (string, error) {
	mimeType, _, err := mime.ParseMediaType(strings.TrimSpace(raw))
	if err != nil {
		return "", invalidScreenshot("invalid_mime_type", err)
	}
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	if mimeType != "image/png" && mimeType != "image/jpeg" {
		return "", invalidScreenshot("unsupported_mime_type", nil)
	}
	return mimeType, nil
}

func decodeScreenshot(raw []byte) (image.Image, string, error) {
	decoded, format, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, "", invalidScreenshot("malformed_image", err)
	}
	return decoded, strings.ToLower(strings.TrimSpace(format)), nil
}

func validateScreenshotFormat(mimeType, format string) error {
	expectedFormat := "png"
	if mimeType == "image/jpeg" {
		expectedFormat = "jpeg"
	}
	if format != expectedFormat {
		return invalidScreenshot("mime_mismatch", fmt.Errorf("declared %s, decoded %s", mimeType, format))
	}
	return nil
}

func screenshotDimensions(screenshot public.PageScreenshot, decoded image.Image) (int, int, error) {
	width := decoded.Bounds().Dx()
	height := decoded.Bounds().Dy()
	if width <= 0 || height <= 0 {
		return 0, 0, invalidScreenshot("invalid_dimensions", nil)
	}
	if screenshot.Width < 0 || screenshot.Height < 0 ||
		(screenshot.Width != 0 && screenshot.Width != width) ||
		(screenshot.Height != 0 && screenshot.Height != height) {
		return 0, 0, invalidScreenshot("dimension_mismatch", nil)
	}
	return width, height, nil
}

func invalidScreenshot(reason string, cause error) error {
	if strings.TrimSpace(reason) == "" {
		reason = "invalid_capture"
	}
	classified := NewClassifiedError(ErrorInvocationFailed, "The browser returned an invalid page capture.", map[string]any{
		"phase":       "capture_page",
		"reason_code": reason,
	})
	classified.Cause = cause
	return classified
}
