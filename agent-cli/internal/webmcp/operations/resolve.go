package operations

import (
	"context"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/direct"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production/normalize"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/selectionstore"
)

const (
	targetTypePage            = "page"
	eligibilityUnsupported    = "unsupported_webmcp"
	reasonNotPage             = "not_page"
	reasonOriginMismatch      = "origin_mismatch"
	reasonSelectorMismatch    = "selector_browser_mismatch"
	reasonPersistedMissing    = "persisted_selection_missing"
	reasonSelectionRequired   = "selection_required"
	reasonPersistedInvalid    = "persisted_selection_invalid"
	reasonPersistedIncomplete = "persisted_selection_incomplete"
)

// ResolveTarget resolves the exact target for a command. Explicit command
// selectors take precedence over both config and the persisted record. When
// no target selector is supplied, an existing persisted record is used as an
// exact opaque ID, never as a hint for a different target.
func ResolveTarget(ctx context.Context, broker webmcp.Broker, selector Selector) (Resolution, error) {
	return resolveTarget(ctx, broker, selector, true)
}

// ResolveReplacementTarget resolves an explicit select from the live
// discovery result. The select command is the recovery operation for a
// remembered browser that was restarted, so a stale persisted identity must
// never be loaded or validated before the replacement can be selected.
func ResolveReplacementTarget(ctx context.Context, broker webmcp.Broker, selector Selector) (Resolution, error) {
	return resolveTarget(ctx, broker, selector, false)
}

// resolver carries one resolution's working identity: the requested browser
// and target IDs and the persisted record they were loaded from, if any.
type resolver struct {
	broker    webmcp.Broker
	browser   config.BrowserConfig
	browserID string
	targetID  string
	stored    *selectionstore.Selection
}

func resolveTarget(ctx context.Context, broker webmcp.Broker, selector Selector, allowPersisted bool) (Resolution, error) {
	r := &resolver{
		broker:    broker,
		browser:   selector.Browser,
		browserID: selector.Browser.Selection.Browser,
		targetID:  selector.Browser.Selection.Tab,
	}
	if err := r.applyCompositeRef(); err != nil {
		return Resolution{}, err
	}
	if allowPersisted && r.targetID == "" && !selector.TabFlagChanged && !selector.BrowserFlagChanged {
		if err := r.loadPersisted(selector.LoadSelection); err != nil {
			return Resolution{}, err
		}
	}
	candidate, err := r.resolveCandidate(ctx)
	if err != nil {
		return Resolution{Stored: r.stored}, err
	}
	targets, err := r.pageTargets(ctx, candidate)
	if err != nil {
		return Resolution{Stored: r.stored}, err
	}
	target, err := r.chooseTarget(targets)
	if err != nil {
		return Resolution{Stored: r.stored}, err
	}
	if err := r.validateTarget(target, len(targets)); err != nil {
		return Resolution{Stored: r.stored}, err
	}
	return Resolution{Candidate: candidate, Target: target, Stored: r.stored}, nil
}

// applyCompositeRef splits a browser-qualified target reference and rejects
// one that names a different browser than the explicit browser selector.
func (r *resolver) applyCompositeRef() error {
	refBrowserID, refTargetID, composite := normalize.SplitCompositeTargetRef(r.targetID)
	if !composite {
		return nil
	}
	if r.browserID != "" && r.browserID != refBrowserID {
		return webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "the target reference names a different browser than the explicit browser selector", map[string]any{
			detailBrowserID:          direct.NormalizeOpaqueID(r.browserID),
			detailTargetID:           direct.NormalizeOpaqueID(refTargetID),
			detailSelectedGeneration: uint64(0),
			detailReason:             reasonSelectorMismatch,
		})
	}
	r.browserID = refBrowserID
	r.targetID = refTargetID
	return nil
}

