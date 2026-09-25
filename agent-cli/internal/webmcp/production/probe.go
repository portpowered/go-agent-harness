package production

import (
	"context"
	"errors"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production/normalize"
)

// unknownToolCount marks a capability whose tool count was not observed.
const unknownToolCount = -1

type targetLister struct{ owner *composition }

func (l targetLister) List(ctx context.Context, browser discovery.BrowserCandidate) ([]discovery.TargetDescriptor, error) {
	if l.owner == nil {
		return nil, webmcp.ErrClosed
	}
	return l.owner.listRawTargetDescriptors(ctx, browser)
}

type targetProbe struct{ owner *composition }

func (p targetProbe) Probe(ctx context.Context, browser discovery.BrowserCandidate, target discovery.Target) (discovery.TargetCapabilities, error) {
	if p.owner == nil {
		return discovery.TargetCapabilities{}, webmcp.ErrClosed
	}
	// An exact selection must not wait for unrelated suspended/restored pages.
	// Their metadata remains listed, but capability checks belong to the target
	// the caller selected. Unknown capabilities never authorize invocation.
	selected := strings.TrimSpace(p.owner.browser.Selection.Tab)
	if refBrowserID, refTargetID, composite := normalize.SplitCompositeTargetRef(selected); composite {
		// A composite reference identifies both the browser and target. Keep
		// the browser half authoritative so the target ID cannot accidentally
		// authorize a same-named tab in another browser.
		if browser.ID != refBrowserID {
			return discovery.TargetCapabilities{ToolCount: unknownToolCount}, nil
		}
		selected = refTargetID
	}
	if selected != "" && selected != target.ID {
		return discovery.TargetCapabilities{ToolCount: unknownToolCount}, nil
	}
	return p.owner.probeTarget(ctx, browser, target)
}

func (p *composition) listRawTargetDescriptors(ctx context.Context, lane discovery.BrowserCandidate) ([]discovery.TargetDescriptor, error) {
	candidate, err := p.coreCandidateForLane(ctx, lane)
	if err != nil {
		return nil, err
	}
	raw, err := p.runtime.Open(ctx, candidate)
	if err != nil {
		return nil, err
	}
	defer func() { _ = raw.Close() }() //nolint:errcheck // Listing is read-only; a release failure never invalidates the listed targets.
	targets, err := raw.ListTargets(ctx)
	if err != nil {
		return nil, err
	}
	descriptors := make([]discovery.TargetDescriptor, 0, len(targets))
	for _, target := range targets {
		descriptors = append(descriptors, discovery.TargetDescriptor{
			ID:                   string(target.ID),
			Type:                 target.Type,
			Title:                target.Title,
			URL:                  target.URL,
			WebSocketDebuggerURL: target.WebSocketURL,
			ContinuityMarker:     target.ContinuityMarker,
		})
	}
	return descriptors, nil
}

// probeTarget attaches to one exact target as an external observer, enables
// WebMCP, and reports the capabilities observable without waiting. The raw
// session and handle are always released before returning.
func (p *composition) probeTarget(ctx context.Context, lane discovery.BrowserCandidate, target discovery.Target) (discovery.TargetCapabilities, error) {
	raw, session, err := p.attachProbe(ctx, lane, target)
	if err != nil {
		return discovery.TargetCapabilities{}, err
	}
	enableErr := session.EnableWebMCP(ctx)
	var capabilities discovery.TargetCapabilities
	if enableErr == nil {
		capabilities = observedCapabilities(session)
	}
	cleanupErr := errors.Join(session.Close(), raw.Close())
	if enableErr != nil {
		if normalize.IsUnsupportedWebMCPError(enableErr) {
			return discovery.TargetCapabilities{ToolCount: unknownToolCount, DomainKnown: true}, cleanupErr
		}
		return discovery.TargetCapabilities{}, errors.Join(enableErr, cleanupErr)
	}
	if cleanupErr != nil {
		return discovery.TargetCapabilities{}, cleanupErr
	}
	return capabilities, nil
}

func (p *composition) attachProbe(ctx context.Context, lane discovery.BrowserCandidate, target discovery.Target) (webmcp.BrowserHandle, webmcp.TargetSession, error) {
	candidate, err := p.coreCandidateForLane(ctx, lane)
	if err != nil {
		return nil, nil, err
	}
	raw, err := p.runtime.Open(ctx, candidate)
	if err != nil {
		return nil, nil, err
	}
	rawTarget, err := p.rawTargetForPublicID(ctx, raw, lane.ID, webmcp.TargetID(target.ID))
	if err != nil {
		_ = raw.Close() //nolint:errcheck // The lookup/attach failure is the primary error; the raw handle is only released.
		return nil, nil, err
	}
	session, err := raw.Attach(ctx, rawTarget.ID, webmcp.TargetOwnershipExternal)
	if err != nil {
		_ = raw.Close() //nolint:errcheck // The lookup/attach failure is the primary error; the raw handle is only released.
		return nil, nil, err
	}
	return raw, session, nil
}

// observedCapabilities reads the enabled session's page context and then
// drains only the events already buffered; it never waits for new ones.
func observedCapabilities(session webmcp.TargetSession) discovery.TargetCapabilities {
	page := session.Context()
	capabilities := discovery.TargetCapabilities{
		WebMCP:               true,
		DomainSupported:      true,
		DomainKnown:          true,
		ToolCount:            unknownToolCount,
		PageToolsReady:       page.CatalogReady,
		PageToolsKnown:       page.CatalogReady,
		PageToolsEvidence:    page.CatalogEvidence,
		DocumentReadyState:   page.DocumentReadyState,
		DocumentLoading:      page.DocumentLoading,
		DocumentLoadingKnown: page.DocumentLoadingKnown,
	}
	for {
		select {
		case event, ok := <-session.Events():
			if !ok {
				return capabilities
			}
			applyProbeEvent(&capabilities, event)
		default:
			return capabilities
		}
	}
}

func applyProbeEvent(capabilities *discovery.TargetCapabilities, event webmcp.BrowserEvent) {
	if event.Type == webmcp.EventCatalogReady {
		capabilities.PageToolsReady = true
		capabilities.PageToolsKnown = event.ToolCountKnown
		capabilities.PageToolsEvidence = "page_producer"
		if event.ToolCountKnown {
			capabilities.ToolCount = event.ToolCount
			capabilities.ToolCountKnown = true
		}
		return
	}
	if event.Type == webmcp.EventToolsAdded && len(event.Tools) > 0 {
		capabilities.PageToolsReady = true
		capabilities.PageToolsKnown = true
		capabilities.PageToolsEvidence = "tools_added"
		capabilities.ToolCount = len(event.Tools)
		capabilities.ToolCountKnown = true
	}
}

func (p *composition) rawTargetForPublicID(ctx context.Context, raw webmcp.BrowserHandle, browserID string, publicID webmcp.TargetID) (webmcp.Target, error) {
	targets, err := raw.ListTargets(ctx)
	if err != nil {
		return webmcp.Target{}, err
	}
	for _, target := range targets {
		if target.ID == publicID || p.publicTargetID(browserID, target.ID) == publicID {
			return target, nil
		}
	}
	return webmcp.Target{}, webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "the selected browser target is no longer current", map[string]any{
		"browser_id": browserID,
		"target_id":  string(publicID),
		"reason":     "target_not_found",
	})
}
