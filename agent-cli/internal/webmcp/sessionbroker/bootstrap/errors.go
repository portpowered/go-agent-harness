package bootstrap

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/chrome"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production/normalize"
)

// Detail keys and fallback guidance shared by the managed-browser errors.
const (
	detailPhase             = "phase"
	detailRemediation       = "remediation"
	fallbackRemediation     = "retry the managed browser operation"
	fallbackLaunchPhase     = "startup"
	fallbackLifecyclePhase  = "lifecycle"
	fallbackLaunchModeLabel = "unknown"
)

// capabilityError classifies a bootstrap failure. Cancellation and
// already-classified errors pass through unchanged; discovery and managed
// browser failures keep their specific code; anything else becomes a
// browser-protocol failure of the session bootstrap phase.
func capabilityError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var classified *webmcp.ClassifiedError
	if errors.As(err, &classified) && classified != nil {
		return err
	}
	// DiscoveryError returns its argument unchanged when no conversion applies.
	if converted := normalize.DiscoveryError(err); converted != err { //nolint:errorlint // identity check, not error matching
		return converted
	}
	if converted := managedBrowserError(err); converted != nil {
		return converted
	}
	wrapped := webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, "WebMCP session capability initialization failed", map[string]any{
		detailPhase: bootstrapPhase,
	})
	wrapped.Cause = err
	return wrapped
}

func managedBrowserError(err error) error {
	var launchErr *chrome.ManagedBrowserLaunchError
	if errors.As(err, &launchErr) && launchErr != nil {
		phase := chrome.SafeManagedBrowserLabel(launchErr.Phase, fallbackLaunchPhase)
		mode := chrome.SafeManagedBrowserLabel(launchErr.Mode, fallbackLaunchModeLabel)
		message := launchErr.Error()
		var acquisitionErr *chrome.ManagedChromeAcquisitionError
		if errors.As(err, &acquisitionErr) && acquisitionErr != nil {
			message = fmt.Sprintf("managed WebMCP browser launch failed during %s in %s mode; %s", phase, mode, acquisitionErr.Error())
		}
		classified := webmcp.NewClassifiedError(webmcp.ErrorEndpointUnreachable, message, map[string]any{
			detailPhase:       phase,
			"mode":            mode,
			detailRemediation: managedBrowserRemediation(phase),
		})
		classified.Cause = err
		return classified
	}
	var lifecycleErr *chrome.ManagedBrowserLifecycleError
	if errors.As(err, &lifecycleErr) && lifecycleErr != nil {
		phase := chrome.SafeManagedBrowserLabel(lifecycleErr.Phase, fallbackLifecyclePhase)
		classified := webmcp.NewClassifiedError(webmcp.ErrorEndpointUnreachable, lifecycleErr.Error(), map[string]any{
			detailPhase:       phase,
			detailRemediation: managedBrowserRemediation(phase),
		})
		classified.Cause = err
		return classified
	}
	return nil
}

func managedBrowserRemediation(phase string) string {
	if remediation, known := chrome.ManagedBrowserRemediation(phase); known {
		return remediation
	}
	return fallbackRemediation
}
