package discovery

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultSelectionReason = "explicit"
	maxSelectionReason     = 64
)

// Clock is the small time seam used for deterministic selection metadata.
// Implementations need only return the current wall-clock value; generation
// itself is logical state and never depends on elapsed time.
type Clock interface {
	Now() time.Time
}

// TargetHandle is a detach-only wrapper for an attached target. It deliberately
// has no operation for closing a target, browser, process, or profile. Close
// and Release are idempotent and return the result of the first detach call.
type TargetHandle struct {
	detacher  TargetDetacher
	ownership TargetOwnership
	once      sync.Once
	err       error
}

// NewDetachOnlyTargetHandle wraps an attached target with the external-target
// cleanup contract used by Lane B. A nil detacher produces a usable no-op
// handle, which is convenient for neutral fakes that only exercise selection.
func NewDetachOnlyTargetHandle(detacher TargetDetacher) *TargetHandle {
	return &TargetHandle{
		detacher:  detacher,
		ownership: TargetOwnershipExternal,
	}
}

// NewExternalTargetHandle is a descriptive constructor alias.
func NewExternalTargetHandle(detacher TargetDetacher) *TargetHandle {
	return NewDetachOnlyTargetHandle(detacher)
}

// Ownership reports the cleanup ownership represented by the handle.
func (h *TargetHandle) Ownership() TargetOwnership {
	if h == nil || h.ownership == "" {
		return TargetOwnershipExternal
	}
	return h.ownership
}

// Detach releases the target session without closing the target itself.
func (h *TargetHandle) Detach(ctx context.Context) error {
	if h == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	h.once.Do(func() {
		if h.detacher != nil {
			h.err = h.detacher.Detach(ctx)
		}
	})
	return h.err
}

// Release is an alias for detach-only cleanup.
func (h *TargetHandle) Release() error { return h.Detach(context.Background()) }

// Close is intentionally equivalent to Release. It never invokes a target,
// browser, process, or profile close operation.
func (h *TargetHandle) Close() error { return h.Release() }

// Close releases the selected target handle, if this selection owns one.
// Selection values remain safe to close after the service selects another
// target because the handle is independently idempotent.
func (s Selection) Close() error {
	if s.Handle == nil {
		return nil
	}
	return s.Handle.Close()
}

// Release is an alias for Selection.Close.
func (s Selection) Release() error { return s.Close() }

// Select refreshes the supplied browser's targets and selects one exact
// normalized browser/target pair. An empty TargetID is accepted only to make
// the fail-closed ambiguity/no-match paths observable; it never authorizes a
// selection based on list order or a page's display metadata.
func (s *Service) Select(ctx context.Context, request TargetSelectionRequest) (Selection, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	s.mu.Lock()
	browser, failure := s.resolveSelectionBrowserLocked(request)
	if failure != nil {
		s.mu.Unlock()
		return Selection{}, failure
	}

	listOptions := TargetListOptions{
		BrowserID:            browser.ID,
		TargetID:             strings.TrimSpace(request.TargetID),
		EligibleOnly:         Bool(true),
		IncludeZeroToolPages: true,
	}
	allTargets, failure := s.refreshTargetsLocked(ctx, browser, listOptions)
	if failure != nil {
		s.mu.Unlock()
		return Selection{}, failure
	}

	target, failure := chooseSelectionTarget(browser.ID, allTargets, listOptions.TargetID)
	if failure != nil {
		s.mu.Unlock()
		return Selection{}, failure
	}
	if !target.WebMCP {
		s.mu.Unlock()
		return Selection{}, newUnsupportedWebMCP(browser.ID, target.ID)
	}
	// /json/list has no standard WebMCP capability field. A fake or adapter
	// must either provide that field or install TargetCapabilityProbe so a
	// successful selection represents validated capability rather than an
	// optimistic listing result.
	if !target.WebMCPKnown {
		s.mu.Unlock()
		return Selection{}, newUnsupportedWebMCP(browser.ID, target.ID)
	}

	var handle *TargetHandle
	if s.targetAttacher != nil {
		detacher, attachErr := s.targetAttacher.Attach(ctx, browser, target)
		if attachErr != nil {
			s.mu.Unlock()
			return Selection{}, newTargetAttachFailed(browser.ID, target.ID, "attach", "attach_failed", attachErr)
		}
		handle = NewDetachOnlyTargetHandle(detacher)
	}
	if request.Activate && s.activator != nil {
		if activateErr := s.activator.Activate(ctx, browser, target); activateErr != nil {
			if handle != nil {
				_ = handle.Close()
			}
			s.mu.Unlock()
			return Selection{}, newTargetAttachFailed(browser.ID, target.ID, "activate", "activation_failed", activateErr)
		}
	}

	reason := boundedLabel(request.Reason, maxSelectionReason)
	if reason == "" {
		reason = defaultSelectionReason
	}
	selectedAt := time.Time{}
	if s.clock != nil {
		selectedAt = s.clock.Now()
	}
	if selectedAt.IsZero() {
		selectedAt = time.Unix(0, 0).UTC()
	}
	selected := Selection{
		BrowserID:  browser.ID,
		TargetID:   target.ID,
		Title:      target.Title,
		URL:        target.URL,
		Origin:     target.Origin,
		Generation: target.Generation,
		SelectedAt: selectedAt.UTC(),
		Target:     target,
		Handle:     handle,
	}
	previous := s.selection
	s.selection = &selected
	s.emitTarget(EventTargetSelected, browser.ID, target.ID, target.Generation, map[string]any{
		"generation": target.Generation,
		"reason":     reason,
	})
	if handle != nil {
		s.emitTarget(EventTargetAttached, browser.ID, target.ID, 0, map[string]any{
			"ownership_mode": string(handle.Ownership()),
			"phase":          "attached",
		})
	}
	s.mu.Unlock()

	// Release only after the new exact selection is committed. The old value
	// remains an independent snapshot, so an in-flight caller cannot be
	// redirected to the newly selected target.
	if previous != nil && previous.Handle != nil && previous.Handle != handle {
		_ = previous.Handle.Close()
	}
	return selected, nil
}

