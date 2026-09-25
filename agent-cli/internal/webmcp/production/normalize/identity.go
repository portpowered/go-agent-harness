package normalize

import (
	"errors"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

const maxOpaqueIDLength = 64

// OpaqueID reports whether value is a bounded opaque identifier made only of
// ASCII letters, digits, underscores, and hyphens.
func OpaqueID(value string) bool {
	if len(value) < 1 || len(value) > maxOpaqueIDLength {
		return false
	}
	for _, character := range value {
		if character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

// SplitCompositeTargetRef accepts the exact "browserID/targetID" reference
// form printed by the tabs, context, and tools outputs. Both halves must be
// valid opaque IDs; any other shape is reported as not composite.
func SplitCompositeTargetRef(value string) (string, string, bool) {
	value = strings.TrimSpace(value)
	browserPart, targetPart, found := strings.Cut(value, "/")
	if !found {
		return "", "", false
	}
	browserID := strings.TrimSpace(browserPart)
	targetID := strings.TrimSpace(targetPart)
	if !OpaqueID(browserID) || !OpaqueID(targetID) {
		return "", "", false
	}
	return browserID, targetID, true
}

// NeutralTarget converts a discovery target into the browser-neutral broker
// shape, redacting its page URL and origin.
func NeutralTarget(browserID webmcp.BrowserID, target discovery.Target) webmcp.Target {
	return webmcp.Target{
		BrowserID:             browserID,
		ID:                    webmcp.TargetID(target.ID),
		Type:                  target.Type,
		Title:                 target.Title,
		URL:                   SafePageURL(target.URL),
		Origin:                SafeOrigin(target.Origin),
		ContinuityMarker:      target.ContinuityMarker,
		Generation:            target.Generation,
		Eligible:              target.Eligible,
		EligibilityReason:     target.EligibilityReason,
		WebMCPDomainSupported: target.WebMCPDomainSupported,
		PageToolsReady:        target.PageToolsReady,
		PageToolsKnown:        target.PageToolsKnown,
		PageToolsEvidence:     target.PageToolsEvidence,
		DocumentReadyState:    target.DocumentReadyState,
		DocumentLoading:       target.DocumentLoading,
		DocumentLoadingKnown:  target.DocumentLoadingKnown,
	}
}

// DiscoverySource maps a discovery lane source onto the neutral broker enum.
func DiscoverySource(source discovery.Source) webmcp.DiscoverySource {
	switch source {
	case discovery.SourceExplicitCDPHTTP, discovery.SourceExplicitBrowserWS:
		return webmcp.DiscoverySourceExplicit
	case discovery.SourceDevToolsActivePort:
		return webmcp.DiscoverySourceActivePort
	case discovery.SourceProcess:
		return webmcp.DiscoverySourceProcess
	case discovery.SourceConfigured:
		fallthrough
	default:
		return webmcp.DiscoverySourceConfigured
	}
}

// DiscoveryError translates a discovery-lane error into the broker's
// classified error, preserving its message, retryability, and details.
// Errors that are not discovery errors are returned unchanged.
func DiscoveryError(err error) error {
	if err == nil {
		return nil
	}
	var discoveryErr *discovery.DiscoveryError
	if !errors.As(err, &discoveryErr) || discoveryErr == nil {
		return err
	}
	code := webmcp.ErrorCode(discoveryErr.Code)
	if !webmcp.IsKnownErrorCode(code) {
		code = webmcp.ErrorBrowserProtocol
	}
	return &webmcp.ClassifiedError{
		Code:      code,
		Message:   discoveryErr.Message,
		Retryable: discoveryErr.Retryable,
		Details:   discoveryErr.Details,
		Cause:     discoveryErr,
	}
}

// IsUnsupportedWebMCPError reports whether err classifies a page that does
// not expose the WebMCP domain.
func IsUnsupportedWebMCPError(err error) bool {
	var classified *webmcp.ClassifiedError
	return errors.As(err, &classified) && classified != nil && classified.Code == webmcp.ErrorUnsupportedWebMCP
}
