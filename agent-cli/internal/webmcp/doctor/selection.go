package doctor

import (
	"context"
	"sort"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/direct"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production/normalize"
)

const (
	messageNoEligibleTab = "no eligible WebMCP target was found"

	unselectedWarning = "No target selected; run `yui webmcp tabs` and `yui webmcp select` or set browser.selection.auto_select."
)

func chooseCandidate(candidates []webmcp.BrowserCandidate, browserID string) (webmcp.BrowserCandidate, error) {
	if browserID != "" {
		for _, candidate := range candidates {
			if string(candidate.ID) == browserID {
				return candidate, nil
			}
		}
		return webmcp.BrowserCandidate{}, direct.StaleBrowserError(browserID)
	}
	if len(candidates) != 1 {
		ids := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			ids = append(ids, string(candidate.ID))
		}
		sort.Strings(ids)
		return webmcp.BrowserCandidate{}, webmcp.NewClassifiedError(webmcp.ErrorAmbiguousBrowser, "multiple browsers matched; an exact browser ID is required", map[string]any{
			"candidate_browser_ids": ids,
		})
	}
	return candidates[0], nil
}

// originFilter accumulates the targets excluded by origin policy so the
// first denied target, or an explicitly requested one, can be reported.
type originFilter struct {
	browser        config.BrowserConfig
	deniedTarget   *webmcp.Target
	explicitDenied bool
}

func (f *originFilter) deny(target webmcp.Target, overrideFirst bool) {
	explicit := f.browser.Selection.Tab != "" && string(target.ID) == f.browser.Selection.Tab
	if explicit {
		f.explicitDenied = true
	}
	if f.deniedTarget == nil || explicit && overrideFirst {
		copyTarget := target
		f.deniedTarget = &copyTarget
	}
}

// candidates keeps eligible page targets matching the origin filter that
// the deny list does not exclude.
func (f *originFilter) candidates(targets []webmcp.Target) []webmcp.Target {
	eligible := make([]webmcp.Target, 0, len(targets))
	for _, target := range targets {
		if !isPageTarget(target) || !target.Eligible {
			continue
		}
		origin := normalize.RedactedOrigin(target.Origin)
		if f.browser.Selection.Origin != "" && origin != normalize.RedactedOrigin(f.browser.Selection.Origin) {
			continue
		}
		if direct.DeniedOrigin(origin, f.browser.Policy) {
			f.deny(target, false)
			continue
		}
		eligible = append(eligible, target)
	}
	return eligible
}

// allowed keeps the targets on the allow list when one is configured.
func (f *originFilter) allowed(eligible []webmcp.Target) []webmcp.Target {
	if len(f.browser.Policy.AllowedOrigins) == 0 {
		return eligible
	}
	allowed := make([]webmcp.Target, 0, len(eligible))
	for _, target := range eligible {
		if direct.AllowedOrigin(normalize.RedactedOrigin(target.Origin), f.browser.Policy.AllowedOrigins) {
			allowed = append(allowed, target)
			continue
		}
		f.deny(target, true)
	}
	return allowed
}

// selectionTargets applies the origin filter and policy to targets. It
// fails when an explicitly requested target, or every candidate, was denied.
func selectionTargets(targets []webmcp.Target, browser config.BrowserConfig) ([]webmcp.Target, error) {
	filter := &originFilter{browser: browser}
	eligible := filter.allowed(filter.candidates(targets))
	if (filter.explicitDenied || len(eligible) == 0) && filter.deniedTarget != nil {
		origin := normalize.RedactedOrigin(filter.deniedTarget.Origin)
		return nil, direct.OriginDeniedError(origin, browser.Policy)
	}
	sort.SliceStable(eligible, func(i, j int) bool { return eligible[i].ID < eligible[j].ID })
	return eligible, nil
}

// chooseTarget resolves the configured selection against the eligible
// targets. A nil target with a warning means no target is selected yet.
func chooseTarget(targets []webmcp.Target, selection config.BrowserSelectionConfig) (*webmcp.Target, string, error) {
	if selection.Tab != "" {
		return chooseExplicitTarget(targets, selection)
	}
	switch selection.AutoSelect {
	case config.BrowserAutoSelectSingle:
		return chooseSingleTarget(targets)
	case config.BrowserAutoSelectPersisted:
		return nil, "", staleTargetError(selection.Browser, "", "persisted browser target selection is not current", "persisted_selection_missing")
	case config.BrowserAutoSelectOff, "":
		if len(targets) == 0 {
			return nil, "", noEligibleTargetError()
		}
		return nil, unselectedWarning, nil
	default:
		return nil, "", webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "browser target auto-selection is invalid", map[string]any{keyReason: "invalid_auto_select"})
	}
}

func chooseExplicitTarget(targets []webmcp.Target, selection config.BrowserSelectionConfig) (*webmcp.Target, string, error) {
	for index := range targets {
		if string(targets[index].ID) == selection.Tab {
			selected := targets[index]
			return &selected, "", nil
		}
	}
	return nil, "", staleTargetError(selection.Browser, selection.Tab, "the selected browser target is no longer current", "target_not_found")
}

func chooseSingleTarget(targets []webmcp.Target) (*webmcp.Target, string, error) {
	if len(targets) == 0 {
		return nil, "", noEligibleTargetError()
	}
	if len(targets) > 1 {
		return nil, "", webmcp.NewClassifiedError(webmcp.ErrorAmbiguousTab, "multiple eligible browser targets matched; an exact target ID is required", map[string]any{
			keyBrowserID:           direct.NormalizeOpaqueID(string(targets[0].BrowserID)),
			"candidate_target_ids": direct.AmbiguityTargetIDs(targets),
			"candidate_choices":    direct.CandidateChoicesForTargets(string(targets[0].BrowserID), targets),
		})
	}
	selected := targets[0]
	return &selected, "", nil
}

func staleTargetError(browserID, targetID, message, reason string) error {
	return webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, message, map[string]any{
		keyBrowserID:   browserID,
		keyTargetID:    targetID,
		keySelectedGen: uint64(0),
		keyReason:      reason,
	})
}

func noEligibleTargetError() error {
	return webmcp.NewClassifiedError(webmcp.ErrorNoEligibleTab, messageNoEligibleTab, map[string]any{keyCandidateCount: 0})
}

// optionSelector is implemented by brokers that can activate the tab they
// select.
type optionSelector interface {
	SelectWithOptions(context.Context, webmcp.TargetSelector, webmcp.SelectOptions) (webmcp.PageContext, error)
}

func selectTarget(ctx context.Context, broker webmcp.Broker, target *webmcp.Target, activate bool) (webmcp.PageContext, error) {
	selector := webmcp.TargetSelector{BrowserID: target.BrowserID, TargetID: target.ID}
	if withOptions, ok := broker.(optionSelector); ok {
		return withOptions.SelectWithOptions(ctx, selector, webmcp.SelectOptions{Activate: activate})
	}
	return broker.Select(ctx, selector)
}