// SelectTarget is the convenience form for callers that already have the
// normalized browser candidate returned by Discover.
func (s *Service) SelectTarget(ctx context.Context, browser BrowserCandidate, targetID string, options ...SelectionOptions) (Selection, error) {
	selectionOptions := firstSelectionOptions(options)
	return s.Select(ctx, TargetSelectionRequest{
		Browser:   browser,
		BrowserID: browser.ID,
		TargetID:  targetID,
		Activate:  selectionOptions.Activate,
		Reason:    selectionOptions.Reason,
	})
}

// SelectExact is a descriptive alias for SelectTarget.
func (s *Service) SelectExact(ctx context.Context, browser BrowserCandidate, targetID string, options ...SelectionOptions) (Selection, error) {
	return s.SelectTarget(ctx, browser, targetID, options...)
}

// Selected returns a snapshot of the service's current selection. The boolean
// is false when no selection has been committed.
func (s *Service) Selected() (Selection, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.selection == nil {
		return Selection{}, false
	}
	return *s.selection, true
}

// CurrentSelection is a descriptive alias for Selected.
func (s *Service) CurrentSelection() (Selection, bool) { return s.Selected() }

// ReleaseSelection clears and detaches the current selection. Releasing an
// already empty service is a successful no-op.
func (s *Service) ReleaseSelection() error {
	s.mu.Lock()
	if s.selection == nil {
		s.mu.Unlock()
		return nil
	}
	previous := s.selection
	s.selection = nil
	s.mu.Unlock()
	if previous.Handle == nil {
		return nil
	}
	return previous.Handle.Close()
}

// Close is the service-level selection cleanup hook. Discovery itself owns no
// browser process, so closing the service only releases its attached target.
func (s *Service) Close() error { return s.ReleaseSelection() }

func firstSelectionOptions(options []SelectionOptions) SelectionOptions {
	if len(options) == 0 {
		return SelectionOptions{}
	}
	return options[0]
}

