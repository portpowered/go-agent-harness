package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"mime"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

const (
	// ShowPageResultVersion is the version of the bounded metadata returned by
	// show_page. The image bytes never cross the textual tool-result boundary.
	ShowPageResultVersion = "show_page.result.v1"
	showPageSource        = "browser_page"
)

// ShowPageResult is the model-facing metadata for a validated page capture.
// It intentionally contains no image bytes or page-owned content.
type ShowPageResult struct {
	Version    string           `json:"version"`
	Source     string           `json:"source"`
	BrowserID  webmcp.BrowserID `json:"browser_id"`
	TargetID   webmcp.TargetID  `json:"target_id"`
	MIMEType   string           `json:"mime_type"`
	ByteLength int              `json:"byte_length"`
	Width      int              `json:"width"`
	Height     int              `json:"height"`
	SHA256     string           `json:"sha256"`
}

type validatedPageScreenshot struct {
	browserID webmcp.BrowserID
	targetID  webmcp.TargetID
	mimeType  string
	bytes     []byte
	width     int
	height    int
}

func (s *BrokerToolSet) capturePage(ctx context.Context) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	capturer, ok := s.broker.(webmcp.PageScreenshotter)
	if !ok {
		return brokerFailure(webmcp.NewClassifiedError(
			webmcp.ErrorUnsupportedWebMCP,
			"the selected browser page does not support screenshot capture",
			map[string]any{
				"phase":      "capture_page",
				"capability": webmcp.PageCaptureScreenshotMethod,
			},
		), webmcp.ErrorUnsupportedWebMCP, nil)
	}

	screenshot, err := capturer.CapturePageScreenshot(ctx)
	if captureErr := ctx.Err(); captureErr != nil {
		return brokerFailure(captureErr, webmcp.ErrorInvocationFailed, map[string]any{
			"phase":      "capture_page",
			"capability": webmcp.PageCaptureScreenshotMethod,
		})
	}
	if err != nil {
		return brokerFailure(err, webmcp.ErrorInvocationFailed, map[string]any{
			"phase":      "capture_page",
			"capability": webmcp.PageCaptureScreenshotMethod,
		})
	}
	validated, err := validatePageScreenshot(screenshot)
	if captureErr := ctx.Err(); captureErr != nil {
		return brokerFailure(captureErr, webmcp.ErrorInvocationFailed, map[string]any{
			"phase":      "capture_page",
			"capability": webmcp.PageCaptureScreenshotMethod,
		})
	}
	if err != nil {
		return brokerFailure(err, webmcp.ErrorInvocationFailed, map[string]any{
			"phase":      "capture_page",
			"capability": webmcp.PageCaptureScreenshotMethod,
		})
	}
	digest := sha256.Sum256(validated.bytes)
	return webmcp.EncodeToolResult(ShowPageResult{
		Version:    ShowPageResultVersion,
		Source:     showPageSource,
		BrowserID:  validated.browserID,
		TargetID:   validated.targetID,
		MIMEType:   validated.mimeType,
		ByteLength: len(validated.bytes),
		Width:      validated.width,
		Height:     validated.height,
		SHA256:     hex.EncodeToString(digest[:]),
	}, nil)
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
