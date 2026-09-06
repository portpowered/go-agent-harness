package tools

import (
	"context"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/sight"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
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
	validated, err := runtimeToolsWire.NewService().BrowserContract().ValidatePageScreenshot(runtimeTools.PageScreenshot{
		BrowserID: string(screenshot.BrowserID),
		TargetID:  string(screenshot.TargetID),
		MIMEType:  screenshot.MIMEType,
		Bytes:     screenshot.Bytes,
		Width:     screenshot.Width,
		Height:    screenshot.Height,
	})
	if err != nil {
		return validatedPageScreenshot{}, err
	}
	return validatedPageScreenshot{
		browserID: webmcp.BrowserID(validated.BrowserID),
		targetID:  webmcp.TargetID(validated.TargetID),
		mimeType:  validated.MIMEType,
		bytes:     validated.Bytes,
		width:     validated.Width,
		height:    validated.Height,
	}, nil
}