func (s *Service) resolveSelectionBrowserLocked(request TargetSelectionRequest) (BrowserCandidate, *DiscoveryError) {
	requestedID := strings.TrimSpace(request.BrowserID)
	providedID := strings.TrimSpace(request.Browser.ID)
	if requestedID != "" && !publicIDPattern.MatchString(requestedID) {
		return BrowserCandidate{}, newNoEligibleTab(requestedID, TargetListOptions{BrowserID: requestedID, TargetID: request.TargetID}, 0)
	}
	if providedID != "" && !publicIDPattern.MatchString(providedID) {
		return BrowserCandidate{}, newNoEligibleTab(providedID, TargetListOptions{BrowserID: providedID, TargetID: request.TargetID}, 0)
	}
	if requestedID != "" && providedID != "" && requestedID != providedID {
		return BrowserCandidate{}, newNoEligibleTab(requestedID, TargetListOptions{BrowserID: requestedID, TargetID: request.TargetID}, 0)
	}
	if providedID != "" {
		requestedID = providedID
		if s.browsers == nil {
			s.browsers = make(map[string]BrowserCandidate)
		}
		s.browsers[providedID] = request.Browser
	}
	if requestedID == "" {
		ids := make([]string, 0, len(s.browsers))
		for id := range s.browsers {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		switch len(ids) {
		case 0:
			return BrowserCandidate{}, newNoEligibleTab("", TargetListOptions{TargetID: request.TargetID}, 0)
		case 1:
			requestedID = ids[0]
		default:
			return BrowserCandidate{}, newAmbiguousBrowser(ids)
		}
	}
	browser, ok := s.browsers[requestedID]
	if !ok && providedID == requestedID {
		browser = request.Browser
		ok = browser.ID != ""
	}
	if !ok || browser.ID != requestedID {
		return BrowserCandidate{}, newNoEligibleTab(requestedID, TargetListOptions{BrowserID: requestedID, TargetID: request.TargetID}, 0)
	}
	return browser, nil
}

func (s *Service) refreshTargetsLocked(ctx context.Context, browser BrowserCandidate, options TargetListOptions) ([]Target, *DiscoveryError) {
	descriptors, failure := s.listTargetDescriptorsLocked(ctx, browser)
	if failure != nil {
		return nil, failure
	}
	targets, failure := s.normalizeTargetsLocked(ctx, browser, descriptors)
	if failure != nil {
		return nil, failure
	}
	snapshot := makeTargetSnapshot(browser, targets, resolvedTargetListOptions(options))
	s.emit(EventTargetsSnapshot, browser.ID, targetSnapshotPayload(snapshot))
	return targets, nil
}

func chooseSelectionTarget(browserID string, targets []Target, requestedID string) (Target, *DiscoveryError) {
	requestedID = strings.TrimSpace(requestedID)
	if requestedID != "" {
		for _, target := range targets {
			if target.ID != requestedID {
				continue
			}
			if target.Type == "page" && !target.WebMCP {
				return Target{}, newUnsupportedWebMCP(browserID, target.ID)
			}
			if !target.Eligible {
				return Target{}, newNoEligibleTab(browserID, TargetListOptions{
					BrowserID:    browserID,
					TargetID:     requestedID,
					EligibleOnly: Bool(true),
				}, len(targets))
			}
			return target, nil
		}
		// Exact selection must never substitute a similarly named or newly
		// discovered target.
		return Target{}, newNoEligibleTab(browserID, TargetListOptions{
			BrowserID:    browserID,
			TargetID:     requestedID,
			EligibleOnly: Bool(true),
		}, len(targets))
	}

	candidates := make([]Target, 0, len(targets))
	for _, target := range targets {
		if target.Eligible {
			candidates = append(candidates, target)
		}
	}
	if len(candidates) == 0 {
		return Target{}, newNoEligibleTab(browserID, TargetListOptions{
			BrowserID:            browserID,
			EligibleOnly:         Bool(true),
			IncludeZeroToolPages: true,
		}, len(targets))
	}
	if len(candidates) > 1 {
		ids := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			ids = append(ids, candidate.ID)
		}
		sort.Strings(ids)
		return Target{}, newAmbiguousTab(browserID, ids)
	}
	// A unique candidate is useful to listing and future auto-select policy,
	// but it is still not an exact selection request.
	return Target{}, newNoEligibleTab(browserID, TargetListOptions{
		BrowserID:            browserID,
		EligibleOnly:         Bool(true),
		IncludeZeroToolPages: true,
	}, len(targets))
}

// Event emission for selection carries target identity and generation in
// dedicated normalized fields as well as the frozen semantic payload.
func (s *Service) emitTarget(kind EventType, browserID, targetID string, generation uint64, payload map[string]any) {
	s.eventSequence++
	copyPayload := make(map[string]any, len(payload))
	for key, value := range payload {
		copyPayload[key] = value
	}
	s.eventSink.Emit(Event{
		Version:     BrowserEventsVersion,
		Sequence:    s.eventSequence,
		MonotonicMS: s.eventSequence,
		Type:        kind,
		BrowserID:   browserID,
		TargetID:    targetID,
		Generation:  generation,
		Payload:     copyPayload,
		Redaction: Redaction{
			Mode:  "redacted",
			Rules: []string{"url_query", "url_fragment", "raw_cdp_disabled"},
		},
	})
}
