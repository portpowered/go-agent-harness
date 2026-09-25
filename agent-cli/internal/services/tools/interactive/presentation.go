package interactive

import (
	"errors"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/sight"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const (
	screenRecheckOperation = "show permission re-check"
	pageSightUnavailable   = "Browser-page sight is unavailable."
	pageSightCause         = "browser-page sight unavailable"
	pageSightFallback      = `{"version":2,"status":"error","source":"browser_page","error_code":"page_sight_unavailable","error":"Browser-page sight is unavailable."}`
)

// Presentation projects CLI display and page-sight failures onto the
// session-turn executor.
func Presentation() sessionturn.ToolPresentation {
	return sessionturn.ToolPresentation{
		DisplayTool:             cliTools.IsPhysicalDisplayToolName,
		DisplayFailure:          cliTools.ScreenToolSessionErrorResult,
		DisplayErrorCode:        cliTools.ScreenToolErrorCode,
		DisplayPermissionDenied: screenPermissionDenied,
		PageSightFailure:        pageSightFailure,
		DisplaySource:           sight.SourceScreen,
		PageSightSource:         sight.SourceBrowserPage,
	}
}

func screenPermissionDenied(permission runtimeTools.DisplayPermission) error {
	return &cliTools.ScreenCaptureError{State: cliTools.ScreenCaptureDenied, Operation: screenRecheckOperation, Reason: permission.Reason}
}

func pageSightFailure() string {
	result := sight.NewError(sight.SourceBrowserPage, errors.New(pageSightCause))
	result.Error = pageSightUnavailable
	result.ErrorCode = sessionturn.PageSightUnavailableErrorCode
	encoded, err := sight.Encode(result)
	if err != nil {
		return pageSightFallback
	}
	return string(encoded)
}