func (r *resolver) loadPersisted(load func() (selectionstore.Selection, error)) error {
	if load == nil {
		return persistedSelectionError("persisted browser selection could not be read", reasonPersistedInvalid)
	}
	selection, err := load()
	if err != nil {
		return persistedSelectionError("persisted browser selection could not be read", reasonPersistedInvalid)
	}
	if selection.BrowserID == "" && selection.TargetID == "" {
		return nil
	}
	if selection.BrowserID == "" || selection.TargetID == "" {
		return persistedSelectionError("persisted browser selection is incomplete", reasonPersistedIncomplete)
	}
	r.stored = &selection
	if r.browserID == "" {
		r.browserID = selection.BrowserID
	}
	r.targetID = selection.TargetID
	return nil
}

// staleOr returns the persisted-selection classification of cause when the
// resolution came from a persisted record, and fallback otherwise.
func (r *resolver) staleOr(fallback error, reason string, cause error) error {
	if r.stored == nil {
		return fallback
	}
	return r.stale(reason, cause)
}

func (r *resolver) stale(reason string, cause error) error {
	return StaleSelectionError(r.browserID, r.targetID, r.stored.Generation, reason, cause)
}

func (r *resolver) resolveCandidate(ctx context.Context) (webmcp.BrowserCandidate, error) {
	candidates, err := direct.DiscoverCandidates(ctx, r.broker, r.browser)
	if err != nil {
		return webmcp.BrowserCandidate{}, r.staleOr(err, reasonBrowserNotFound, err)
	}
	if r.browserID == "" {
		if ids := direct.BrowserCandidateIDs(candidates); len(ids) != 1 {
			return webmcp.BrowserCandidate{}, webmcp.NewClassifiedError(webmcp.ErrorAmbiguousBrowser, "multiple browsers matched; an exact browser ID is required", map[string]any{
				"candidate_browser_ids": ids,
			})
		}
		r.browserID = string(candidates[0].ID)
	}
	for _, candidate := range candidates {
		if string(candidate.ID) == r.browserID {
			return candidate, r.checkPersistedBrowser(candidate)
		}
	}
	return webmcp.BrowserCandidate{}, r.missingBrowser(candidates)
}

func (r *resolver) missingBrowser(candidates []webmcp.BrowserCandidate) error {
	if r.stored != nil {
		if reason, replacement := direct.ReplacementReason(candidates, r.browser, *r.stored); replacement {
			return r.stale(reason, nil)
		}
	}
	err := webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "the selected browser is no longer current", map[string]any{
		detailBrowserID:          r.browserID,
		detailTargetID:           r.targetID,
		detailSelectedGeneration: persistedGeneration(r.stored),
		detailReason:             reasonBrowserNotFound,
	})
	return r.staleOr(err, reasonBrowserNotFound, err)
}

func (r *resolver) checkPersistedBrowser(candidate webmcp.BrowserCandidate) error {
	if r.stored == nil {
		return nil
	}
	if r.stored.EndpointID != "" && r.stored.EndpointID != string(candidate.ID) {
		return r.stale(reasonEndpointChanged, nil)
	}
	if r.stored.BrowserInstanceID != candidate.BrowserInstanceID && (r.stored.BrowserInstanceID != "" || candidate.BrowserInstanceID != "") {
		return r.stale(reasonInstanceChanged, nil)
	}
	return nil
}

func (r *resolver) pageTargets(ctx context.Context, candidate webmcp.BrowserCandidate) ([]webmcp.Target, error) {
	targets, err := r.broker.ListTargets(ctx, webmcp.BrowserSelector{BrowserID: candidate.ID})
	if err != nil {
		return nil, r.staleOr(err, reasonTargetListFailed, err)
	}
	targets = direct.PageTargetCandidates(targets)
	for index := range targets {
		if targets[index].BrowserID == "" {
			targets[index].BrowserID = candidate.ID
		}
	}
	return targets, nil
}

func (r *resolver) chooseTarget(targets []webmcp.Target) (webmcp.Target, error) {
	if r.targetID != "" {
		return r.exactTarget(targets)
	}
	return r.autoSelectTarget(targets)
}

