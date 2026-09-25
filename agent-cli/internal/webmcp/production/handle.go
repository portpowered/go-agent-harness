package production

import (
	"context"
	"errors"
	"sync"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production/normalize"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production/targetsession"
)

const phaseOpenTab = "open_tab"

// handle is one command-scoped browser handle. Target listing and selection
// flow through discovery; the raw handle is used only for exact targets.
type handle struct {
	owner     *composition
	candidate webmcp.BrowserCandidate
	raw       webmcp.BrowserHandle
	closeOnce sync.Once
	closeErr  error
	closed    chan struct{}
}

var (
	_ webmcp.BrowserHandle    = (*handle)(nil)
	_ webmcp.BrowserTabOpener = (*handle)(nil)
	_ webmcp.DevToolsCatalog  = (*catalog)(nil)
)

func (h *handle) Candidate() webmcp.BrowserCandidate {
	if h == nil {
		return webmcp.BrowserCandidate{}
	}
	return h.candidate
}

func (h *handle) ListTargets(ctx context.Context) ([]webmcp.Target, error) {
	if h == nil || h.owner == nil || h.isClosed() {
		return nil, webmcp.ErrClosed
	}
	lane := h.owner.laneCandidateForCore(h.candidate)
	snapshot, err := h.owner.discovery.ListTargetSnapshot(ctx, lane, discovery.TargetListOptions{
		BrowserID:            lane.ID,
		EligibleOnly:         discovery.Bool(false),
		IncludeZeroToolPages: true,
	})
	if err != nil {
		var discoveryErr *discovery.DiscoveryError
		if errors.As(err, &discoveryErr) && discoveryErr.Code == discovery.CodeNoEligibleTab && len(snapshot.Targets) == 0 {
			return []webmcp.Target{}, nil
		}
		return nil, normalize.DiscoveryError(err)
	}
	targets := make([]webmcp.Target, 0, len(snapshot.Targets))
	for _, target := range snapshot.Targets {
		targets = append(targets, normalize.NeutralTarget(h.candidate.ID, target))
	}
	return targets, nil
}

func (h *handle) usable() bool {
	return h != nil && h.owner != nil && h.raw != nil && !h.isClosed()
}

func (h *handle) Activate(ctx context.Context, targetID webmcp.TargetID) error {
	if !h.usable() {
		return webmcp.ErrClosed
	}
	rawTarget, err := h.owner.rawTargetForPublicID(ctx, h.raw, string(h.candidate.ID), targetID)
	if err != nil {
		return err
	}
	return h.raw.Activate(ctx, rawTarget.ID)
}

// OpenTab preserves the optional tab-creation capability exposed by the raw
// Chrome handle while translating its transport target ID into the opaque ID
// used by the production discovery and selection boundary.
func (h *handle) OpenTab(ctx context.Context, rawURL string) (webmcp.Target, error) {
	if !h.usable() {
		return webmcp.Target{}, webmcp.ErrClosed
	}
	opener, ok := h.raw.(webmcp.BrowserTabOpener)
	if !ok {
		return webmcp.Target{}, webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, "The connected browser cannot open a new tab.", map[string]any{
			"phase":  phaseOpenTab,
			"reason": "unsupported_operation",
		})
	}
	opened, err := opener.OpenTab(ctx, rawURL)
	if err != nil {
		return webmcp.Target{}, err
	}
	if opened.ID == "" {
		return webmcp.Target{}, webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, "The browser did not return a target for the new tab.", map[string]any{
			"phase":  phaseOpenTab,
			"reason": "target_id_missing",
		})
	}
	opened.BrowserID = h.candidate.ID
	opened.ID = h.owner.publicTargetID(string(h.candidate.ID), opened.ID)
	opened.URL = normalize.SafePageURL(opened.URL)
	opened.Origin = normalize.SafeOrigin(opened.Origin)
	opened.WebSocketURL = ""
	opened.ContinuityMarker = ""
	return opened, nil
}

func (h *handle) Attach(ctx context.Context, targetID webmcp.TargetID, ownership webmcp.TargetOwnership) (webmcp.TargetSession, error) {
	if !h.usable() {
		return nil, webmcp.ErrClosed
	}
	lane := h.owner.laneCandidateForCore(h.candidate)
	rawTarget, err := h.owner.rawTargetForPublicID(ctx, h.raw, string(h.candidate.ID), targetID)
	if err != nil {
		return nil, err
	}
	rawSession, err := h.raw.Attach(ctx, rawTarget.ID, ownership)
	if err != nil {
		return nil, err
	}
	selection, selectErr := h.owner.discovery.Select(ctx, discovery.TargetSelectionRequest{
		Browser:   lane,
		BrowserID: lane.ID,
		TargetID:  string(targetID),
		Reason:    "broker_exact_selection",
	})
	if selectErr != nil {
		_ = rawSession.Close() //nolint:errcheck // The selection failure is the primary error; the raw session is only released.
		return nil, normalize.DiscoveryError(selectErr)
	}
	publicTarget := normalize.NeutralTarget(h.candidate.ID, selection.Target)
	if publicTarget.ID == "" {
		publicTarget = webmcp.Target{
			BrowserID:  h.candidate.ID,
			ID:         targetID,
			Type:       rawTarget.Type,
			Title:      rawTarget.Title,
			URL:        normalize.SafePageURL(rawTarget.URL),
			Origin:     normalize.SafeOrigin(rawTarget.Origin),
			Eligible:   true,
			Generation: 1,
		}
	}
	return targetsession.New(rawSession, publicTarget)
}

func (h *handle) Close() error {
	if h == nil {
		return nil
	}
	h.closeOnce.Do(func() {
		if h.raw != nil {
			h.closeErr = h.raw.Close()
		}
		if h.closed != nil {
			close(h.closed)
		}
	})
	if h.closed != nil {
		<-h.closed
	}
	return h.closeErr
}

func (h *handle) isClosed() bool {
	if h == nil || h.closed == nil {
		return false
	}
	select {
	case <-h.closed:
		return true
	default:
		return false
	}
}

// catalog serves version and target metadata through the composition so
// public IDs stay normalized by discovery.
type catalog struct{ owner *composition }

func (c *catalog) Version(ctx context.Context, candidate webmcp.BrowserCandidate) (webmcp.BrowserVersion, error) {
	if c == nil || c.owner == nil {
		return webmcp.BrowserVersion{}, webmcp.ErrClosed
	}
	enriched, err := c.owner.enrichCoreCandidate(ctx, candidate)
	if err != nil {
		return webmcp.BrowserVersion{}, err
	}
	if c.owner.catalog != nil {
		return c.owner.catalog.Version(ctx, enriched)
	}
	return webmcp.BrowserVersion{
		Browser:              enriched.Product,
		ProtocolVersion:      enriched.Protocol,
		WebSocketDebuggerURL: enriched.BrowserWSURL,
		BrowserInstanceID:    enriched.BrowserInstanceID,
	}, nil
}

func (c *catalog) ListTargets(ctx context.Context, candidate webmcp.BrowserCandidate) ([]webmcp.Target, error) {
	if c == nil || c.owner == nil {
		return nil, webmcp.ErrClosed
	}
	return (&handle{owner: c.owner, candidate: candidate, closed: make(chan struct{})}).ListTargets(ctx)
}
