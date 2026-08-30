package tools

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"mime"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/sight"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const (
	// ShowPageResultVersion is the version of the bounded metadata returned by
	// show_page. The image bytes never cross the textual tool-result boundary.
	ShowPageResultVersion                   = sight.ResultVersion
	ShowPageResultStatusSuccess             = sight.StatusSuccess
	ShowPageResultStatusError               = sight.StatusError
	ShowPageResultTypedProjectionInputImage = sight.TypedProjectionInputImage
	showPageSource                          = sight.SourceBrowserPage
)

// ShowPageResult is the model-facing metadata for a validated page capture.
// It intentionally contains no image bytes or page-owned content.
type ShowPageResult = sight.Result

type validatedPageScreenshot struct {
	browserID webmcp.BrowserID
	targetID  webmcp.TargetID
	mimeType  string
	bytes     []byte
	width     int
	height    int
}

func (s *BrokerToolSet) capturePage(ctx context.Context) ([]byte, error) {
	encoded, _, err := s.capturePageRich(ctx)
	return encoded, err
}

// capturePageRich returns the existing classified WebMCP envelope plus the
// exact validated bytes that belong in the one typed image projection.
func (s *BrokerToolSet) capturePageRich(ctx context.Context) ([]byte, messages.ImagePart, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.broker == nil {
		encoded, err := disabledEnvelope()
		return encoded, messages.ImagePart{}, err
	}
	capturer, ok := s.broker.(webmcp.PageScreenshotter)
	if !ok {
		encoded, err := brokerFailure(webmcp.NewClassifiedError(
			webmcp.ErrorUnsupportedWebMCP,
			"the selected browser page does not support screenshot capture",
			map[string]any{
				"phase":      "capture_page",
				"capability": webmcp.PageCaptureScreenshotMethod,
			},
		), webmcp.ErrorUnsupportedWebMCP, nil)
		return encoded, messages.ImagePart{}, err
	}

	screenshot, err := capturer.CapturePageScreenshot(ctx)
	if captureErr := ctx.Err(); captureErr != nil {
		encoded, err := brokerFailure(captureErr, webmcp.ErrorInvocationFailed, map[string]any{
			"phase":      "capture_page",
			"capability": webmcp.PageCaptureScreenshotMethod,
		})
		return encoded, messages.ImagePart{}, err
	}
	if err != nil {
		encoded, encodeErr := brokerFailure(err, webmcp.ErrorInvocationFailed, map[string]any{
			"phase":      "capture_page",
			"capability": webmcp.PageCaptureScreenshotMethod,
		})
		return encoded, messages.ImagePart{}, encodeErr
	}
	validated, err := validatePageScreenshot(screenshot)
	if captureErr := ctx.Err(); captureErr != nil {
		encoded, encodeErr := brokerFailure(captureErr, webmcp.ErrorInvocationFailed, map[string]any{
			"phase":      "capture_page",
			"capability": webmcp.PageCaptureScreenshotMethod,
		})
		return encoded, messages.ImagePart{}, encodeErr
	}
	if err != nil {
		encoded, encodeErr := brokerFailure(err, webmcp.ErrorInvocationFailed, map[string]any{
			"phase":      "capture_page",
			"capability": webmcp.PageCaptureScreenshotMethod,
		})
		return encoded, messages.ImagePart{}, encodeErr
	}
	result, err := sight.NewSuccess(showPageSource, validated.mimeType, validated.bytes, validated.width, validated.height)
	if err != nil {
		return nil, messages.ImagePart{}, err
	}
	result.BrowserID = string(validated.browserID)
	result.TargetID = string(validated.targetID)
	encoded, err := webmcp.EncodeToolResult(result, nil)
	if err != nil {
		return nil, messages.ImagePart{}, err
	}
	return encoded, messages.ImagePart{
		Bytes:     append([]byte(nil), validated.bytes...),
		MediaType: result.MIMEType,
	}, nil
}

func validatePageScreenshot(screenshot webmcp.PageScreenshot) (validatedPageScreenshot, error) {
	if screenshot.BrowserID == "" || screenshot.TargetID == "" {
		return validatedPageScreenshot{}, invalidPageScreenshot("missing_target_identity", nil)
	}
	if len(screenshot.Bytes) == 0 {
		return validatedPageScreenshot{}, invalidPageScreenshot("empty_capture", nil)
	}
	mimeType, _, err := mime.ParseMediaType(strings.TrimSpace(screenshot.MIMEType))
	if err != nil {
		return validatedPageScreenshot{}, invalidPageScreenshot("invalid_mime_type", err)
	}
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	if mimeType != "image/png" && mimeType != "image/jpeg" {
		return validatedPageScreenshot{}, invalidPageScreenshot("unsupported_mime_type", nil)
	}

	decoded, format, err := image.Decode(bytes.NewReader(screenshot.Bytes))
	if err != nil {
		return validatedPageScreenshot{}, invalidPageScreenshot("malformed_image", err)
	}
	format = strings.ToLower(strings.TrimSpace(format))
	expectedFormat := "png"
	if mimeType == "image/jpeg" {
		expectedFormat = "jpeg"
	}
	if format != expectedFormat {
		return validatedPageScreenshot{}, invalidPageScreenshot("mime_mismatch", fmt.Errorf("declared %s, decoded %s", mimeType, format))
	}
	width := decoded.Bounds().Dx()
	height := decoded.Bounds().Dy()
	if width <= 0 || height <= 0 {
		return validatedPageScreenshot{}, invalidPageScreenshot("invalid_dimensions", nil)
	}
	if screenshot.Width < 0 || screenshot.Height < 0 ||
		(screenshot.Width != 0 && screenshot.Width != width) ||
		(screenshot.Height != 0 && screenshot.Height != height) {
		return validatedPageScreenshot{}, invalidPageScreenshot("dimension_mismatch", nil)
	}
	return validatedPageScreenshot{
		browserID: screenshot.BrowserID,
		targetID:  screenshot.TargetID,
		mimeType:  mimeType,
		bytes:     append([]byte(nil), screenshot.Bytes...),
		width:     width,
		height:    height,
	}, nil
}

func invalidPageScreenshot(reason string, cause error) error {
	if strings.TrimSpace(reason) == "" {
		reason = "invalid_capture"
	}
	return &webmcp.ClassifiedError{
		Code:      webmcp.ErrorInvocationFailed,
		Message:   "The browser returned an invalid page capture.",
		Retryable: false,
		Details: map[string]any{
			"phase":       "capture_page",
			"reason_code": reason,
		},
		Cause: cause,
	}
}