func (r *resolver) exactTarget(targets []webmcp.Target) (webmcp.Target, error) {
	for _, target := range targets {
		if string(target.ID) == r.targetID {
			return target, nil
		}
	}
	err := webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "the selected browser target is no longer current", map[string]any{
		detailBrowserID:          r.browserID,
		detailTargetID:           r.targetID,
		detailSelectedGeneration: uint64(0),
		detailReason:             reasonTargetNotFound,
	})
	return webmcp.Target{}, r.staleOr(err, reasonTargetNotFound, err)
}

func (r *resolver) autoSelectTarget(targets []webmcp.Target) (webmcp.Target, error) {
	matches := direct.EligibleTargetMatches(targets, r.browser)
	autoSelect := r.browser.Selection.AutoSelect
	switch {
	case len(matches) == 0:
		return webmcp.Target{}, direct.NoEligibleTabError(r.browserID, r.browser, len(targets), "")
	case len(matches) > 1:
		return webmcp.Target{}, webmcp.NewClassifiedError(webmcp.ErrorAmbiguousTab, "multiple eligible browser targets matched; an exact target ID is required", map[string]any{
			detailBrowserID:        direct.NormalizeOpaqueID(r.browserID),
			"candidate_target_ids": direct.AmbiguityTargetIDs(matches),
			"candidate_choices":    direct.CandidateChoicesForTargets(r.browserID, matches),
		})
	case autoSelect == config.BrowserAutoSelectPersisted:
		return webmcp.Target{}, r.unselectedError("persisted browser target selection is not current", reasonPersistedMissing)
	case autoSelect == config.BrowserAutoSelectOff || autoSelect == "":
		return webmcp.Target{}, r.unselectedError("no browser target is selected; provide an exact target ID or enable auto-selection", reasonSelectionRequired)
	default:
		return matches[0], nil
	}
}

func (r *resolver) unselectedError(message, reason string) error {
	return webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, message, map[string]any{
		detailBrowserID:          r.browserID,
		detailTargetID:           "",
		detailSelectedGeneration: uint64(0),
		detailReason:             reason,
	})
}

// validateTarget checks the chosen target against the persisted record, the
// origin policy, page type, WebMCP eligibility, and the origin selector.
func (r *resolver) validateTarget(target webmcp.Target, candidateCount int) error {
	if err := r.checkPersistedTarget(target); err != nil {
		return err
	}
	if err := direct.TargetPolicyError(target, r.browser); err != nil {
		return err
	}
	if target.Type != "" && !strings.EqualFold(target.Type, targetTypePage) {
		return direct.NoEligibleTabError(r.browserID, r.browser, candidateCount, reasonNotPage)
	}
	if !target.Eligible {
		return r.ineligibleError(target, candidateCount)
	}
	origin := r.browser.Selection.Origin
	if origin != "" && normalize.RedactedOrigin(target.Origin) != normalize.RedactedOrigin(origin) {
		return direct.NoEligibleTabError(r.browserID, r.browser, candidateCount, reasonOriginMismatch)
	}
	return nil
}

func (r *resolver) checkPersistedTarget(target webmcp.Target) error {
	stored := r.stored
	if stored == nil {
		return nil
	}
	if stored.Origin != "" && normalize.RedactedOrigin(stored.Origin) != normalize.RedactedOrigin(target.Origin) {
		return r.stale(reasonOriginChanged, nil)
	}
	if stored.ContinuityMarker != "" && target.ContinuityMarker != "" && stored.ContinuityMarker != target.ContinuityMarker {
		return r.stale(reasonContinuityChanged, nil)
	}
	if stored.Generation != 0 && target.Generation != 0 && stored.Generation != target.Generation {
		return r.stale(reasonGenerationChanged, nil)
	}
	return nil
}

func (r *resolver) ineligibleError(target webmcp.Target, candidateCount int) error {
	if strings.EqualFold(target.EligibilityReason, eligibilityUnsupported) {
		return webmcp.NewClassifiedError(webmcp.ErrorUnsupportedWebMCP, "the selected target does not provide WebMCP", map[string]any{
			detailBrowserID:       r.browserID,
			detailTargetID:        r.targetID,
			"required_capability": "webmcp",
		})
	}
	return direct.NoEligibleTabError(r.browserID, r.browser, candidateCount, direct.BoundedReason(target.EligibilityReason))
}
